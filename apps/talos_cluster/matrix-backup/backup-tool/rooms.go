// rooms.go — room list management: name calculation, DM detection, S3 persistence.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"net/url"
)

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

// calcRoomName implements the Matrix room display-name algorithm:
// explicit name → canonical alias → heroes → "Empty Room".
func calcRoomName(
	stateEvents []*event.Event,
	heroes []id.UserID,
	joinedCount, invitedCount int,
	selfUserID id.UserID,
) string {
	for _, ev := range stateEvents {
		if ev.Type == event.StateRoomName {
			if c := ev.Content.AsRoomName(); c != nil && c.Name != "" {
				return c.Name
			}
		}
	}
	for _, ev := range stateEvents {
		if ev.Type == event.StateCanonicalAlias {
			if c := ev.Content.AsCanonicalAlias(); c != nil && c.Alias != "" {
				return string(c.Alias)
			}
		}
	}

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

	var filtered []id.UserID
	for _, h := range heroes {
		if h != selfUserID {
			filtered = append(filtered, h)
		}
	}

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

	others := joinedCount + invitedCount - 1
	if others < 0 {
		others = 0
	}
	if len(filtered) == 0 {
		if others == 0 {
			return "Empty Room"
		}
		return ""
	}

	names := make([]string, len(filtered))
	for i, h := range filtered {
		names[i] = heroName(h)
	}
	unnamed := others - len(names)
	if unnamed <= 0 {
		switch len(names) {
		case 1:
			return names[0]
		case 2:
			return names[0] + " and " + names[1]
		default:
			return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
		}
	}
	switch len(names) {
	case 1:
		return fmt.Sprintf("%s and %d others", names[0], unnamed)
	case 2:
		return fmt.Sprintf("%s, %s, and %d others", names[0], names[1], unnamed)
	default:
		return fmt.Sprintf("%s, and %d others", strings.Join(names, ", "), unnamed)
	}
}

// collectViaServers returns up to 3 server names for matrix.to ?via= links.
func collectViaServers(roomID id.RoomID, heroes []id.UserID, stateEvents []*event.Event) []string {
	seen := make(map[string]bool)
	var servers []string
	add := func(s string) bool {
		if s != "" && !seen[s] {
			seen[s] = true
			servers = append(servers, s)
		}
		return len(servers) >= 3
	}
	if idx := strings.LastIndex(string(roomID), ":"); idx >= 0 {
		add(string(roomID)[idx+1:])
	}
	for _, h := range heroes {
		_, server, err := h.Parse()
		if err == nil && add(server) {
			return servers
		}
	}
	for _, ev := range stateEvents {
		if len(servers) >= 3 {
			break
		}
		if ev.Type != event.StateMember || ev.StateKey == nil {
			continue
		}
		var mc struct {
			Membership string `json:"membership"`
		}
		if ev.Content.VeryRaw != nil {
			_ = json.Unmarshal(ev.Content.VeryRaw, &mc)
		}
		if mc.Membership != "join" {
			continue
		}
		_, server, err := id.UserID(*ev.StateKey).Parse()
		if err == nil {
			add(server)
		}
	}
	return servers
}

// fetchRoomNameFromState fetches the room name directly from the state API.
// Used when the sync delta doesn't include the name event.
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

// saveRoomList builds a JSON snapshot of all joined rooms and uploads it to S3.
// Rooms absent from this sync delta are preserved from the prior list.
func saveRoomList(ctx context.Context, client *mautrix.Client, syncResp *mautrix.RespSync, prefix string) error {
	existing := make(map[string]roomEntry)
	if raw, err := getDecryptedAgeFromS3(ctx, prefix+"/rooms-latest.json.age"); err != nil {
		slog.Warn("Could not load previous room list", "error", err)
	} else if raw != nil {
		var prev []roomEntry
		if err := json.Unmarshal(raw, &prev); err != nil {
			slog.Warn("Previous room list is corrupt, starting fresh", "error", err)
		} else {
			for _, r := range prev {
				// Retroactively reclassify 2-member non-space rooms stored as
				// "normal" before the member-count heuristic was introduced.
				if r.Type == "normal" && r.MemberCount > 0 && r.MemberCount <= 2 {
					r.Type = "dm"
				}
				existing[r.RoomID] = r
			}
		}
	}

	dmRooms := getDMRooms(ctx, client, syncResp)

	for roomID, joinedRoom := range syncResp.Rooms.Join {
		var name, canonicalAlias string
		var aliases []string
		encrypted := false
		rtype := "normal"
		if dmRooms[roomID] {
			rtype = "dm"
		}

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
		if rtype == "normal" && joinedCount > 0 && joinedCount <= 2 {
			rtype = "dm"
		}

		if name == "" {
			name = calcRoomName(joinedRoom.State.Events, joinedRoom.Summary.Heroes, joinedCount, invitedCount, client.UserID)
		}
		if name == "" {
			name = fetchRoomNameFromState(ctx, client, roomID)
		}
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
			ViaServers:     collectViaServers(roomID, joinedRoom.Summary.Heroes, joinedRoom.State.Events),
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
	if err := s3PutAge(ctx, prefix+"/rooms-"+dateStr+".json", data); err != nil {
		return err
	}
	if err := s3PutAge(ctx, prefix+"/rooms-latest.json", data); err != nil {
		return err
	}
	slog.Info("Uploaded room list", "rooms", len(rooms))
	return nil
}

// getDMRooms returns the full set of room IDs marked as direct chats.
// Always fetches from the account-data API so incremental syncs (which only
// carry changed account-data events) don't see a stale map.
func getDMRooms(ctx context.Context, client *mautrix.Client, syncResp *mautrix.RespSync) map[id.RoomID]bool {
	dmRooms := make(map[id.RoomID]bool)
	populate := func(direct event.DirectChatsEventContent) {
		for _, roomIDs := range direct {
			for _, rid := range roomIDs {
				dmRooms[rid] = true
			}
		}
	}
	var direct event.DirectChatsEventContent
	if err := getAccountData(ctx, client, "m.direct", &direct); err == nil {
		populate(direct)
		return dmRooms
	}
	// Fallback: use whatever the sync included.
	for _, ev := range syncResp.AccountData.Events {
		if ev.Type == event.AccountDataDirectChats {
			if err := json.Unmarshal(ev.Content.VeryRaw, &direct); err == nil {
				populate(direct)
			}
			break
		}
	}
	return dmRooms
}
