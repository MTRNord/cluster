// events.go — per-event processing: media download, profile-change recording,
// and the historyEvent record type shared by both pagination paths.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// historyEvent is the on-disk representation of a single room event stored
// in JSONL files under prefix/history/<safeKey>/<run>.jsonl.age.
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

// roomSafeKey converts a room ID into a string safe for use as an S3 key
// component by replacing '/' and ':' with '_'.
func roomSafeKey(roomID id.RoomID) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(string(roomID))
}

// processEvent downloads media and records profile changes for a single event.
// The event must already be decrypted before this is called.
func processEvent(ctx context.Context, client *mautrix.Client, ev *event.Event, roomID id.RoomID, prefix string, isDM bool) {
	if ev.Type == event.EventEncrypted {
		return
	}

	if ev.Type == event.EventMessage {
		var content struct {
			MsgType string          `json:"msgtype"`
			URL     string          `json:"url"`
			File    json.RawMessage `json:"file"`
		}
		if err := json.Unmarshal(ev.Content.VeryRaw, &content); err == nil {
			mediaURL := content.URL
			if mediaURL == "" && content.File != nil {
				var f struct {
					URL string `json:"url"`
				}
				if json.Unmarshal(content.File, &f) == nil {
					mediaURL = f.URL
				}
			}
			if mediaMsgTypes[content.MsgType] && mediaURL != "" {
				if isDM || strings.HasSuffix(string(ev.Sender), ":"+ourServerName) {
					if err := downloadAndStoreMedia(ctx, client, mediaURL, prefix, "media"); err != nil {
						slog.Warn("Media download failed", "url", mediaURL, "error", err)
					}
				}
			}
		}
	}

	if ev.Type == event.EventSticker {
		var content struct {
			URL  string          `json:"url"`
			File json.RawMessage `json:"file"`
		}
		if err := json.Unmarshal(ev.Content.VeryRaw, &content); err == nil {
			mediaURL := content.URL
			if mediaURL == "" && content.File != nil {
				var f struct {
					URL string `json:"url"`
				}
				if json.Unmarshal(content.File, &f) == nil {
					mediaURL = f.URL
				}
			}
			if mediaURL != "" {
				if isDM || strings.HasSuffix(string(ev.Sender), ":"+ourServerName) {
					if err := downloadAndStoreMedia(ctx, client, mediaURL, prefix, "media"); err != nil {
						slog.Warn("Sticker download failed", "url", mediaURL, "error", err)
					}
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

// recordProfileUpdate appends a JSONL entry to S3 when a member event shows a
// display-name or avatar change for a local user; also downloads the new avatar.
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
