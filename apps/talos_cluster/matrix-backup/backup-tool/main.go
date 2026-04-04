// matrix-backup pulls E2EE key backups, room history, and media from a Matrix
// homeserver and stores them in S3. Accounts are configured via environment
// variables; see the deployment CronJob manifest for the full list.
//
// Room history is decrypted using the Megolm sessions fetched from the
// server-side key backup, then re-encrypted with age before uploading to S3.
// Session tokens and the device crypto store are persisted as a tarball in S3
// so incremental sync works across CronJob runs.
//
// Build: CGO_ENABLED=0 go build -tags goolm -o matrix-backup .
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/crypto/pbkdf2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/goolm/session"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

const ourServerName = "mtrnord.blog"

type accountCfg struct {
	UserID   id.UserID
	Password string
	SSSSKey  string
	Prefix   string
	StoreDir string
}

var (
	homeserver    = mustEnv("HOMESERVER")
	s3Endpoint    = mustEnv("S3_ENDPOINT")
	s3BucketName  = mustEnv("S3_BUCKET")
	s3AccessKey   = mustEnv("S3_ACCESS_KEY")
	s3SecretKey   = mustEnv("S3_SECRET_KEY")
	keyExportPass = mustEnv("KEY_EXPORT_PASSPHRASE")
	dateStr       = time.Now().UTC().Format("20060102")
	runStr        = time.Now().UTC().Format("20060102-150405")

	accounts = []accountCfg{
		{
			UserID:   "@mtrnord:mtrnord.blog",
			Password: os.Getenv("MTRNORD_PASSWORD"),
			SSSSKey:  os.Getenv("MTRNORD_SSSS_KEY"),
			Prefix:   "mtrnord",
			StoreDir: "/data/crypto/mtrnord",
		},
		{
			UserID:   "@lexi:mtrnord.blog",
			Password: os.Getenv("LEXI_PASSWORD"),
			SSSSKey:  os.Getenv("LEXI_SSSS_KEY"),
			Prefix:   "lexi",
			StoreDir: "/data/crypto/lexi",
		},
	}

	// ageRecipients holds the parsed public keys used to encrypt history files.
	ageRecipients []age.Recipient

	// ageIdentity is the optional private key used to decrypt files written by
	// this tool (loaded from AGE_PRIVATE_KEY).  Allows reading back previously
	// encrypted objects (e.g. the room list) without storing plain copies in S3.
	ageIdentity age.Identity
)

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("Required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

// initAgeRecipients parses a comma-separated list of age public keys and
// populates the global ageRecipients slice.
func initAgeRecipients(s string) error {
	for _, raw := range strings.Split(s, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		r, err := age.ParseX25519Recipient(raw)
		if err != nil {
			return fmt.Errorf("invalid age recipient %q: %w", raw, err)
		}
		ageRecipients = append(ageRecipients, r)
	}
	if len(ageRecipients) == 0 {
		return fmt.Errorf("AGE_RECIPIENTS is empty — at least one public key is required")
	}
	return nil
}

// initAgeIdentity parses the AGE_PRIVATE_KEY value (a single AGE-SECRET-KEY-1…
// line) and stores it for use when decrypting previously written .age files.
func initAgeIdentity(privKey string) error {
	privKey = strings.TrimSpace(privKey)
	if privKey == "" {
		return nil
	}
	ids, err := age.ParseIdentities(strings.NewReader(privKey))
	if err != nil {
		return fmt.Errorf("parse age identity: %w", err)
	}
	if len(ids) > 0 {
		ageIdentity = ids[0]
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Age encryption / decryption
// ─────────────────────────────────────────────────────────────────────────────

// ageEncrypt encrypts data for all ageRecipients and returns the ciphertext.
func ageEncrypt(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, ageRecipients...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// getDecryptedAgeFromS3 fetches an .age object from S3 and decrypts it using
// the configured ageIdentity.  Returns (nil, nil) when the object doesn't exist
// or no identity is available.
func getDecryptedAgeFromS3(ctx context.Context, key string) ([]byte, error) {
	if ageIdentity == nil {
		return nil, nil
	}
	enc, err := s3Get(ctx, key)
	if err != nil || enc == nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(enc), ageIdentity)
	if err != nil {
		return nil, fmt.Errorf("age decrypt %s: %w", key, err)
	}
	return io.ReadAll(r)
}

// s3PutAge age-encrypts data then uploads it under key (appending ".age").
func s3PutAge(ctx context.Context, key string, data []byte) error {
	enc, err := ageEncrypt(data)
	if err != nil {
		return fmt.Errorf("age encrypt %s: %w", key, err)
	}
	return s3Put(ctx, key+".age", enc, "application/octet-stream")
}

// ─────────────────────────────────────────────────────────────────────────────
// S3 helpers
// ─────────────────────────────────────────────────────────────────────────────

var s3c *s3.Client

func initS3(ctx context.Context) error {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("hel1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			s3AccessKey, s3SecretKey, "",
		)),
	)
	if err != nil {
		return err
	}
	endpoint := "https://" + s3Endpoint
	s3c = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = &endpoint
	})
	return nil
}

func s3Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s3c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s3BucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, nil
		}
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func s3GetJSON(ctx context.Context, key string, out interface{}) error {
	data, err := s3Get(ctx, key)
	if err != nil || data == nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func s3Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s3BucketName),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	return err
}

func s3Exists(ctx context.Context, key string) bool {
	_, err := s3c.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s3BucketName),
		Key:    aws.String(key),
	})
	return err == nil
}

// s3DeletePrefix deletes all objects whose key starts with prefix.
func s3DeletePrefix(ctx context.Context, prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket: aws.String(s3BucketName),
		Prefix: aws.String(prefix),
	})
	deleted := 0
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			if _, err := s3c.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(s3BucketName),
				Key:    obj.Key,
			}); err != nil {
				slog.Warn("Failed to delete S3 object", "key", *obj.Key, "error", err)
			} else {
				deleted++
			}
		}
	}
	slog.Info("Deleted S3 prefix", "prefix", prefix, "count", deleted)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Crypto-store tarball (persists sessions and sync tokens across CronJob runs)
// ─────────────────────────────────────────────────────────────────────────────

// downloadStore restores a previously uploaded crypto store tarball from S3.
// Missing store (first run) is not an error — the caller starts fresh.
func downloadStore(ctx context.Context, storeDir, s3Key string) error {
	data, err := s3Get(ctx, s3Key)
	if err != nil {
		return err
	}
	if data == nil {
		slog.Info("No existing store in S3, starting fresh", "key", s3Key)
		return nil
	}
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		return err
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target := storeDir + "/" + hdr.Name
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0700) //nolint:errcheck
		case tar.TypeReg:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	slog.Info("Store restored from S3", "key", s3Key, "bytes", len(data))
	return nil
}

// uploadStore tarballs the crypto store directory and uploads it to S3 for the
// next run to restore.
func uploadStore(ctx context.Context, storeDir, s3Key string) error {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := addDirToTar(tw, storeDir, "."); err != nil {
		return err
	}
	tw.Close() //nolint:errcheck
	gw.Close() //nolint:errcheck
	data := buf.Bytes()
	if err := s3Put(ctx, s3Key, data, "application/gzip"); err != nil {
		return err
	}
	slog.Info("Store saved to S3", "key", s3Key, "bytes", len(data))
	return nil
}

func addDirToTar(tw *tar.Writer, baseDir, arcBase string) error {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		srcPath := baseDir + "/" + e.Name()
		arcPath := arcBase + "/" + e.Name()
		if e.IsDir() {
			_ = tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     arcPath + "/",
				Mode:     0700,
			})
			if err := addDirToTar(tw, srcPath, arcPath); err != nil {
				return err
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		f, err := os.Open(srcPath)
		if err != nil {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     arcPath,
			Size:     info.Size(),
			Mode:     0600,
		}); err != nil {
			f.Close()
			return err
		}
		_, copyErr := io.Copy(tw, f)
		f.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Session management
// ─────────────────────────────────────────────────────────────────────────────

type sessionData struct {
	AccessToken string      `json:"access_token"`
	DeviceID    id.DeviceID `json:"device_id"`
}

// ensureSession loads a saved session from S3 or logs in with the account
// password and saves the resulting session for future runs.
func ensureSession(ctx context.Context, client *mautrix.Client, acc accountCfg, s3Key string) (*sessionData, error) {
	var sess sessionData
	if err := s3GetJSON(ctx, s3Key, &sess); err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	if sess.AccessToken != "" {
		slog.Info("Restored existing session", "device_id", sess.DeviceID)
		client.UserID = acc.UserID
		client.DeviceID = sess.DeviceID
		client.AccessToken = sess.AccessToken
		return &sess, nil
	}

	slog.Info("First run — logging in with password", "user_id", acc.UserID)
	resp, err := client.Login(ctx, &mautrix.ReqLogin{
		Type: mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{
			Type: mautrix.IdentifierTypeUser,
			User: string(acc.UserID),
		},
		Password:                 acc.Password,
		InitialDeviceDisplayName: "matrix-backup",
	})
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	sess = sessionData{
		AccessToken: resp.AccessToken,
		DeviceID:    resp.DeviceID,
	}
	client.UserID = resp.UserID
	client.DeviceID = resp.DeviceID
	client.AccessToken = resp.AccessToken
	slog.Info("Logged in", "device_id", resp.DeviceID)

	data, err := json.Marshal(sess)
	if err != nil {
		return nil, err
	}
	if err := s3Put(ctx, s3Key, data, "application/json"); err != nil {
		return nil, fmt.Errorf("save session: %w", err)
	}
	return &sess, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Matrix API helpers
// ─────────────────────────────────────────────────────────────────────────────

// matrixGetJSON makes an authenticated GET to the homeserver and JSON-decodes
// the response. Used for endpoints not wrapped by the mautrix client.
func matrixGetJSON(ctx context.Context, client *mautrix.Client, path string, out interface{}) error {
	base := strings.TrimRight(client.HomeserverURL.String(), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+client.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

func getAccountData(ctx context.Context, client *mautrix.Client, eventType string, out interface{}) error {
	path := "/_matrix/client/v3/user/" +
		url.PathEscape(client.UserID.String()) +
		"/account_data/" +
		url.PathEscape(eventType)
	return matrixGetJSON(ctx, client, path, out)
}

// ─────────────────────────────────────────────────────────────────────────────
// SSSS + Megolm key backup
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

// exportedSessionEntry matches the standard Megolm key export session format
// (https://spec.matrix.org/v1.9/client-server-api/#key-exports).
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

// fetchAndExportKeyBackup derives the Megolm backup private key from SSSS,
// decrypts every backed-up session, and uploads both a raw JSON archive and a
// standard .key export file (importable by any Matrix client) to S3.
// It also returns an in-memory session map for use when decrypting room history.
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

	// ErrUnverifiableKey just means the metadata has no MAC to check against —
	// the derived key is still usable.
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

	// Decrypt each session: build both the export list and the live session map.
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

			// Build the in-memory session for history decryption.
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

// decodeBackupPrivateKey extracts the raw 32-byte Curve25519 private key from
// whatever format the client stored it as. The spec says unpadded base64, but
// in practice standard, URL-safe, and padded variants all appear in the wild.
// If the bytes are already 32 bytes long they're used directly.
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
// Megolm event decryption
// ─────────────────────────────────────────────────────────────────────────────

// tryDecryptEvent attempts to decrypt an m.room.encrypted event using the
// in-memory session map. Returns the original event unchanged if decryption
// isn't possible (missing session, wrong algorithm, parse error).
func tryDecryptEvent(ev *event.Event, sessions megolmSessions) *event.Event {
	if ev.Type != event.EventEncrypted || sessions == nil {
		return ev
	}
	// Events from client.Messages() only have VeryRaw set; Parsed is nil until
	// ParseRaw is called explicitly.  AsEncrypted() returns an empty struct (not
	// nil) when Parsed is absent, so Algorithm would be "" and we'd bail early.
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
// Megolm key export (spec: https://spec.matrix.org/v1.9/client-server-api/#key-exports)
// ─────────────────────────────────────────────────────────────────────────────

// buildMegolmExport encodes sessions into the standard passphrase-protected key
// export format used by all Matrix clients (PBKDF2-SHA512 + AES-256-CTR + HMAC-SHA256).
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

// ─────────────────────────────────────────────────────────────────────────────
// Room list
// ─────────────────────────────────────────────────────────────────────────────

// calcRoomName implements the Matrix room display name algorithm from the spec:
// https://spec.matrix.org/v1.11/client-server-api/#calculating-the-display-name-for-a-room
//
// Priority: explicit m.room.name → canonical alias → heroes list → "Empty Room".
func calcRoomName(
	stateEvents []*event.Event,
	heroes []id.UserID,
	joinedCount, invitedCount int,
	selfUserID id.UserID,
) string {
	// 1. Explicit room name.
	for _, ev := range stateEvents {
		if ev.Type == event.StateRoomName {
			if c := ev.Content.AsRoomName(); c != nil && c.Name != "" {
				return c.Name
			}
		}
	}

	// 2. Canonical alias.
	for _, ev := range stateEvents {
		if ev.Type == event.StateCanonicalAlias {
			if c := ev.Content.AsCanonicalAlias(); c != nil && c.Alias != "" {
				return string(c.Alias)
			}
		}
	}

	// 3. Heroes list — build display names from member state events.
	memberNames := make(map[id.UserID]string)
	for _, ev := range stateEvents {
		if ev.Type == event.StateMember && ev.StateKey != nil {
			uid := id.UserID(*ev.StateKey)
			if c := ev.Content.AsMember(); c != nil && c.Displayname != "" {
				memberNames[uid] = c.Displayname
			} else {
				local, _, _ := uid.Parse()
				if local != "" {
					memberNames[uid] = local
				} else {
					memberNames[uid] = string(uid)
				}
			}
		}
	}

	// Spec says heroes should already exclude self, but filter just in case.
	var filtered []id.UserID
	for _, h := range heroes {
		if h != selfUserID {
			filtered = append(filtered, h)
		}
	}

	// Build tentative names for each hero, then disambiguate duplicates by
	// appending the server part (e.g. "mtrnord (mtrnord.blog)").
	tentativeNames := make(map[id.UserID]string, len(filtered))
	for _, h := range filtered {
		if n, ok := memberNames[h]; ok {
			tentativeNames[h] = n
		} else {
			local, _, _ := h.Parse()
			if local != "" {
				tentativeNames[h] = local
			} else {
				tentativeNames[h] = string(h)
			}
		}
	}
	nameCounts := make(map[string]int, len(filtered))
	for _, n := range tentativeNames {
		nameCounts[n]++
	}
	heroName := func(uid id.UserID) string {
		n := tentativeNames[uid]
		if nameCounts[n] > 1 {
			_, server, err := uid.Parse()
			if err == nil && server != "" {
				return n + " (" + server + ")"
			}
			return string(uid)
		}
		return n
	}

	// Total other members in the room (everyone except self).
	others := joinedCount + invitedCount - 1
	if others < 0 {
		others = 0
	}

	if len(filtered) == 0 {
		if others == 0 {
			return "Empty Room"
		}
		// Heroes list absent but we know there are members — fall through to ID.
		return ""
	}

	names := make([]string, len(filtered))
	for i, h := range filtered {
		names[i] = heroName(h)
	}

	// How many members aren't represented by the heroes list.
	unnamed := others - len(names)

	if unnamed <= 0 {
		// All others are named.
		switch len(names) {
		case 1:
			return names[0]
		case 2:
			return names[0] + " and " + names[1]
		default:
			return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
		}
	}

	// Some members aren't in the heroes list.
	switch len(names) {
	case 1:
		return fmt.Sprintf("%s and %d others", names[0], unnamed)
	case 2:
		return fmt.Sprintf("%s, %s, and %d others", names[0], names[1], unnamed)
	default:
		return fmt.Sprintf("%s, and %d others", strings.Join(names, ", "), unnamed)
	}
}

type roomEntry struct {
	RoomID         string   `json:"room_id"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Aliases        []string `json:"aliases"`
	CanonicalAlias string   `json:"canonical_alias,omitempty"`
	MemberCount    int      `json:"member_count"`
	Encrypted      bool     `json:"encrypted"`
}

// fetchRoomNameFromState fetches m.room.name then m.room.canonical_alias
// directly from the server state API.  Used when the sync delta doesn't include
// those events (common for large/public rooms that haven't changed recently).
func fetchRoomNameFromState(ctx context.Context, client *mautrix.Client, roomID id.RoomID) string {
	base := url.PathEscape(string(roomID))

	var nameContent struct {
		Name string `json:"name"`
	}
	if err := matrixGetJSON(ctx, client,
		"/_matrix/client/v3/rooms/"+base+"/state/m.room.name",
		&nameContent,
	); err == nil && nameContent.Name != "" {
		return nameContent.Name
	}

	var aliasContent struct {
		Alias string `json:"alias"`
	}
	if err := matrixGetJSON(ctx, client,
		"/_matrix/client/v3/rooms/"+base+"/state/m.room.canonical_alias",
		&aliasContent,
	); err == nil && aliasContent.Alias != "" {
		return aliasContent.Alias
	}

	return ""
}

// saveRoomList builds a JSON snapshot of all joined rooms (name, type, aliases,
// member count, encryption status) and uploads it to S3.
//
// On incremental syncs the server only returns rooms with recent activity, so
// we load the previously saved list and merge: rooms present in this sync get
// fresh data, rooms absent from this sync are kept as-is from the prior list.
func saveRoomList(ctx context.Context, client *mautrix.Client, syncResp *mautrix.RespSync, prefix string) error {
	// Load the previously persisted room list so that rooms absent from this
	// sync's delta are not lost.
	existing := make(map[string]roomEntry)
	if raw, err := getDecryptedAgeFromS3(ctx, prefix+"/rooms-latest.json.age"); err == nil && raw != nil {
		var prev []roomEntry
		if json.Unmarshal(raw, &prev) == nil {
			for _, r := range prev {
				existing[r.RoomID] = r
			}
		}
	}

	dmRooms := getDMRooms(syncResp)

	for roomID, joinedRoom := range syncResp.Rooms.Join {
		var name, canonicalAlias string
		var aliases []string
		encrypted := false
		rtype := "normal"
		if dmRooms[roomID] {
			rtype = "dm"
		}

		// ParseRaw must be called explicitly — mautrix only sets VeryRaw during
		// JSON decode; the typed helpers (AsRoomName etc.) need Parsed != nil.
		for _, ev := range joinedRoom.State.Events {
			_ = ev.Content.ParseRaw(ev.Type)
		}

		for _, ev := range joinedRoom.State.Events {
			switch ev.Type {
			case event.StateRoomName:
				if c := ev.Content.AsRoomName(); c.Name != "" {
					name = c.Name
				}
			case event.StateCanonicalAlias:
				if c := ev.Content.AsCanonicalAlias(); c.Alias != "" {
					canonicalAlias = string(c.Alias)
					for _, a := range c.AltAliases {
						aliases = append(aliases, string(a))
					}
				}
			case event.StateEncryption:
				encrypted = true
			case event.StateCreate:
				if ev.Content.VeryRaw != nil {
					var createContent struct {
						Type string `json:"type"`
					}
					if json.Unmarshal(ev.Content.VeryRaw, &createContent) == nil && createContent.Type == "m.space" {
						rtype = "space"
					}
				}
			}
		}

		joinedCount, invitedCount := 0, 0
		if joinedRoom.Summary.JoinedMemberCount != nil {
			joinedCount = *joinedRoom.Summary.JoinedMemberCount
		}
		if joinedRoom.Summary.InvitedMemberCount != nil {
			invitedCount = *joinedRoom.Summary.InvitedMemberCount
		}

		if name == "" {
			name = calcRoomName(joinedRoom.State.Events, joinedRoom.Summary.Heroes, joinedCount, invitedCount, client.UserID)
		}
		if name == "" {
			// Sync delta doesn't include the name — fetch it directly.
			name = fetchRoomNameFromState(ctx, client, roomID)
		}
		// If we still have no name, keep whatever we had from the previous run.
		if name == "" {
			if prev, ok := existing[string(roomID)]; ok {
				name = prev.Name
			}
		}
		if name == "" {
			name = string(roomID)
		}

		existing[string(roomID)] = roomEntry{
			RoomID:         string(roomID),
			Name:           name,
			Type:           rtype,
			Aliases:        aliases,
			CanonicalAlias: canonicalAlias,
			MemberCount:    joinedCount,
			Encrypted:      encrypted,
		}
		slog.Info("Room", "type", rtype, "name", name)
	}

	rooms := make([]roomEntry, 0, len(existing))
	for _, r := range existing {
		rooms = append(rooms, r)
	}

	data, err := json.MarshalIndent(rooms, "", "  ")
	if err != nil {
		return err
	}
	// Age-encrypted copies — readable by both the backup tool (via ageIdentity)
	// and the external decrypt utility.
	if err := s3PutAge(ctx, prefix+"/rooms-"+dateStr+".json", data); err != nil {
		return err
	}
	if err := s3PutAge(ctx, prefix+"/rooms-latest.json", data); err != nil {
		return err
	}
	slog.Info("Uploaded room list", "rooms", len(rooms))
	return nil
}

// getDMRooms returns a set of room IDs marked as direct chats in account data.
func getDMRooms(syncResp *mautrix.RespSync) map[id.RoomID]bool {
	dmRooms := make(map[id.RoomID]bool)
	for _, ev := range syncResp.AccountData.Events {
		if ev.Type == event.AccountDataDirectChats {
			var direct event.DirectChatsEventContent
			if err := json.Unmarshal(ev.Content.VeryRaw, &direct); err != nil {
				continue
			}
			for _, roomIDs := range direct {
				for _, rid := range roomIDs {
					dmRooms[rid] = true
				}
			}
			break
		}
	}
	return dmRooms
}

// ─────────────────────────────────────────────────────────────────────────────
// Media download
// ─────────────────────────────────────────────────────────────────────────────

// downloadAndStoreMedia downloads a single mxc:// URL and stores it under
// prefix/label/server/mediaID in S3, skipping if already present.
func downloadAndStoreMedia(ctx context.Context, client *mautrix.Client, mxcURL, prefix, label string) error {
	if !strings.HasPrefix(mxcURL, "mxc://") {
		return nil
	}
	rest := mxcURL[len("mxc://"):]
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		return nil
	}
	server := rest[:slashIdx]
	mediaID := rest[slashIdx+1:]
	if server == "" || mediaID == "" {
		return nil
	}

	s3Key := prefix + "/" + label + "/" + server + "/" + mediaID
	if s3Exists(ctx, s3Key) {
		return nil
	}

	base := strings.TrimRight(client.HomeserverURL.String(), "/")
	downloadURL := base + "/_matrix/media/v3/download/" +
		url.PathEscape(server) + "/" + url.PathEscape(mediaID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+client.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("media download HTTP %d for %s", resp.StatusCode, mxcURL)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}

	finalKey := s3Key
	if ext := extFromContentType(ct); ext != "" {
		finalKey = s3Key + ext
	}
	if err := s3Put(ctx, finalKey, data, ct); err != nil {
		return err
	}
	slog.Info("Stored media", "key", finalKey, "bytes", len(data))
	return nil
}

func extFromContentType(ct string) string {
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	switch strings.TrimSpace(ct) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "audio/mpeg":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	case "audio/opus":
		return ".opus"
	case "application/pdf":
		return ".pdf"
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// Event processing
// ─────────────────────────────────────────────────────────────────────────────

var mediaMsgTypes = map[string]bool{
	"m.image": true,
	"m.file":  true,
	"m.video": true,
	"m.audio": true,
}

// processEvent downloads media and records profile changes for a single event.
// The event should already be decrypted before this is called.
func processEvent(ctx context.Context, client *mautrix.Client, ev *event.Event, roomID id.RoomID, prefix string, isDM bool) {
	if ev.Type == event.EventEncrypted {
		return
	}

	if ev.Type == event.EventMessage {
		var content struct {
			MsgType string `json:"msgtype"`
			URL     string `json:"url"`
		}
		if err := json.Unmarshal(ev.Content.VeryRaw, &content); err == nil {
			if mediaMsgTypes[content.MsgType] && content.URL != "" {
				if isDM || strings.HasSuffix(string(ev.Sender), ":"+ourServerName) {
					if err := downloadAndStoreMedia(ctx, client, content.URL, prefix, "media"); err != nil {
						slog.Warn("Media download failed", "url", content.URL, "error", err)
					}
				}
			}
		}
	}

	if ev.Type == event.EventSticker {
		var content struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(ev.Content.VeryRaw, &content); err == nil && content.URL != "" {
			if isDM || strings.HasSuffix(string(ev.Sender), ":"+ourServerName) {
				if err := downloadAndStoreMedia(ctx, client, content.URL, prefix, "media"); err != nil {
					slog.Warn("Sticker download failed", "url", content.URL, "error", err)
				}
			}
		}
	}

	if ev.Type == event.StateMember {
		recordProfileUpdate(ctx, client, ev, roomID, prefix)
	}
}

type profileUpdateRecord struct {
	TS             int64  `json:"ts"`
	EventID        string `json:"event_id"`
	UserID         string `json:"user_id"`
	RoomID         string `json:"room_id"`
	DisplayNameOld string `json:"displayname_old,omitempty"`
	DisplayNameNew string `json:"displayname_new,omitempty"`
	AvatarOld      string `json:"avatar_old,omitempty"`
	AvatarNew      string `json:"avatar_new,omitempty"`
}

// recordProfileUpdate appends a JSONL entry to S3 when a room member event shows
// a display name or avatar change for a local user. Also downloads the new avatar.
func recordProfileUpdate(ctx context.Context, client *mautrix.Client, ev *event.Event, roomID id.RoomID, prefix string) {
	if ev.StateKey == nil {
		return
	}
	stateKey := *ev.StateKey
	if !strings.HasSuffix(stateKey, ":"+ourServerName) {
		return
	}
	var content, prevContent struct {
		Displayname string `json:"displayname"`
		AvatarURL   string `json:"avatar_url"`
	}
	if err := json.Unmarshal(ev.Content.VeryRaw, &content); err != nil {
		return
	}
	if ev.Unsigned.PrevContent != nil {
		_ = json.Unmarshal(ev.Unsigned.PrevContent.VeryRaw, &prevContent)
	}
	if content.Displayname == prevContent.Displayname && content.AvatarURL == prevContent.AvatarURL {
		return
	}
	rec := profileUpdateRecord{
		TS:      ev.Timestamp,
		EventID: string(ev.ID),
		UserID:  stateKey,
		RoomID:  string(roomID),
	}
	if content.Displayname != prevContent.Displayname {
		rec.DisplayNameOld = prevContent.Displayname
		rec.DisplayNameNew = content.Displayname
	}
	if content.AvatarURL != prevContent.AvatarURL {
		rec.AvatarOld = prevContent.AvatarURL
		rec.AvatarNew = content.AvatarURL
		if content.AvatarURL != "" {
			if err := downloadAndStoreMedia(ctx, client, content.AvatarURL, prefix, "avatars"); err != nil {
				slog.Warn("Avatar download failed", "url", content.AvatarURL, "error", err)
			}
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	s3Key := prefix + "/profile-updates.jsonl"
	existing, _ := s3Get(ctx, s3Key)
	_ = s3Put(ctx, s3Key, append(existing, append(line, '\n')...), "application/x-ndjson")
}

// ─────────────────────────────────────────────────────────────────────────────
// History pagination
// ─────────────────────────────────────────────────────────────────────────────

type historyEvent struct {
	EventID   string          `json:"event_id"`
	Sender    string          `json:"sender"`
	Type      string          `json:"type"`
	Timestamp int64           `json:"origin_server_ts"`
	Content   json.RawMessage `json:"content"`
	Encrypted bool            `json:"encrypted,omitempty"`
}

func eventToRecord(ev *event.Event, wasEncrypted bool) historyEvent {
	return historyEvent{
		EventID:   string(ev.ID),
		Sender:    string(ev.Sender),
		Type:      ev.Type.Type,
		Timestamp: ev.Timestamp,
		Content:   ev.Content.VeryRaw,
		Encrypted: wasEncrypted,
	}
}

// paginateRoom fetches room history and writes new events to a per-run
// age-encrypted JSONL file in S3. For DMs on first run, the full history is
// fetched backwards from the current position before normal forward pagination.
//
// syncNextBatch is the nextBatch token from the current sync response. On first
// run we save it as the forward cursor so the next run only sees events that
// arrive after this sync — avoiding re-fetching the events the sync already
// returned.
func paginateRoom(ctx context.Context, client *mautrix.Client, roomID id.RoomID, prefix string, isDM bool, prevBatch string, syncNextBatch string, sessions megolmSessions) error {
	safeKey := strings.NewReplacer("/", "_", ":", "_").Replace(string(roomID))
	cursorKey := prefix + "/history-cursor/" + safeKey + ".json"

	var cursor struct {
		Token string `json:"token"`
	}
	if err := s3GetJSON(ctx, cursorKey, &cursor); err != nil {
		return err
	}

	if cursor.Token == "" {
		// First time we've seen this room. For DMs, walk all the way back through
		// history so we have a complete record from day one.
		if isDM && prevBatch != "" {
			slog.Info("DM first-run: fetching full history", "room_id", roomID)
			var messages []historyEvent
			token := prevBatch
			for {
				resp, err := client.Messages(ctx, roomID, token, "", mautrix.DirectionBackward, nil, 100)
				if err != nil {
					slog.Warn("room_messages error (backward)", "room_id", roomID, "error", err)
					break
				}
				if len(resp.Chunk) == 0 {
					break
				}
				for _, ev := range resp.Chunk {
					wasEncrypted := ev.Type == event.EventEncrypted
					ev = tryDecryptEvent(ev, sessions)
					messages = append(messages, eventToRecord(ev, wasEncrypted))
					processEvent(ctx, client, ev, roomID, prefix, true)
				}
				if resp.End == "" || resp.End == token {
					break
				}
				token = resp.End
			}
			if len(messages) > 0 {
				for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
					messages[i], messages[j] = messages[j], messages[i]
				}
				histKey := prefix + "/history/" + safeKey + "/" + runStr + ".jsonl"
				var buf bytes.Buffer
				for _, m := range messages {
					line, _ := json.Marshal(m)
					buf.Write(line)
					buf.WriteByte('\n')
				}
				if err := s3PutAge(ctx, histKey, buf.Bytes()); err != nil {
					slog.Warn("Failed to upload DM history", "room_id", roomID, "error", err)
				}
				slog.Info("DM history stored", "room_id", roomID, "events", len(messages))
			}
		}
		// Save the sync's nextBatch as the forward cursor. This means the next
		// run will forward-paginate from here and only pick up genuinely new
		// events, not the batch we already received in this sync response.
		if syncNextBatch == "" {
			syncNextBatch = prevBatch
		}
		if syncNextBatch != "" {
			cursorJSON, _ := json.Marshal(map[string]string{"token": syncNextBatch})
			_ = s3Put(ctx, cursorKey, cursorJSON, "application/json")
		}
		return nil
	}

	// Incremental run: fetch every page forward from the saved cursor until we
	// reach the live end of the timeline.
	var messages []historyEvent
	nextToken := cursor.Token
	lastGoodToken := cursor.Token
	histKey := prefix + "/history/" + safeKey + "/" + runStr + ".jsonl"

	for range 100 {
		resp, err := client.Messages(ctx, roomID, nextToken, "", mautrix.DirectionForward, nil, 100)
		if err != nil {
			slog.Warn("room_messages error (forward)", "room_id", roomID, "error", err)
			break
		}
		if len(resp.Chunk) == 0 {
			// No events — we are already at the live end.
			break
		}
		for _, ev := range resp.Chunk {
			wasEncrypted := ev.Type == event.EventEncrypted
			ev = tryDecryptEvent(ev, sessions)
			messages = append(messages, eventToRecord(ev, wasEncrypted))
			processEvent(ctx, client, ev, roomID, prefix, isDM)
		}
		if resp.End == "" || resp.End == nextToken {
			// Reached the live end of the timeline; nextToken is the furthest
			// position we can name.
			lastGoodToken = nextToken
			nextToken = ""
			break
		}
		lastGoodToken = resp.End
		nextToken = resp.End
	}
	// If the loop hit the 100-page cap before reaching live end, nextToken still
	// holds the resume position for the next run.
	if nextToken != "" {
		lastGoodToken = nextToken
	}

	if len(messages) > 0 {
		var buf bytes.Buffer
		for _, m := range messages {
			line, _ := json.Marshal(m)
			buf.Write(line)
			buf.WriteByte('\n')
		}
		if err := s3PutAge(ctx, histKey, buf.Bytes()); err != nil {
			slog.Warn("Failed to upload history", "room_id", roomID, "error", err)
		}
		slog.Info("History updated", "room_id", roomID, "new_events", len(messages))
	}

	// Always advance the cursor so the next run never re-processes events we've
	// already stored.
	if lastGoodToken != cursor.Token {
		cursorJSON, _ := json.Marshal(map[string]string{"token": lastGoodToken})
		_ = s3Put(ctx, cursorKey, cursorJSON, "application/json")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-account backup
// ─────────────────────────────────────────────────────────────────────────────

// backupAccount runs the full backup pipeline for a single Matrix account:
// restore state, sync, export E2EE keys, save room list, paginate history,
// then persist state back to S3.
func backupAccount(ctx context.Context, acc accountCfg) error {
	slog.Info("=== Backing up account ===", "user_id", acc.UserID)

	storeS3Key := acc.Prefix + "/crypto-store.tar.gz"
	sessionS3Key := acc.Prefix + "/session.json"
	syncTokenS3Key := acc.Prefix + "/sync-token.json"

	if err := downloadStore(ctx, acc.StoreDir, storeS3Key); err != nil {
		slog.Warn("Could not restore store", "error", err)
	}

	client, err := mautrix.NewClient(homeserver, "", "")
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	if _, err := ensureSession(ctx, client, acc, sessionS3Key); err != nil {
		return fmt.Errorf("session: %w", err)
	}

	var syncToken struct {
		NextBatch string `json:"next_batch"`
	}
	_ = s3GetJSON(ctx, syncTokenS3Key, &syncToken)

	slog.Info("Syncing", "since", syncToken.NextBatch)
	syncResp, err := client.SyncRequest(ctx, 60000, syncToken.NextBatch, "", true, event.PresenceUnavailable)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	if data, err := json.Marshal(map[string]string{"next_batch": syncResp.NextBatch}); err == nil {
		_ = s3Put(ctx, syncTokenS3Key, data, "application/json")
	}
	slog.Info("Sync done", "rooms", len(syncResp.Rooms.Join))

	var sessions megolmSessions
	if acc.SSSSKey != "" {
		var err error
		sessions, err = fetchAndExportKeyBackup(ctx, client, acc.SSSSKey, acc.Prefix)
		if err != nil {
			slog.Warn("Key backup export failed", "error", err)
		}
	}

	if err := saveRoomList(ctx, client, syncResp, acc.Prefix); err != nil {
		slog.Warn("Room list failed", "error", err)
	}

	dmRooms := getDMRooms(syncResp)
	for roomID, joinedRoom := range syncResp.Rooms.Join {
		isDM := dmRooms[roomID]
		if err := paginateRoom(ctx, client, roomID, acc.Prefix, isDM, joinedRoom.Timeline.PrevBatch, syncResp.NextBatch, sessions); err != nil {
			slog.Warn("History error", "room_id", roomID, "error", err)
		}
	}

	if err := uploadStore(ctx, acc.StoreDir, storeS3Key); err != nil {
		slog.Warn("Could not upload store", "error", err)
	}

	slog.Info("Done", "user_id", acc.UserID)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	ctx := context.Background()

	if err := initAgeRecipients(mustEnv("AGE_RECIPIENTS")); err != nil {
		slog.Error("Failed to init age recipients", "error", err)
		os.Exit(1)
	}

	if err := initAgeIdentity(os.Getenv("AGE_PRIVATE_KEY")); err != nil {
		slog.Error("Failed to init age identity", "error", err)
		os.Exit(1)
	}

	if err := initS3(ctx); err != nil {
		slog.Error("Failed to init S3", "error", err)
		os.Exit(1)
	}

	for _, acc := range accounts {
		if err := backupAccount(ctx, acc); err != nil {
			slog.Error("Account backup failed", "user_id", acc.UserID, "error", err)
		}
	}

	slog.Info("All accounts backed up successfully")
}
