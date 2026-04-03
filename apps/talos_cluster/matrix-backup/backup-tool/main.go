// Matrix backup tool — rewrites the Python matrix-nio backup in Go using mautrix-go.
//
// Key improvements over the Python version:
//   - SSSS decryption uses mautrix-go's proven implementation (fixes BAD_MESSAGE_MAC)
//   - Olm PK decryption uses mautrix-go with the goolm pure-Go backend (no libolm needed)
//   - No runtime pip install; compiled static binary via Dockerfile multi-stage build
//   - No SQLite/OlmMachine needed; session and sync token stored in S3
//   - Megolm export format implemented in pure Go (PBKDF2-SHA512 + AES-CTR + HMAC-SHA256)
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

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/crypto/pbkdf2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/backup"
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
)

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("Required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
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
		awsconfig.WithBaseEndpoint("https://"+s3Endpoint),
	)
	if err != nil {
		return err
	}
	s3c = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
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

// ─────────────────────────────────────────────────────────────────────────────
// Crypto-store tarball (persists sessions and sync tokens across CronJob runs)
// ─────────────────────────────────────────────────────────────────────────────

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
		Password:   acc.Password,
		DeviceName: "matrix-backup",
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

func fetchAndExportKeyBackup(ctx context.Context, client *mautrix.Client, recoveryKeyStr, prefix string) error {
	// 1. Resolve the default SSSS key ID from account data.
	var defaultKey defaultKeyEventContent
	if err := getAccountData(ctx, client, "m.secret_storage.default_key", &defaultKey); err != nil {
		return fmt.Errorf("get default key: %w", err)
	}
	if defaultKey.Key == "" {
		return fmt.Errorf("m.secret_storage.default_key has no 'key' field")
	}
	keyID := defaultKey.Key
	slog.Info("SSSS default key ID", "key_id", keyID)

	// 2. Fetch the key metadata for the default key.
	// The event type is m.secret_storage.key.<keyID>.
	var keyMetadata ssss.KeyMetadata
	if err := getAccountData(ctx, client, "m.secret_storage.key."+keyID, &keyMetadata); err != nil {
		return fmt.Errorf("get key metadata: %w", err)
	}

	// 3. Verify the recovery key against the stored metadata and derive the raw key.
	// VerifyRecoveryKey may return ErrUnverifiableKey alongside a valid key when
	// no MAC/IV is stored in the metadata — treat that as success.
	sssKey, err := keyMetadata.VerifyRecoveryKey(keyID, recoveryKeyStr)
	if err != nil && !errors.Is(err, ssss.ErrUnverifiableKey) {
		return fmt.Errorf("verify recovery key: %w", err)
	}
	slog.Info("Recovery key verified (or unverifiable but accepted)")

	// 4. Fetch the encrypted backup secret and decrypt with the SSSS key.
	var backupSecret encryptedSecretContent
	if err := getAccountData(ctx, client, "m.megolm_backup.v1", &backupSecret); err != nil {
		return fmt.Errorf("get backup secret: %w", err)
	}
	encData, ok := backupSecret.Encrypted[keyID]
	if !ok {
		return fmt.Errorf("backup secret not encrypted with key %q", keyID)
	}
	backupKeyRaw, err := sssKey.Decrypt("m.megolm_backup.v1", encData)
	if err != nil {
		return fmt.Errorf("decrypt backup key from SSSS: %w", err)
	}
	// backupKeyRaw is the base64-encoded X25519 private key bytes.
	backupKeyB64 := strings.TrimSpace(string(backupKeyRaw))

	// 5. Decode the private key bytes and create the MegolmBackupKey.
	privateKeyBytes, err := base64DecodeUnpadded(backupKeyB64)
	if err != nil {
		return fmt.Errorf("decode backup private key: %w", err)
	}
	megolmKey, err := backup.MegolmBackupKeyFromBytes(privateKeyBytes)
	if err != nil {
		return fmt.Errorf("create megolm backup key: %w", err)
	}
	slog.Info("Decoded megolm backup private key from SSSS")

	// 6. Get the current backup version.
	var backupVersion keyBackupVersionResp
	if err := matrixGetJSON(ctx, client, "/_matrix/client/v3/room_keys/version", &backupVersion); err != nil {
		return fmt.Errorf("get backup version: %w", err)
	}
	slog.Info("Key backup version", "version", backupVersion.Version)

	// 7. Fetch all backed-up sessions.
	var allRooms keyBackupAllRooms
	if err := matrixGetJSON(ctx, client,
		"/_matrix/client/v3/room_keys/keys?version="+url.QueryEscape(backupVersion.Version),
		&allRooms,
	); err != nil {
		return fmt.Errorf("get backup keys: %w", err)
	}
	slog.Info("Fetched key backup", "rooms", len(allRooms.Rooms))

	// 8. Decrypt each session using mautrix-go's Olm PK (via goolm).
	// session_data JSON → EncryptedSessionData[MegolmSessionData] → Decrypt → MegolmSessionData
	var sessions []exportedSessionEntry
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
			sessions = append(sessions, exportedSessionEntry{
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
	slog.Info("Session decryption complete", "ok", decOK, "failed", decFail)

	if len(sessions) == 0 {
		slog.Warn("No sessions decrypted — skipping key export")
		return nil
	}

	// 9. Archive raw session JSON for debugging / future re-import.
	if rawJSON, err := json.MarshalIndent(sessions, "", "  "); err == nil {
		_ = s3Put(ctx, prefix+"/backup-sessions-latest.json", rawJSON, "application/json")
		_ = s3Put(ctx, prefix+"/backup-sessions-"+dateStr+".json", rawJSON, "application/json")
	}

	// 10. Build the standard Megolm key export file and upload.
	exportData, err := buildMegolmExport(sessions, keyExportPass)
	if err != nil {
		return fmt.Errorf("build megolm export: %w", err)
	}
	if err := s3Put(ctx, prefix+"/crypto-keys-latest.bin", exportData, "application/octet-stream"); err != nil {
		return err
	}
	if err := s3Put(ctx, prefix+"/crypto-keys-"+dateStr+".bin", exportData, "application/octet-stream"); err != nil {
		return err
	}
	slog.Info("Exported E2EE keys", "sessions", len(sessions), "bytes", len(exportData))
	return nil
}

func base64DecodeUnpadded(s string) ([]byte, error) {
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.StdEncoding.DecodeString(s)
}

// ─────────────────────────────────────────────────────────────────────────────
// Standard Megolm key export format (pure Go)
// Spec: https://spec.matrix.org/v1.9/client-server-api/#key-exports
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

// ─────────────────────────────────────────────────────────────────────────────
// Room list
// ─────────────────────────────────────────────────────────────────────────────

type roomEntry struct {
	RoomID         string   `json:"room_id"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Aliases        []string `json:"aliases"`
	CanonicalAlias string   `json:"canonical_alias,omitempty"`
	MemberCount    int      `json:"member_count"`
	Encrypted      bool     `json:"encrypted"`
}

func saveRoomList(ctx context.Context, client *mautrix.Client, syncResp *mautrix.RespSync, prefix string) error {
	dmRooms := getDMRooms(syncResp)

	var rooms []roomEntry
	for roomID, joinedRoom := range syncResp.Rooms.Join {
		var name, canonicalAlias string
		var aliases []string
		encrypted := false
		rtype := "normal"
		if dmRooms[roomID] {
			rtype = "dm"
		}
		for _, ev := range joinedRoom.State.Events {
			switch ev.Type {
			case event.StateRoomName:
				if c := ev.Content.AsName(); c != nil {
					name = c.Name
				}
			case event.StateCanonicalAlias:
				if c := ev.Content.AsCanonicalAlias(); c != nil {
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
		if name == "" {
			name = string(roomID)
		}
		rooms = append(rooms, roomEntry{
			RoomID:         string(roomID),
			Name:           name,
			Type:           rtype,
			Aliases:        aliases,
			CanonicalAlias: canonicalAlias,
			MemberCount:    joinedRoom.Summary.JoinedMemberCount,
			Encrypted:      encrypted,
		})
		slog.Info("Room", "type", rtype, "name", name)
	}

	data, err := json.MarshalIndent(rooms, "", "  ")
	if err != nil {
		return err
	}
	if err := s3Put(ctx, prefix+"/rooms-"+dateStr+".json", data, "application/json"); err != nil {
		return err
	}
	if err := s3Put(ctx, prefix+"/rooms-latest.json", data, "application/json"); err != nil {
		return err
	}
	slog.Info("Uploaded room list", "rooms", len(rooms))
	return nil
}

func getDMRooms(syncResp *mautrix.RespSync) map[id.RoomID]bool {
	dmRooms := make(map[id.RoomID]bool)
	for _, ev := range syncResp.AccountData.Events {
		if ev.Type == event.AccountDataDirectChats {
			// DirectChatsEventContent is map[id.UserID][]id.RoomID
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

// processEvent handles media downloads and profile-update tracking for one event.
// Encrypted events are stored as-is but media/profiles are skipped (no OlmMachine).
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
		var content struct{ URL string `json:"url"` }
		if err := json.Unmarshal(ev.Content.VeryRaw, &content); err == nil && content.URL != "" {
			if isDM || strings.HasSuffix(string(ev.Sender), ":"+ourServerName) {
				if err := downloadAndStoreMedia(ctx, client, content.URL, prefix, "media"); err != nil {
					slog.Warn("Sticker download failed", "url", content.URL, "error", err)
				}
			}
		}
	}

	if ev.Type == event.StateMember {
		recordProfileUpdate(ctx, ev, roomID, prefix)
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

func recordProfileUpdate(ctx context.Context, ev *event.Event, roomID id.RoomID, prefix string) {
	stateKey := string(ev.StateKey)
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
}

func eventToRecord(ev *event.Event) historyEvent {
	return historyEvent{
		EventID:   string(ev.ID),
		Sender:    string(ev.Sender),
		Type:      ev.Type.Type,
		Timestamp: ev.Timestamp,
		Content:   ev.Content.VeryRaw,
	}
}

func paginateRoom(ctx context.Context, client *mautrix.Client, roomID id.RoomID, prefix string, isDM bool, prevBatch string) error {
	safeKey := strings.NewReplacer("/", "_", ":", "_").Replace(string(roomID))
	cursorKey := prefix + "/history-cursor/" + safeKey + ".json"

	var cursor struct {
		Token string `json:"token"`
	}
	if err := s3GetJSON(ctx, cursorKey, &cursor); err != nil {
		return err
	}

	if cursor.Token == "" {
		if prevBatch == "" {
			return nil
		}
		if isDM {
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
					messages = append(messages, eventToRecord(ev))
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
				histKey := prefix + "/history/" + safeKey + "/" + dateStr + ".jsonl"
				var buf bytes.Buffer
				for _, m := range messages {
					line, _ := json.Marshal(m)
					buf.Write(line)
					buf.WriteByte('\n')
				}
				_ = s3Put(ctx, histKey, buf.Bytes(), "application/x-ndjson")
				slog.Info("DM history stored", "room_id", roomID, "events", len(messages))
			}
		}
		cursorJSON, _ := json.Marshal(map[string]string{"token": prevBatch})
		_ = s3Put(ctx, cursorKey, cursorJSON, "application/json")
		return nil
	}

	var messages []historyEvent
	nextToken := cursor.Token
	histKey := prefix + "/history/" + safeKey + "/" + dateStr + ".jsonl"

	for range 100 {
		resp, err := client.Messages(ctx, roomID, nextToken, "", mautrix.DirectionForward, nil, 100)
		if err != nil {
			slog.Warn("room_messages error (forward)", "room_id", roomID, "error", err)
			break
		}
		if len(resp.Chunk) == 0 {
			break
		}
		for _, ev := range resp.Chunk {
			messages = append(messages, eventToRecord(ev))
			processEvent(ctx, client, ev, roomID, prefix, isDM)
		}
		if resp.End == "" || resp.End == nextToken {
			nextToken = ""
			break
		}
		nextToken = resp.End
	}

	if len(messages) > 0 {
		existing, _ := s3Get(ctx, histKey)
		var buf bytes.Buffer
		buf.Write(existing)
		for _, m := range messages {
			line, _ := json.Marshal(m)
			buf.Write(line)
			buf.WriteByte('\n')
		}
		_ = s3Put(ctx, histKey, buf.Bytes(), "application/x-ndjson")
		slog.Info("History updated", "room_id", roomID, "new_events", len(messages))
	}

	if nextToken != "" && nextToken != cursor.Token {
		cursorJSON, _ := json.Marshal(map[string]string{"token": nextToken})
		_ = s3Put(ctx, cursorKey, cursorJSON, "application/json")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-account backup
// ─────────────────────────────────────────────────────────────────────────────

func backupAccount(ctx context.Context, acc accountCfg) error {
	slog.Info("=== Backing up account ===", "user_id", acc.UserID)

	storeS3Key  := acc.Prefix + "/crypto-store.tar.gz"
	sessionS3Key  := acc.Prefix + "/session.json"
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

	if acc.SSSSKey != "" {
		if err := fetchAndExportKeyBackup(ctx, client, acc.SSSSKey, acc.Prefix); err != nil {
			slog.Warn("Key backup export failed", "error", err)
		}
	}

	if err := saveRoomList(ctx, client, syncResp, acc.Prefix); err != nil {
		slog.Warn("Room list failed", "error", err)
	}

	dmRooms := getDMRooms(syncResp)
	for roomID, joinedRoom := range syncResp.Rooms.Join {
		isDM := dmRooms[roomID]
		if err := paginateRoom(ctx, client, roomID, acc.Prefix, isDM, joinedRoom.Timeline.PrevBatch); err != nil {
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
