// backup.go — per-account backup orchestration.
package main

import (
	"context"
	"encoding/json"
	"log/slog"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// backupAccount runs the full backup pipeline for a single Matrix account:
// restore crypto state, sync, export E2EE keys, save room list, paginate
// history, save account data, then persist state back to S3.
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
		return err
	}

	if _, err := ensureSession(ctx, client, acc, sessionS3Key); err != nil {
		return err
	}

	var syncToken struct {
		NextBatch string `json:"next_batch"`
	}
	_ = s3GetJSON(ctx, syncTokenS3Key, &syncToken)

	slog.Info("Syncing", "since", syncToken.NextBatch)
	syncResp, err := client.SyncRequest(ctx, 60000, syncToken.NextBatch, "", true, event.PresenceUnavailable)
	if err != nil {
		return err
	}
	if data, err := json.Marshal(map[string]string{"next_batch": syncResp.NextBatch}); err == nil {
		if err := s3Put(ctx, syncTokenS3Key, data, "application/json"); err != nil {
			slog.Warn("Failed to save sync token", "error", err)
		}
	}
	slog.Info("Sync done", "rooms", len(syncResp.Rooms.Join))

	var sessions megolmSessions
	if acc.SSSSKey != "" {
		sessions, err = fetchAndExportKeyBackup(ctx, client, acc.SSSSKey, acc.Prefix)
		if err != nil {
			slog.Warn("Key backup export failed", "error", err)
		}
	}

	if err := saveRoomList(ctx, client, syncResp, acc.Prefix); err != nil {
		slog.Warn("Room list failed", "error", err)
	}

	// Back up account-data, per-room account data, and profile snapshot.
	backupAccountData(ctx, client, syncResp, acc.Prefix)
	backupRoomAccountData(ctx, client, syncResp, acc.Prefix)
	backupProfileSnapshot(ctx, client, acc.Prefix)

	// Build the DM room set: m.direct API + 2-member heuristic from stored list.
	dmRooms := getDMRooms(ctx, client, syncResp)
	if raw, err := getDecryptedAgeFromS3(ctx, acc.Prefix+"/rooms-latest.json.age"); err == nil && raw != nil {
		var storedRooms []roomEntry
		if json.Unmarshal(raw, &storedRooms) == nil {
			for _, r := range storedRooms {
				if r.Type == "dm" {
					dmRooms[id.RoomID(r.RoomID)] = true
				}
			}
		}
	}

	// Paginate history for all rooms present in the sync delta.
	for roomID, joinedRoom := range syncResp.Rooms.Join {
		isDM := dmRooms[roomID]
		if err := paginateRoom(ctx, client, roomID, acc.Prefix, isDM, joinedRoom.Timeline.PrevBatch, syncResp.NextBatch, sessions); err != nil {
			slog.Warn("History error", "room_id", roomID, "error", err)
		}
	}

	// Catch-up: DM rooms absent from this sync delta (no new events) may still
	// need their history backfilled. Use the stored rooms list as the authoritative
	// DM source, falling back to the API-based dmRooms map.
	catchupDMs := make(map[id.RoomID]bool)
	for roomID := range dmRooms {
		catchupDMs[roomID] = true
	}
	if raw, err := getDecryptedAgeFromS3(ctx, acc.Prefix+"/rooms-latest.json.age"); err != nil {
		slog.Warn("DM catch-up: failed to read rooms list", "error", err)
	} else if raw == nil {
		slog.Warn("DM catch-up: rooms list not found or ageIdentity not set")
	} else {
		var storedRooms []roomEntry
		if err := json.Unmarshal(raw, &storedRooms); err != nil {
			slog.Warn("DM catch-up: failed to parse rooms list", "error", err)
		} else {
			slog.Info("DM catch-up: loaded rooms list", "total_rooms", len(storedRooms))
			for _, r := range storedRooms {
				if r.Type == "dm" {
					catchupDMs[id.RoomID(r.RoomID)] = true
				}
			}
		}
	}
	slog.Info("DM catch-up: checking rooms", "total_dm_rooms", len(catchupDMs), "in_sync", len(syncResp.Rooms.Join))
	for roomID := range catchupDMs {
		if _, inSync := syncResp.Rooms.Join[roomID]; inSync {
			continue // already handled above
		}
		safeKey := roomSafeKey(roomID)
		dmHistoryDoneKey := acc.Prefix + "/history-cursor/" + safeKey + ".dm-done"
		dmDone, dmDoneErr := s3Get(ctx, dmHistoryDoneKey)
		if dmDoneErr != nil {
			slog.Warn("DM catch-up: error checking dm-done marker", "room_id", roomID, "error", dmDoneErr)
		}
		if dmDone != nil {
			continue // already backfilled
		}
		cursorKey := acc.Prefix + "/history-cursor/" + safeKey + ".json"
		var cursor struct {
			Token string `json:"token"`
		}
		if err := s3GetJSON(ctx, cursorKey, &cursor); err != nil {
			slog.Warn("DM catch-up: error reading cursor", "room_id", roomID, "error", err)
			continue
		}
		if cursor.Token == "" {
			slog.Info("DM catch-up: no cursor yet, skipping", "room_id", roomID)
			continue
		}
		slog.Info("DM catch-up: backfilling room", "room_id", roomID, "cursor", cursor.Token)
		if err := paginateRoom(ctx, client, roomID, acc.Prefix, true, cursor.Token, syncResp.NextBatch, sessions); err != nil {
			slog.Warn("DM catch-up error", "room_id", roomID, "error", err)
		}
	}

	if err := uploadStore(ctx, acc.StoreDir, storeS3Key); err != nil {
		slog.Warn("Could not upload store", "error", err)
	}

	slog.Info("Done", "user_id", acc.UserID)
	return nil
}
