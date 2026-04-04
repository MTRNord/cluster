// recovery is a local utility for retrieving, decrypting, and displaying
// age-encrypted backup files from S3.  It does more than just decrypt: it
// reconstructs full conversation history, downloads backed-up media, and
// shows account data summaries.
//
// Credentials are read from the Kubernetes Secret YAML at
// ../secret.yaml (relative to the working directory) — the same file that the
// CronJob references.  No env vars or extra config needed.
//
// Usage (run from the backup-tool/ directory):
//
//	go run ./recovery/ -identity ~/age.key -prefix mtrnord
//	go run ./recovery/ -identity ~/age.key -prefix mtrnord -room '!roomid:server'
//	go run ./recovery/ -identity ~/age.key -prefix mtrnord -all -out ./decrypted
//
// Optional overrides:
//
//	-secret   path to secret.yaml (default: ../secret.yaml)
//	-endpoint S3 endpoint          (default: hel1.your-objectstorage.com)
//	-region   S3 region            (default: hel1)
//	-bucket   S3 bucket name       (default: midnightthoughts-matrix-backup)
//	-out      output directory     (default: ./decrypted)
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"gopkg.in/yaml.v3"
)

// ─────────────────────────────────────────────────────────────────────────────
// CLI flags
// ─────────────────────────────────────────────────────────────────────────────

var (
	flagIdentity = flag.String("identity", "", "path to age identity file (private key) [required]")
	flagPrefix   = flag.String("prefix", "", "account prefix: mtrnord or lexi [required]")
	flagSecret   = flag.String("secret", "../secret.yaml", "path to secret.yaml")
	flagEndpoint = flag.String("endpoint", "hel1.your-objectstorage.com", "S3 endpoint")
	flagRegion   = flag.String("region", "hel1", "S3 region")
	flagBucket   = flag.String("bucket", "midnightthoughts-matrix-backup", "S3 bucket")
	flagRoom     = flag.String("room", "", "room ID to decrypt (empty = list rooms only)")
	flagOut      = flag.String("out", "./decrypted", "output directory for decrypted files")
	flagAll      = flag.Bool("all", false, "dump history for ALL rooms")
)

// ─────────────────────────────────────────────────────────────────────────────
// Secret YAML parser
// ─────────────────────────────────────────────────────────────────────────────

// parseSecretYAML extracts the stringData map from a Kubernetes Secret YAML.
func parseSecretYAML(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open secret file: %w", err)
	}
	defer f.Close()

	var secret struct {
		Sops       map[string]any    `yaml:"sops"`
		StringData map[string]string `yaml:"stringData"`
	}
	if err := yaml.NewDecoder(f).Decode(&secret); err != nil {
		return nil, fmt.Errorf("decode secret yaml: %w", err)
	}
	if secret.Sops != nil {
		return nil, fmt.Errorf("secret.yaml is SOPS-encrypted — run `sops -d %s > /tmp/secret-plain.yaml` and point -secret at the plaintext file", path)
	}
	if secret.StringData == nil {
		return nil, fmt.Errorf("secret.yaml has no stringData section")
	}
	return secret.StringData, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// S3
// ─────────────────────────────────────────────────────────────────────────────

var (
	s3c          *s3.Client
	s3BucketName string
)

func initS3(ctx context.Context, endpoint, region, bucket, accessKey, secretKey string) error {
	s3BucketName = bucket
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return err
	}
	ep := "https://" + endpoint
	s3c = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = &ep
	})
	return nil
}

func s3GetBytes(ctx context.Context, key string) ([]byte, error) {
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

func listPrefix(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(s3c, &s3.ListObjectsV2Input{
		Bucket: aws.String(s3BucketName),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			keys = append(keys, *obj.Key)
		}
	}
	return keys, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Age decryption
// ─────────────────────────────────────────────────────────────────────────────

var ageIdentities []age.Identity

func loadIdentities(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open identity file: %w", err)
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return fmt.Errorf("parse identities: %w", err)
	}
	ageIdentities = ids
	return nil
}

func ageDecrypt(data []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(data), ageIdentities...)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// tryDecodeBase64 attempts to decode a base64 string using all four standard
// variants (raw/padded × URL-safe/standard) and returns the first that succeeds.
// Matrix encrypted-media fields are specified as URL-safe unpadded base64, but
// older clients used standard base64 with + and / characters.
func tryDecodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("cannot base64-decode %q", s)
}

func getDecryptedAge(ctx context.Context, key string) ([]byte, error) {
	raw, err := s3GetBytes(ctx, key)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return ageDecrypt(raw)
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
	ViaServers     []string `json:"via_servers,omitempty"`
}

func listRooms(ctx context.Context, prefix string) ([]roomEntry, error) {
	data, err := getDecryptedAge(ctx, prefix+"/rooms-latest.json.age")
	if err != nil {
		return nil, fmt.Errorf("fetch room list: %w", err)
	}
	var rooms []roomEntry
	if err := json.Unmarshal(data, &rooms); err != nil {
		return nil, fmt.Errorf("parse room list: %w", err)
	}
	return rooms, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Room account data (tags)
// ─────────────────────────────────────────────────────────────────────────────

// loadRoomAccountData returns the per-room account data map, or nil if absent.
func loadRoomAccountData(ctx context.Context, prefix string) map[string]map[string]json.RawMessage {
	raw, err := s3GetBytes(ctx, prefix+"/room-account-data-latest.json.age")
	if err != nil || raw == nil {
		return nil
	}
	plain, err := ageDecrypt(raw)
	if err != nil {
		slog.Warn("Failed to decrypt room account data", "error", err)
		return nil
	}
	var result map[string]map[string]json.RawMessage
	if err := json.Unmarshal(plain, &result); err != nil {
		slog.Warn("Failed to parse room account data", "error", err)
		return nil
	}
	return result
}

// roomTagLabel returns a short label for known room tags (empty string if none).
func roomTagLabel(roomID string, roomAccountData map[string]map[string]json.RawMessage) string {
	if roomAccountData == nil {
		return ""
	}
	rd, ok := roomAccountData[roomID]
	if !ok {
		return ""
	}
	tagRaw, ok := rd["m.tag"]
	if !ok {
		return ""
	}
	var tags struct {
		Tags map[string]json.RawMessage `json:"tags"`
	}
	if json.Unmarshal(tagRaw, &tags) != nil {
		return ""
	}
	if _, ok := tags.Tags["m.favourite"]; ok {
		return " [fav]"
	}
	if _, ok := tags.Tags["m.lowpriority"]; ok {
		return " [low]"
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// Account data summary
// ─────────────────────────────────────────────────────────────────────────────

// printAccountDataSummary prints a brief summary of global account data.
func printAccountDataSummary(ctx context.Context, prefix string) {
	raw, err := s3GetBytes(ctx, prefix+"/account-data-latest.json.age")
	if err != nil || raw == nil {
		return
	}
	plain, err := ageDecrypt(raw)
	if err != nil {
		return
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(plain, &data); err != nil {
		return
	}

	var pushRuleCount int
	if pr, ok := data["m.push_rules"]; ok {
		var rules struct {
			Global map[string][]json.RawMessage `json:"global"`
		}
		if json.Unmarshal(pr, &rules) == nil {
			for _, list := range rules.Global {
				pushRuleCount += len(list)
			}
		}
	}

	var ignoredCount int
	if il, ok := data["m.ignored_user_list"]; ok {
		var ignored struct {
			IgnoredUsers map[string]json.RawMessage `json:"ignored_users"`
		}
		if json.Unmarshal(il, &ignored) == nil {
			ignoredCount = len(ignored.IgnoredUsers)
		}
	}

	if pushRuleCount > 0 || ignoredCount > 0 {
		fmt.Printf("Account data: %d push rules, %d ignored users\n\n", pushRuleCount, ignoredCount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// History events
// ─────────────────────────────────────────────────────────────────────────────

type historyEvent struct {
	EventID   string          `json:"event_id"`
	Sender    string          `json:"sender"`
	Type      string          `json:"type"`
	Timestamp int64           `json:"origin_server_ts"`
	Content   json.RawMessage `json:"content"`
	Encrypted bool            `json:"encrypted,omitempty"`
}

// displayNameFor returns the best human-readable name for a sender.
func displayNameFor(userID string, displayNames map[string]string) string {
	if n, ok := displayNames[userID]; ok && n != "" {
		return n
	}
	if len(userID) > 1 && userID[0] == '@' {
		if i := strings.Index(userID[1:], ":"); i >= 0 {
			return userID[1 : i+1]
		}
	}
	return userID
}

// prettyContent returns a readable one-liner for common event types.
func prettyContent(evType string, rawContent json.RawMessage) string {
	var c map[string]json.RawMessage
	if json.Unmarshal(rawContent, &c) != nil {
		return string(rawContent)
	}
	switch evType {
	case "m.room.message":
		var body, msgtype string
		json.Unmarshal(c["body"], &body)       //nolint:errcheck
		json.Unmarshal(c["msgtype"], &msgtype) //nolint:errcheck
		switch msgtype {
		case "m.image":
			return "[image: " + body + "]"
		case "m.file":
			return "[file: " + body + "]"
		case "m.video":
			return "[video: " + body + "]"
		case "m.audio":
			return "[audio: " + body + "]"
		default:
			return body
		}
	case "m.room.member":
		var membership, dn string
		json.Unmarshal(c["membership"], &membership) //nolint:errcheck
		json.Unmarshal(c["displayname"], &dn)        //nolint:errcheck
		if dn != "" {
			return fmt.Sprintf("[membership: %s, displayname: %s]", membership, dn)
		}
		return fmt.Sprintf("[membership: %s]", membership)
	case "m.reaction":
		var rel map[string]json.RawMessage
		if raw, ok := c["m.relates_to"]; ok {
			if json.Unmarshal(raw, &rel) == nil {
				var key string
				json.Unmarshal(rel["key"], &key) //nolint:errcheck
				return "[reaction: " + key + "]"
			}
		}
	case "m.room.encrypted":
		return "[encrypted — session not available]"
	}
	return string(rawContent)
}

func dumpHistory(ctx context.Context, prefix, roomID, roomName, outDir string, roomAccountData map[string]map[string]json.RawMessage) error {
	safeKey := strings.NewReplacer("/", "_", ":", "_").Replace(roomID)
	histPrefix := prefix + "/history/" + safeKey + "/"

	keys, err := listPrefix(ctx, histPrefix)
	if err != nil {
		return fmt.Errorf("list history keys: %w", err)
	}
	if len(keys) == 0 {
		slog.Info("No history files found", "room_id", roomID)
		return nil
	}

	var allEvents []historyEvent
	displayNames := make(map[string]string)

	for _, key := range keys {
		if !strings.HasSuffix(key, ".age") {
			continue
		}
		data, err := getDecryptedAge(ctx, key)
		if err != nil {
			slog.Warn("Failed to decrypt", "key", key, "error", err)
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev historyEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				slog.Warn("Failed to parse event line", "error", err)
				continue
			}
			allEvents = append(allEvents, ev)
			if ev.Type == "m.room.member" {
				var mc struct {
					Displayname string `json:"displayname"`
				}
				if json.Unmarshal(ev.Content, &mc) == nil && mc.Displayname != "" {
					displayNames[ev.Sender] = mc.Displayname
				}
			}
		}
		if err := sc.Err(); err != nil {
			slog.Warn("Scanner error reading history file", "key", key, "error", err)
		}
	}

	if len(allEvents) == 0 {
		slog.Info("No events after parsing", "room_id", roomID)
		return nil
	}

	// Sort by timestamp.
	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Timestamp < allEvents[j].Timestamp
	})

	// Deduplicate by event ID.
	seen := make(map[string]struct{}, len(allEvents))
	deduped := allEvents[:0]
	for _, ev := range allEvents {
		if _, dup := seen[ev.EventID]; dup {
			continue
		}
		seen[ev.EventID] = struct{}{}
		deduped = append(deduped, ev)
	}
	allEvents = deduped

	roomDir := filepath.Join(outDir, safeKey)
	if err := os.MkdirAll(roomDir, 0700); err != nil {
		return err
	}

	// Write plain-text log with mode 0600 (contains decrypted messages).
	txtPath := filepath.Join(roomDir, "history.txt")
	tf, err := os.OpenFile(txtPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer tf.Close()

	fmt.Fprintf(tf, "Room: %s (%s)\n", roomName, roomID)
	fmt.Fprintf(tf, "Events: %d\n\n", len(allEvents))
	for _, ev := range allEvents {
		ts := time.UnixMilli(ev.Timestamp).UTC().Format("2006-01-02 15:04:05")
		sender := displayNameFor(ev.Sender, displayNames)
		content := prettyContent(ev.Type, ev.Content)
		if ev.Encrypted {
			fmt.Fprintf(tf, "[%s] <%s> [encrypted] %s\n", ts, sender, content)
		} else {
			fmt.Fprintf(tf, "[%s] <%s> %s\n", ts, sender, content)
		}
	}

	// Write raw JSONL with mode 0600.
	jsonlPath := filepath.Join(roomDir, "history.jsonl")
	jf, err := os.OpenFile(jsonlPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer jf.Close()
	enc := json.NewEncoder(jf)
	for _, ev := range allEvents {
		if err := enc.Encode(ev); err != nil {
			slog.Warn("Failed to encode event to JSONL, skipping", "event_id", ev.EventID, "error", err)
		}
	}

	// Write room tags if present.
	if roomAccountData != nil {
		if rd, ok := roomAccountData[roomID]; ok {
			if tagRaw, ok := rd["m.tag"]; ok {
				tagPath := filepath.Join(roomDir, "room-tags.json")
				if data, err := json.MarshalIndent(tagRaw, "", "  "); err == nil {
					_ = os.WriteFile(tagPath, data, 0600)
				}
			}
		}
	}

	slog.Info("Dumped room history",
		"room", roomName,
		"events", len(allEvents),
		"txt", txtPath,
		"jsonl", jsonlPath,
	)

	downloadMedia(ctx, prefix, allEvents, roomDir)
	return nil
}

// downloadMedia saves backed-up media files for the given events into a
// "media/" subdirectory inside roomDir.  Files already present are skipped.
func downloadMedia(ctx context.Context, prefix string, events []historyEvent, roomDir string) {
	mediaDir := filepath.Join(roomDir, "media")

	mediaMsgTypes := map[string]bool{
		"m.image": true, "m.file": true, "m.video": true,
		"m.audio": true, "m.sticker": true,
	}

	downloaded, skipped, missing := 0, 0, 0

	for _, ev := range events {
		if ev.Type != "m.room.message" && ev.Type != "m.sticker" {
			continue
		}

		var c struct {
			MsgType string `json:"msgtype"`
			Body    string `json:"body"`
			URL     string `json:"url"`
			File    *struct {
				URL string `json:"url"`
				Key struct {
					K string `json:"k"`
				} `json:"key"`
				IV     string            `json:"iv"`
				Hashes map[string]string `json:"hashes"`
			} `json:"file"`
		}
		if json.Unmarshal(ev.Content, &c) != nil {
			continue
		}
		if ev.Type == "m.room.message" && !mediaMsgTypes[c.MsgType] {
			continue
		}

		mxcURL := c.URL
		encrypted := false
		if c.File != nil && c.File.URL != "" {
			mxcURL = c.File.URL
			encrypted = true
		}
		if mxcURL == "" || !strings.HasPrefix(mxcURL, "mxc://") {
			continue
		}

		rest := mxcURL[len("mxc://"):]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			continue
		}
		server, mediaID := rest[:slash], rest[slash+1:]
		if server == "" || mediaID == "" {
			continue
		}

		s3Key := prefix + "/media/" + server + "/" + mediaID
		data, err := s3GetBytes(ctx, s3Key)
		if err != nil {
			slog.Warn("Media fetch error", "mxc", mxcURL, "error", err)
			missing++
			continue
		}
		if data == nil {
			slog.Debug("Media not in backup", "mxc", mxcURL)
			missing++
			continue
		}

		// Verify SHA-256 hash before decrypting to detect corruption/tampering.
		if encrypted && c.File != nil {
			if sha, ok := c.File.Hashes["sha256"]; ok {
				sum := sha256.Sum256(data)
				if base64.RawStdEncoding.EncodeToString(sum[:]) != sha {
					slog.Warn("Media hash mismatch, skipping", "mxc", mxcURL)
					missing++
					continue
				}
			}

			// Key and IV may use URL-safe or standard base64, with or without
			// padding, depending on which client sent the message.
			keyBytes, err1 := tryDecodeBase64(c.File.Key.K)
			ivBytes, err2 := tryDecodeBase64(c.File.IV)
			if err1 != nil || err2 != nil || len(keyBytes) != 32 {
				slog.Warn("Bad key/IV for encrypted media", "mxc", mxcURL,
					"key_err", err1, "iv_err", err2, "key_len", len(keyBytes), "iv_len", len(ivBytes))
				missing++
				continue
			}
			// The spec stores the 64-bit random IV prefix; the full 128-bit
			// AES-CTR counter block is IV || zeros (counter starts at 0).
			// Some clients store all 16 bytes directly — accept both.
			if len(ivBytes) == 8 {
				full := make([]byte, 16)
				copy(full, ivBytes)
				ivBytes = full
			}
			if len(ivBytes) != 16 {
				slog.Warn("Bad IV length for encrypted media", "mxc", mxcURL, "iv_len", len(ivBytes))
				missing++
				continue
			}
			block, err := aes.NewCipher(keyBytes)
			if err != nil {
				slog.Warn("AES init failed", "error", err)
				missing++
				continue
			}
			plain := make([]byte, len(data))
			cipher.NewCTR(block, ivBytes).XORKeyStream(plain, data)
			data = plain
		}

		// Sanitise the filename: strip path-separator characters and control chars,
		// then verify the resolved path stays within mediaDir.
		safeName := strings.Map(func(r rune) rune {
			if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
				return '_'
			}
			return r
		}, c.Body)
		if safeName == "" {
			safeName = mediaID
		}
		outName := mediaID[:8] + "_" + safeName
		outPath := filepath.Join(mediaDir, outName)

		// Prevent path traversal: ensure the resolved path is under mediaDir.
		cleanMedia := filepath.Clean(mediaDir) + string(os.PathSeparator)
		if !strings.HasPrefix(filepath.Clean(outPath)+string(os.PathSeparator), cleanMedia) {
			slog.Warn("Skipping unsafe media path", "body", c.Body, "mxc", mxcURL)
			missing++
			continue
		}

		if _, err := os.Stat(outPath); err == nil {
			skipped++
			continue
		}
		if err := os.MkdirAll(mediaDir, 0700); err != nil {
			slog.Warn("Cannot create media dir", "error", err)
			return
		}
		if err := os.WriteFile(outPath, data, 0600); err != nil {
			slog.Warn("Cannot write media file", "path", outPath, "error", err)
			continue
		}
		downloaded++
	}

	slog.Info("Media download complete",
		"downloaded", downloaded,
		"skipped", skipped,
		"not_in_backup", missing,
		"dir", mediaDir,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	flag.Parse()

	if *flagIdentity == "" || *flagPrefix == "" {
		fmt.Fprintln(os.Stderr, "Usage: go run ./recovery/ -identity <age-key-file> -prefix <mtrnord|lexi> [-room <room-id>] [-all] [-out ./decrypted]")
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx := context.Background()

	secrets, err := parseSecretYAML(*flagSecret)
	if err != nil {
		slog.Error("Failed to parse secret.yaml", "path", *flagSecret, "error", err)
		os.Exit(1)
	}
	accessKey := secrets["s3_access_key"]
	secretKey := secrets["s3_secret_key"]
	if accessKey == "" || secretKey == "" {
		slog.Error("secret.yaml missing s3_access_key or s3_secret_key", "path", *flagSecret)
		os.Exit(1)
	}
	slog.Info("Loaded S3 credentials from secret.yaml")

	if err := loadIdentities(*flagIdentity); err != nil {
		slog.Error("Failed to load age identity", "error", err)
		os.Exit(1)
	}

	if err := initS3(ctx, *flagEndpoint, *flagRegion, *flagBucket, accessKey, secretKey); err != nil {
		slog.Error("Failed to init S3", "error", err)
		os.Exit(1)
	}

	rooms, err := listRooms(ctx, *flagPrefix)
	if err != nil {
		slog.Error("Failed to list rooms", "error", err)
		os.Exit(1)
	}

	roomAccountData := loadRoomAccountData(ctx, *flagPrefix)
	printAccountDataSummary(ctx, *flagPrefix)

	fmt.Printf("Rooms for prefix %q (%d total):\n\n", *flagPrefix, len(rooms))
	fmt.Printf("%-55s %-7s %-7s %s\n", "ROOM ID", "TYPE", "MEMBERS", "NAME")
	fmt.Printf("%s\n", strings.Repeat("-", 110))
	for _, r := range rooms {
		enc := ""
		if r.Encrypted {
			enc = " [E2EE]"
		}
		tag := roomTagLabel(r.RoomID, roomAccountData)
		fmt.Printf("%-55s %-7s %-7d %s%s%s\n", r.RoomID, r.Type, r.MemberCount, r.Name, enc, tag)
		if r.CanonicalAlias != "" {
			fmt.Printf("  alias: %s\n", r.CanonicalAlias)
		} else if len(r.Aliases) > 0 {
			fmt.Printf("  alias: %s\n", r.Aliases[0])
		}
		if len(r.ViaServers) > 0 {
			fmt.Printf("  via:   %s\n", strings.Join(r.ViaServers, ", "))
		}
	}
	fmt.Println()

	if !*flagAll && *flagRoom == "" {
		return
	}

	if err := os.MkdirAll(*flagOut, 0700); err != nil {
		slog.Error("Failed to create output dir", "error", err)
		os.Exit(1)
	}

	if *flagAll {
		for _, r := range rooms {
			if err := dumpHistory(ctx, *flagPrefix, r.RoomID, r.Name, *flagOut, roomAccountData); err != nil {
				slog.Warn("Failed to dump room", "room_id", r.RoomID, "error", err)
			}
		}
	} else {
		name := *flagRoom
		for _, r := range rooms {
			if r.RoomID == *flagRoom {
				name = r.Name
				break
			}
		}
		if err := dumpHistory(ctx, *flagPrefix, *flagRoom, name, *flagOut, roomAccountData); err != nil {
			slog.Error("Failed to dump room", "room_id", *flagRoom, "error", err)
			os.Exit(1)
		}
	}
}
