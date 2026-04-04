// accountdata.go — backup of global account data, per-room account data, and
// the initial profile snapshot.  All three use a retry-on-next-run pattern:
// failures are logged as warnings so the next CronJob run retries automatically.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
)

// backupAccountData saves a snapshot of all global account-data events to S3.
// The strategy is overwrite-every-run so the file is always current; a failed
// write leaves the previous version in place and the next run retries.
//
// S3 keys:
//
//	{prefix}/account-data-latest.json.age
//	{prefix}/account-data-{dateStr}.json.age
func backupAccountData(ctx context.Context, client *mautrix.Client, syncResp *mautrix.RespSync, prefix string) {
	accountData := make(map[string]json.RawMessage)

	// Seed from the sync response (present on first sync and when changed).
	for _, ev := range syncResp.AccountData.Events {
		if ev.Content.VeryRaw != nil {
			accountData[ev.Type.Type] = ev.Content.VeryRaw
		}
	}

	// Always fetch these key types directly from the API so they're present even
	// on incremental syncs where account data hasn't changed.
	for _, t := range []string{"m.push_rules", "m.ignored_user_list", "m.direct"} {
		if _, ok := accountData[t]; !ok {
			var raw json.RawMessage
			if err := getAccountData(ctx, client, t, &raw); err == nil && raw != nil {
				accountData[t] = raw
			}
		}
	}

	if len(accountData) == 0 {
		slog.Info("No account data to save", "prefix", prefix)
		return
	}

	data, err := json.MarshalIndent(accountData, "", "  ")
	if err != nil {
		slog.Warn("Failed to marshal account data", "error", err)
		return
	}
	if err := s3PutAge(ctx, prefix+"/account-data-latest.json", data); err != nil {
		slog.Warn("Failed to save account data", "prefix", prefix, "error", err)
		return
	}
	if err := s3PutAge(ctx, prefix+"/account-data-"+dateStr+".json", data); err != nil {
		slog.Warn("Failed to save dated account data", "prefix", prefix, "error", err)
	}
	slog.Info("Account data saved", "prefix", prefix, "types", len(accountData))
}

// backupRoomAccountData saves per-room account-data (room tags, read markers,
// etc.) to S3 using a merge pattern so rooms absent from this sync retain their
// last-known values.
//
// S3 key:
//
//	{prefix}/room-account-data-latest.json.age
func backupRoomAccountData(ctx context.Context, client *mautrix.Client, syncResp *mautrix.RespSync, prefix string) {
	// Load existing data (merge pattern — rooms not in this sync are preserved).
	existing := make(map[string]map[string]json.RawMessage)
	if raw, err := getDecryptedAgeFromS3(ctx, prefix+"/room-account-data-latest.json.age"); err != nil {
		slog.Warn("Could not load previous room account data", "error", err)
	} else if raw != nil {
		if err := json.Unmarshal(raw, &existing); err != nil {
			slog.Warn("Previous room account data is corrupt, starting fresh", "error", err)
		}
	}

	for roomID, joinedRoom := range syncResp.Rooms.Join {
		roomData := existing[string(roomID)]
		if roomData == nil {
			roomData = make(map[string]json.RawMessage)
		}

		// Collect all account-data types present in the sync delta.
		for _, ev := range joinedRoom.AccountData.Events {
			if ev.Content.VeryRaw != nil {
				roomData[ev.Type.Type] = ev.Content.VeryRaw
			}
		}

		// Always fetch m.room.tag directly so it's present even without a delta.
		tagPath := "/_matrix/client/v3/user/" +
			url.PathEscape(client.UserID.String()) +
			"/rooms/" +
			url.PathEscape(string(roomID)) +
			"/tags"
		var tags json.RawMessage
		if err := matrixGetJSON(ctx, client, tagPath, &tags); err == nil && tags != nil {
			roomData[event.AccountDataRoomTags.Type] = tags
		}

		existing[string(roomID)] = roomData
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		slog.Warn("Failed to marshal room account data", "error", err)
		return
	}
	if err := s3PutAge(ctx, prefix+"/room-account-data-latest.json", data); err != nil {
		slog.Warn("Failed to save room account data", "prefix", prefix, "error", err)
		return
	}
	slog.Info("Room account data saved", "prefix", prefix, "rooms", len(existing))
}

// backupProfileSnapshot saves the user's current displayname and avatar_url to
// S3 on the first run.  Subsequent runs skip it (write-once semantics); if the
// write failed on a previous run the file is absent and this run will retry.
//
// S3 key:
//
//	{prefix}/profile-snapshot.json.age
func backupProfileSnapshot(ctx context.Context, client *mautrix.Client, prefix string) {
	snapshotKey := prefix + "/profile-snapshot.json.age"
	if s3Exists(ctx, snapshotKey) {
		return
	}
	var profile struct {
		Displayname string `json:"displayname"`
		AvatarURL   string `json:"avatar_url"`
	}
	path := "/_matrix/client/v3/profile/" + url.PathEscape(client.UserID.String())
	if err := matrixGetJSON(ctx, client, path, &profile); err != nil {
		slog.Warn("Failed to fetch profile snapshot", "error", err)
		return
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		slog.Warn("Failed to marshal profile snapshot", "error", err)
		return
	}
	// s3PutAge appends ".age" — writes to prefix+"/profile-snapshot.json.age".
	if err := s3PutAge(ctx, prefix+"/profile-snapshot.json", data); err != nil {
		slog.Warn("Failed to save profile snapshot", "prefix", prefix, "error", err)
		return
	}
	slog.Info("Profile snapshot saved", "prefix", prefix, "displayname", profile.Displayname)
}
