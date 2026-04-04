// session.go — Matrix session persistence (login, save, restore).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

type sessionData struct {
	AccessToken string      `json:"access_token"`
	DeviceID    id.DeviceID `json:"device_id"`
}

// ensureSession loads a saved session from S3 or logs in with the account
// password and persists the new session for future runs.
//
// Sessions are stored age-encrypted under s3Key+".age". Plain-JSON sessions
// written by older tool versions are transparently read as a fallback so
// existing deployments migrate automatically on the next run.
func ensureSession(ctx context.Context, client *mautrix.Client, acc accountCfg, s3Key string) (*sessionData, error) {
	var sess sessionData

	// Try age-encrypted first (current format).
	if raw, err := getDecryptedAgeFromS3(ctx, s3Key+".age"); err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	} else if raw != nil {
		if err := json.Unmarshal(raw, &sess); err != nil {
			return nil, fmt.Errorf("parse session: %w", err)
		}
	} else {
		// Fallback: plain JSON written by older versions.
		if err := s3GetJSON(ctx, s3Key, &sess); err != nil {
			return nil, fmt.Errorf("load session (legacy): %w", err)
		}
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
	// Write age-encrypted; appends ".age" to s3Key.
	if err := s3PutAge(ctx, s3Key, data); err != nil {
		return nil, fmt.Errorf("save session: %w", err)
	}
	return &sess, nil
}
