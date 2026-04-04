// config.go — global configuration, account definitions, and age key initialisation.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"filippo.io/age"
	"maunium.net/go/mautrix/id"
)

// ourServerName is used to decide whose media and avatars to download.
// Override with S3_SERVER_NAME if needed.
var ourServerName = envOrDefault("SERVER_NAME", "mtrnord.blog")

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
	s3Region      = envOrDefault("S3_REGION", "hel1")
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
	// this tool (loaded from AGE_PRIVATE_KEY). Allows reading back previously
	// encrypted objects without storing plain copies in S3.
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

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// initAgeRecipients parses a comma-separated list of age public keys.
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

// initAgeIdentity parses an AGE-SECRET-KEY-1… line for decrypting previously
// written .age objects (e.g. the room list, session token).
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
