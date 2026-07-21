// SCOPE:layer=infra,removal=plugin — NATS JetStream + Leaf Node + CRUD proxy
package nats

import (
	"encoding/json"
	"log/slog"
	"time"
)

type UserInfo struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"` // "active", "idle", "away"
	JoinedAt time.Time `json:"joinedAt"`
}

// UserJoin broadcasts a join event and persists to KV.
func UserJoin(roomID string, info UserInfo) error {
	kv, err := EnsureKeyValue("room-presence")
	if err != nil {
		return err
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	key := "room." + roomID + ".user." + info.ID
	if _, putErr := kv.Put(key, data); putErr != nil {
		slog.Warn("nats/presence: kv.Put failed", "room", roomID, "user", info.ID, "error", putErr)
	}

	event, err := json.Marshal(map[string]string{
		"type": "join", "roomID": roomID, "userID": info.ID,
	})
	if err != nil {
		return err
	}
	subj := "presence." + roomID + ".join"
	if err := NC.Publish(subj, event); err != nil {
		slog.Warn("nats/presence: publish join failed", "room", roomID, "error", err)
	}
	return nil
}

// UserLeave removes user from KV and broadcasts leave event.
func UserLeave(roomID, userID string) error {
	kv, err := EnsureKeyValue("room-presence")
	if err != nil {
		return err
	}
	if delErr := kv.Delete("room." + roomID + ".user." + userID); delErr != nil {
		slog.Warn("nats/presence: kv.Delete failed", "room", roomID, "user", userID, "error", delErr)
	}

	event, marshalErr := json.Marshal(map[string]string{
		"type": "leave", "roomID": roomID, "userID": userID,
	})
	if marshalErr != nil {
		return marshalErr
	}
	if pubErr := NC.Publish("presence."+roomID+".leave", event); pubErr != nil {
		slog.Warn("nats/presence: publish leave failed", "room", roomID, "error", pubErr)
	}
	return nil
}

// GetRoomUsers returns all active users in a room from KV.
func GetRoomUsers(roomID string) ([]UserInfo, error) {
	kv, err := EnsureKeyValue("room-presence")
	if err != nil {
		return nil, err
	}
	keys, err := kv.Keys()
	if err != nil {
		return nil, err
	}
	var users []UserInfo
	for _, key := range keys {
		entry, err := kv.Get(key)
		if err != nil {
			continue
		}
		var info UserInfo
		if err := json.Unmarshal(entry.Value(), &info); err != nil {
			continue
		}
		users = append(users, info)
	}
	return users, nil
}
