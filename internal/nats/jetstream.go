// SCOPE:layer=infra,removal=plugin — NATS JetStream + Leaf Node + CRUD proxy
package nats

import (
	"time"

	"github.com/nats-io/nats.go"
)

// EnsureStream creates a stream if it doesn't exist.
func EnsureStream(name string, subjects []string, maxAge ...time.Duration) error {
	cfg := &nats.StreamConfig{
		Name:      name,
		Subjects:  subjects,
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
	}
	if len(maxAge) > 0 {
		cfg.MaxAge = maxAge[0]
	}
	_, err := JS.AddStream(cfg)
	if err == nil {
		return nil
	}
	// Stream already exists is not an error
	return err
}

// EnsureKeyValue creates a KV bucket if it doesn't exist.
func EnsureKeyValue(bucket string, maxValueSize ...int32) (nats.KeyValue, error) {
	cfg := &nats.KeyValueConfig{
		Bucket:  bucket,
		Storage: nats.FileStorage,
	}
	if len(maxValueSize) > 0 {
		cfg.MaxValueSize = maxValueSize[0]
	}
	kv, err := JS.CreateKeyValue(cfg)
	if err != nil {
		return nil, err
	}
	return kv, nil
}

// PublishEvent publishes an event to a room stream.
func PublishEvent(roomID, eventType string, data []byte) error {
	_, err := JS.Publish("room."+roomID+"."+eventType, data)
	return err
}

// SubscribeRoom subscribes to all events for a room.
func SubscribeRoom(roomID string, handler func(msg *nats.Msg)) (*nats.Subscription, error) {
	return JS.Subscribe("room."+roomID+".>", handler)
}
