// keys.go — SSSS key derivation, Megolm key-backup export, and in-memory
// session decryption used during history pagination.
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/goolm/session"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type defaultKeyEventContent struct {
	Key string `json:"key"`
}

// encryptedSecretContent matches the `{"encrypted": {"<keyID>": {...}}}` format.
type encryptedSecretContent struct {
	Encrypted map[string]ssss.EncryptedKeyData `json:"encrypted"`
}

type keyBackupVersionResp struct {
	Version   string `json:"version"`
	Algorithm string `json:"algorithm"`
}

type keyBackupRoomSession struct {
	FirstMessageIndex int             `json:"first_message_index"`
	ForwardedCount    int             `json:"forwarded_count"`
	IsVerified        bool            `json:"is_verified"`
	SessionData       json.RawMessage `json:"session_data"`
}

type keyBackupRoom struct {
	Sessions map[string]keyBackupRoomSession `json:"sessions"`
}

type keyBackupAllRooms struct {
	Rooms map[id.RoomID]keyBackupRoom `json:"rooms"`
}

// exportedSessionEntry matches the standard Megolm key export format.
type exportedSessionEntry struct {
	Algorithm                    string            `json:"algorithm"`
	ForwardingCurve25519KeyChain []string          `json:"forwarding_curve25519_key_chain"`
	RoomID                       string            `json:"room_id"`
	SenderClaimedKeys            map[string]string `json:"sender_claimed_keys"`
	SenderKey                    string            `json:"sender_key"`
	SessionID                    string            `json:"session_id"`
	SessionKey                   string            `json:"session_key"`
}

// megolmSessions maps session IDs to live inbound sessions ready to decrypt.
type megolmSessions map[id.SessionID]*session.MegolmInboundSession

// ─────────────────────────────────────────────────────────────────────────────
// Key-backup fetch and export
// ─────────────────────────────────────────────────────────────────────────────

// fetchAndExportKeyBackup derives the Megolm backup key from SSSS, downloads
// all room sessions from the server key backup, decrypts them, uploads a JSON
// session dump and standard key-export file to S3, and returns an in-memory
// session map for use during history pagination.
func fetchAndExportKeyBackup(ctx context.Context, client *mautrix.Client, recoveryKeyStr, prefix string) (megolmSessions, error) {
	var defaultKey defaultKeyEventContent
	if err := getAccountData(ctx, client, "m.secret_storage.default_key", &defaultKey); err != nil {
		return nil, fmt.Errorf("get default key: %w", err)
	}
	if defaultKey.Key == "" {
		return nil, fmt.Errorf("m.secret_storage.default_key has no 'key' field")
	}
	keyID := defaultKey.Key
	slog.Info("SSSS default key ID", "key_id", keyID)

	var keyMetadata ssss.KeyMetadata
	if err := getAccountData(ctx, client, "m.secret_storage.key."+keyID, &keyMetadata); err != nil {
		return nil, fmt.Errorf("get key metadata: %w", err)
	}

	// ErrUnverifiableKey means no MAC to check — the derived key is still usable.
	sssKey, err := keyMetadata.VerifyRecoveryKey(keyID, recoveryKeyStr)
	if err != nil && !errors.Is(err, ssss.ErrUnverifiableKey) {
		return nil, fmt.Errorf("verify recovery key: %w", err)
	}
	slog.Info("Recovery key verified (or unverifiable but accepted)")

	var backupSecret encryptedSecretContent
	if err := getAccountData(ctx, client, "m.megolm_backup.v1", &backupSecret); err != nil {
		return nil, fmt.Errorf("get backup secret: %w", err)
	}
	encData, ok := backupSecret.Encrypted[keyID]
	if !ok {
		return nil, fmt.Errorf("backup secret not encrypted with key %q", keyID)
	}
	backupKeyRaw, err := sssKey.Decrypt("m.megolm_backup.v1", encData)
	if err != nil {
		return nil, fmt.Errorf("decrypt backup key from SSSS: %w", err)
	}

	privateKeyBytes, err := decodeBackupPrivateKey(backupKeyRaw)
	if err != nil {
		return nil, fmt.Errorf("decode backup private key: %w", err)
	}
	megolmKey, err := backup.MegolmBackupKeyFromBytes(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("create megolm backup key: %w", err)
	}
	slog.Info("Decoded megolm backup private key from SSSS")

	var backupVersion keyBackupVersionResp
	if err := matrixGetJSON(ctx, client, "/_matrix/client/v3/room_keys/version", &backupVersion); err != nil {
		return nil, fmt.Errorf("get backup version: %w", err)
	}
	slog.Info("Key backup version", "version", backupVersion.Version)

	var allRooms keyBackupAllRooms
	if err := matrixGetJSON(ctx, client,
		"/_matrix/client/v3/room_keys/keys?version="+url.QueryEscape(backupVersion.Version),
		&allRooms,
	); err != nil {
		return nil, fmt.Errorf("get backup keys: %w", err)
	}
	slog.Info("Fetched key backup", "rooms", len(allRooms.Rooms))

	sessions := make(megolmSessions)
	var exported []exportedSessionEntry
	decOK, decFail := 0, 0
	for roomID, room := range allRooms.Rooms {
		for sessionID, sessionInfo := range room.Sessions {
			var encSD backup.EncryptedSessionData[backup.MegolmSessionData]
			if err := json.Unmarshal(sessionInfo.SessionData, &encSD); err != nil {
				slog.Warn("Failed to parse session_data",
					"room_id", roomID, "session_id", sessionID, "error", err)
				decFail++
				continue
			}
			sessionData, err := encSD.Decrypt(megolmKey)
			if err != nil {
				slog.Warn("Failed to decrypt session",
					"room_id", roomID, "session_id", sessionID, "error", err)
				decFail++
				continue
			}
			if sess, err := session.NewMegolmInboundSessionFromExport([]byte(sessionData.SessionKey)); err == nil {
				sessions[id.SessionID(sessionID)] = sess
			} else {
				slog.Warn("Failed to import Megolm session", "session_id", sessionID, "error", err)
			}
			exported = append(exported, exportedSessionEntry{
				Algorithm:                    string(sessionData.Algorithm),
				ForwardingCurve25519KeyChain: sessionData.ForwardingKeyChain,
				RoomID:                       string(roomID),
				SenderClaimedKeys:            map[string]string{"ed25519": string(sessionData.SenderClaimedKeys.Ed25519)},
				SenderKey:                    string(sessionData.SenderKey),
				SessionID:                    sessionID,
				SessionKey:                   sessionData.SessionKey,
			})
			decOK++
		}
	}
	slog.Info("Session decryption complete", "ok", decOK, "failed", decFail, "in_memory", len(sessions))

	if len(exported) == 0 {
		slog.Warn("No sessions decrypted — skipping key export")
		return sessions, nil
	}

	if rawJSON, err := json.MarshalIndent(exported, "", "  "); err == nil {
		_ = s3PutAge(ctx, prefix+"/backup-sessions-latest.json", rawJSON)
		_ = s3PutAge(ctx, prefix+"/backup-sessions-"+dateStr+".json", rawJSON)
	}

	exportData, err := buildMegolmExport(exported, keyExportPass)
	if err != nil {
		return sessions, fmt.Errorf("build megolm export: %w", err)
	}
	if err := s3PutAge(ctx, prefix+"/crypto-keys-latest.bin", exportData); err != nil {
		return sessions, err
	}
	if err := s3PutAge(ctx, prefix+"/crypto-keys-"+dateStr+".bin", exportData); err != nil {
		return sessions, err
	}
	slog.Info("Exported E2EE keys", "sessions", len(exported), "bytes", len(exportData))
	return sessions, nil
}

// decodeBackupPrivateKey extracts the raw 32-byte Curve25519 private key.
// Tries several base64 encodings used in the wild before giving up.
func decodeBackupPrivateKey(raw []byte) ([]byte, error) {
	if len(raw) == 32 {
		return raw, nil
	}
	s := strings.TrimSpace(string(raw))
	for _, enc := range []*base64.Encoding{
		base64.RawStdEncoding,
		base64.StdEncoding,
		base64.RawURLEncoding,
		base64.URLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("cannot decode %d-byte value as a 32-byte Curve25519 private key", len(raw))
}

// ─────────────────────────────────────────────────────────────────────────────
// Event decryption
// ─────────────────────────────────────────────────────────────────────────────

// tryDecryptEvent attempts in-place Megolm decryption of an m.room.encrypted
// event. Returns the event unchanged if decryption is not possible.
func tryDecryptEvent(ev *event.Event, sessions megolmSessions) *event.Event {
	if ev.Type != event.EventEncrypted || sessions == nil {
		return ev
	}
	// Events from client.Messages() only have VeryRaw set; ParseRaw must be
	// called before the typed helpers (AsEncrypted etc.) work.
	if ev.Content.Parsed == nil {
		_ = ev.Content.ParseRaw(ev.Type)
	}
	content := ev.Content.AsEncrypted()
	if content.Algorithm != id.AlgorithmMegolmV1 {
		return ev
	}
	sess, ok := sessions[content.SessionID]
	if !ok {
		return ev
	}
	plaintext, _, err := sess.Decrypt(content.MegolmCiphertext)
	if err != nil {
		slog.Debug("Megolm decrypt failed", "session_id", content.SessionID, "error", err)
		return ev
	}
	var inner struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(plaintext, &inner); err != nil {
		return ev
	}
	decrypted := *ev
	decrypted.Type = event.NewEventType(inner.Type)
	decrypted.Content = event.Content{VeryRaw: inner.Content}
	return &decrypted
}

// ─────────────────────────────────────────────────────────────────────────────
// Megolm key export (spec §14.4 — PBKDF2-SHA512 + AES-256-CTR + HMAC-SHA256)
// ─────────────────────────────────────────────────────────────────────────────

func buildMegolmExport(sessions []exportedSessionEntry, passphrase string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	ivBytes := make([]byte, 16)
	if _, err := rand.Read(ivBytes); err != nil {
		return nil, err
	}
	ivBytes[0] &= 0x7F // spec: highest bit of IV must be 0

	const iterations = 100_000
	keyMaterial := pbkdf2.Key([]byte(passphrase), salt, iterations, 64, sha512.New)
	aesKey := keyMaterial[:32]
	hmacKey := keyMaterial[32:]

	plaintext, err := json.Marshal(sessions)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCTR(block, ivBytes).XORKeyStream(ciphertext, plaintext)

	iterBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(iterBuf, uint32(iterations))

	payload := []byte{0x01}
	payload = append(payload, salt...)
	payload = append(payload, ivBytes...)
	payload = append(payload, iterBuf...)
	payload = append(payload, ciphertext...)

	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(payload)
	payload = append(payload, mac.Sum(nil)...)

	encoded := base64.StdEncoding.EncodeToString(payload)
	var sb strings.Builder
	sb.WriteString("-----BEGIN MEGOLM SESSION DATA-----\n")
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		sb.WriteString(encoded[i:end])
		sb.WriteByte('\n')
	}
	sb.WriteString("-----END MEGOLM SESSION DATA-----\n")
	return []byte(sb.String()), nil
}
