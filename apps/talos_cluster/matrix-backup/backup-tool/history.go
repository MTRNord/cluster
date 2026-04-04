// history.go — room history pagination: forward incremental sync and DM
// backward backfill.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// paginateRoom fetches room history and writes new events to a per-run
// age-encrypted JSONL file in S3. For DMs on first run the full history is
// fetched backwards from the current position before normal forward pagination.
//
// syncNextBatch is the nextBatch token from the current sync response. On first
// run it is saved as the forward cursor so the next run only sees events that
// arrive after this sync — avoiding re-fetching events already in the sync.
func paginateRoom(
	ctx context.Context,
	client *mautrix.Client,
	roomID id.RoomID,
	prefix string,
	isDM bool,
	prevBatch string,
	syncNextBatch string,
	sessions megolmSessions,
) error {
	safeKey := roomSafeKey(roomID)
	cursorKey := prefix + "/history-cursor/" + safeKey + ".json"
	dmHistoryDoneKey := prefix + "/history-cursor/" + safeKey + ".dm-done"

	var cursor struct {
		Token string `json:"token"`
	}
	if err := s3GetJSON(ctx, cursorKey, &cursor); err != nil {
		return err
	}

	// fetchDMHistory does a full backward walk from fromToken and stores results.
	// Used on first run for DMs and as catch-up when a room was initially missed.
	fetchDMHistory := func(fromToken string) {
		slog.Info("DM: fetching full history", "room_id", roomID, "from_token", fromToken)
		var messages []historyEvent
		token := fromToken
		for {
			resp, err := client.Messages(ctx, roomID, token, "", mautrix.DirectionBackward, nil, 100)
			if err != nil {
				slog.Warn("room_messages error (backward)", "room_id", roomID, "token", token, "error", err)
				break
			}
			slog.Info("DM history page", "room_id", roomID, "chunk_size", len(resp.Chunk), "end", resp.End)
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
			// Reverse so events are stored oldest-first.
			for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
				messages[i], messages[j] = messages[j], messages[i]
			}
			histKey := prefix + "/history/" + safeKey + "/" + runStr + "-backfill.jsonl"
			var buf bytes.Buffer
			for _, m := range messages {
				line, err := json.Marshal(m)
				if err != nil {
					slog.Warn("Failed to marshal event, skipping", "event_id", m.EventID, "error", err)
					continue
				}
				buf.Write(line)
				buf.WriteByte('\n')
			}
			if err := s3PutAge(ctx, histKey, buf.Bytes()); err != nil {
				slog.Warn("Failed to upload DM history", "room_id", roomID, "error", err)
			} else {
				slog.Info("DM history stored", "room_id", roomID, "events", len(messages))
				if err := s3Put(ctx, dmHistoryDoneKey, []byte("done"), "text/plain"); err != nil {
					slog.Warn("Failed to write dm-done marker", "room_id", roomID, "error", err)
				}
			}
		} else {
			// Nothing to backfill — mark done so we don't retry every run.
			slog.Info("DM history: no messages found, marking done", "room_id", roomID)
			if err := s3Put(ctx, dmHistoryDoneKey, []byte("done"), "text/plain"); err != nil {
				slog.Warn("Failed to write dm-done marker", "room_id", roomID, "error", err)
			}
		}
	}

	if cursor.Token == "" {
		// First time we've seen this room. For DMs, walk all the way back through
		// history so we have a complete record from day one.
		if isDM && prevBatch != "" {
			fetchDMHistory(prevBatch)
		}
		// Save the sync's nextBatch as the forward cursor so the next run only
		// picks up genuinely new events.
		if syncNextBatch == "" {
			syncNextBatch = prevBatch
		}
		if syncNextBatch != "" {
			cursorJSON, err := json.Marshal(map[string]string{"token": syncNextBatch})
			if err == nil {
				if err := s3Put(ctx, cursorKey, cursorJSON, "application/json"); err != nil {
					slog.Warn("Failed to save cursor (first run)", "room_id", roomID, "error", err)
				}
			}
		}
		return nil
	}

	// Incremental run: if this is a DM but we never completed a backward history
	// fetch (e.g. room was missed because it wasn't in m.direct on first run),
	// do it now using the saved cursor as the backward starting point.
	// Note: prevBatch may be empty for rooms with no new events in a full_state
	// sync, so we use cursor.Token directly rather than gating on prevBatch.
	if isDM {
		dmDoneData, _ := s3Get(ctx, dmHistoryDoneKey)
		if dmDoneData == nil {
			slog.Info("DM catch-up: no history marker found, backfilling", "room_id", roomID)
			fetchDMHistory(cursor.Token)
		}
	}

	// Incremental forward pagination from the saved cursor.
	var messages []historyEvent
	nextToken := cursor.Token
	lastGoodToken := cursor.Token
	histKey := prefix + "/history/" + safeKey + "/" + runStr + ".jsonl"

	const maxPages = 100
	pages := 0
	for range maxPages {
		pages++
		resp, err := client.Messages(ctx, roomID, nextToken, "", mautrix.DirectionForward, nil, 100)
		if err != nil {
			slog.Warn("room_messages error (forward)", "room_id", roomID, "error", err)
			break
		}
		if len(resp.Chunk) == 0 {
			break
		}
		for _, ev := range resp.Chunk {
			wasEncrypted := ev.Type == event.EventEncrypted
			ev = tryDecryptEvent(ev, sessions)
			messages = append(messages, eventToRecord(ev, wasEncrypted))
			processEvent(ctx, client, ev, roomID, prefix, isDM)
		}
		if resp.End == "" || resp.End == nextToken {
			lastGoodToken = nextToken
			nextToken = ""
			break
		}
		lastGoodToken = resp.End
		nextToken = resp.End
	}
	if pages == maxPages && nextToken != "" {
		slog.Warn("Pagination cap reached — room has more events; next run will continue",
			"room_id", roomID, "pages", maxPages)
		lastGoodToken = nextToken
	}

	if len(messages) > 0 {
		var buf bytes.Buffer
		for _, m := range messages {
			line, err := json.Marshal(m)
			if err != nil {
				slog.Warn("Failed to marshal event, skipping", "event_id", m.EventID, "error", err)
				continue
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		if err := s3PutAge(ctx, histKey, buf.Bytes()); err != nil {
			slog.Warn("Failed to upload history", "room_id", roomID, "error", err)
		}
		slog.Info("History updated", "room_id", roomID, "new_events", len(messages))
	}

	// Advance the cursor so the next run never re-processes stored events.
	if lastGoodToken != cursor.Token {
		cursorJSON, err := json.Marshal(map[string]string{"token": lastGoodToken})
		if err != nil {
			return fmt.Errorf("marshal cursor: %w", err)
		}
		if err := s3Put(ctx, cursorKey, cursorJSON, "application/json"); err != nil {
			slog.Warn("Failed to advance cursor", "room_id", roomID, "error", err)
		}
	}
	return nil
}
