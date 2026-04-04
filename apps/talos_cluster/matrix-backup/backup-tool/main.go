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
	"context"
	"log/slog"
	"os"
)

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
