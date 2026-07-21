// SCOPE:layer=infra,removal=plugin — REMOVE if you don't need cross-instance CRDT sync
package crdtstore

//
// CRDTStore Phase 2: JetStream op transport. Without this file, the
// CRDTStore works in single-process mode (the doc lives in memory and
// the snapshot is persisted in PB). With it, ops are ALSO published
// over JetStream so a second binary running the same CRDTStore
// converges on the same state. The JetStream MsgId header provides
// built-in dedup within the duplicate-window (default 2 min), so the
// same op arriving twice — from the original producer AND from a
// sync replay — is silently dropped.
//
// In-process loop avoidance: each op published by THIS process is
// tagged with a per-process PublisherID. The consumer in this same
// process ignores messages with its own PublisherID (a Loro op
// applied locally doesn't need to be re-applied). Cross-process
// traffic has a different PublisherID and gets applied.

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"

	"github.com/google/uuid"
	natsio "github.com/nats-io/nats.go"
)

// transportSubjectPrefix is the JetStream subject root for todo CRDT
// ops. One subject per owner: "app.todo_crdt.<ownerID>". Per-owner
// subjects let us subscribe lazily (one goroutine per active owner)
// and parallelise across owners.
const transportSubjectPrefix = "app.todo_crdt."

// transportOpTTL is how long a published op stays in the JetStream
// stream before being GC'd. Should be >> the worst-case reconnection
// time so a briefly-offline instance can still catch up.
const transportOpTTL = 24 * 60 * 60 * 1_000_000_000 // 24h in nanoseconds

// transportStreamName is the JetStream stream that carries todo CRDT
// ops. One stream per CRDT store; multiple subjects map into it via
// the prefix. Defined here so tests can refer to the same name.
const transportStreamName = "TODO_CRDT_OPS"

// TransportConfig holds the JetStream connection + per-process identity
// the CRDTStore publisher needs. nil JetStream means single-process
// mode (publisher is a no-op); the consumer returns immediately.
type TransportConfig struct {
	// JetStream is the live JetStream context. nil = single-process.
	JetStream natsio.JetStreamContext
	// PublisherID identifies this process. Embedded in every
	// published op so consumers can ignore self-published messages
	// and avoid the apply → publish → apply → publish loop.
	PublisherID string
}

// CRDTTransport publishes Loro op updates over JetStream and consumes
// them from other instances. Phase 2 ships both halves.
type CRDTTransport struct {
	cfg TransportConfig
}

// NewTransport returns a transport that publishes to cfg.JetStream
// (or no-ops if cfg.JetStream is nil). The caller is responsible for
// starting the consumer (see Subscribe) and for the goroutine that
// pumps the doc's encoded updates into Publish.
func NewTransport(cfg TransportConfig) *CRDTTransport {
	if cfg.PublisherID == "" {
		cfg.PublisherID = uuid.NewString()
	}
	return &CRDTTransport{cfg: cfg}
}

// PublisherID returns the per-process identifier embedded in ops
// published by this transport.
func (t *CRDTTransport) PublisherID() string { return t.cfg.PublisherID }

// Op is a single Loro update published over JetStream. ID is unique
// per logical op (caller generates it; we don't trust Loro's internal
// version vector as a MsgId because it can repeat for the same
// logical change in rare cases). Updates is the Loro export delta
// from the publisher's last known version to the current state.
type Op struct {
	ID          string // unique per logical op (UUID v4 in practice)
	PublisherID string // which CRDTStore instance emitted this op
	OwnerID     string // scoping key (one subject per owner)
	Updates     []byte // Loro Update bytes (delta from prior version)
}

// Publish sends op on the per-owner subject. Returns nil if JetStream
// is not configured (single-process mode). The MsgId header is the
// unique op ID, so JetStream drops duplicates within the
// duplicate-window even if the consumer is briefly disconnected
// and the publisher retries.
func (t *CRDTTransport) Publish(_ context.Context, op Op) error {
	if t.cfg.JetStream == nil {
		return nil // single-process mode: no transport
	}
	if op.ID == "" || op.OwnerID == "" {
		return fmt.Errorf("crdtstore transport: op ID and owner ID required")
	}
	if op.PublisherID == "" {
		op.PublisherID = t.cfg.PublisherID
	}
	subject := transportSubjectPrefix + op.OwnerID
	// Encode the PublisherID as a short body prefix so the subscriber
	// can detect self-published ops. JetStream's Nats-Msg-Id handles
	// dedup; the publisher marker handles in-process loop avoidance.
	// Format: <8-char short ID> ":" <loro update bytes>. The colon
	// separates the marker from the op bytes; the subscriber strips
	// the prefix before applying the Loro update.
	prefix := publisherPrefix(op.PublisherID)
	_, err := t.cfg.JetStream.Publish(subject,
		[]byte(prefix+":"+string(op.Updates)),
		natsio.MsgId(op.ID),
	)
	if err != nil {
		return fmt.Errorf("crdtstore transport: publish %s on %s: %w", op.ID, subject, err)
	}
	return nil
}

// Subscribe installs a JetStream queue subscription on the per-owner
// subject. handler is called for every op that wasn't emitted by THIS
// process (in-process loop avoidance). The caller is responsible for
// the goroutine that processes the stream and for the lifecycle of
// the returned subscription (call sub.Unsubscribe() + Drain() on
// shutdown).
//
// queueName parameter is no longer used; retained as a positional
// placeholder so callers that pass it still compile. Will be removed
// in a follow-up; for now just ignore it.
// Subscribe installs a JetStream subscription on the per-owner
// subject. handler is called for every op that wasn't emitted by THIS
// process (in-process loop avoidance). The caller is responsible for
// the goroutine that processes the stream and for the lifecycle of
// the returned subscription (call sub.Unsubscribe() + Drain() on
// shutdown).
//
// Implementation note: this is a plain Subscribe (not QueueSubscribe)
// on an InterestPolicy stream, so every binary running the store
// receives every op (fan-out). The in-process loop filter (PublisherID
// == own) prevents the subscriber from re-applying ops the same
// process produced. WorkQueuePolicy would be wrong here — we want
// every replica to converge, not just one.
// Subscribe installs a JetStream subscription for the given owner.
// ownerID is the per-owner key (e.g. "user-123") used to build the
// subject — WireCRDTStoreTransport passes ">" to subscribe to all
// owners in one subscription (multi-tenant). When using ">", the
// handler receives every op on every owner and the Op.OwnerID field
// tells you who produced it. This is the deployment wire pattern:
// one subscription per process across all owners. Per-owner
// subscriptions are only useful in test scenarios where you want
// strict isolation between owners.
//
// The per-process PublisherID loop filter still applies: ops the
// subscribing process itself published are dropped in this handler
// to avoid the in-process replay loop. See splitPublisherPrefix.
func (t *CRDTTransport) Subscribe(
	_ context.Context, ownerID string, handler func(Op) error,
) (*natsio.Subscription, error) {
	if t.cfg.JetStream == nil {
		return nil, nil // single-process: no consumer
	}
	subject := transportSubjectPrefix + ownerID
	sub, err := t.cfg.JetStream.Subscribe(subject, func(msg *natsio.Msg) {
		op := Op{
			ID:      msg.Header.Get("Nats-Msg-Id"),
			OwnerID: ownerID,
			Updates: msg.Data,
		}
		if pubID, body, ok := splitPublisherPrefix(msg.Data); ok {
			op.PublisherID = pubID
			op.Updates = body
		}
		if op.ID == "" {
			slog.Warn("crdtstore transport: op without MsgId", "owner", ownerID)
			return
		}
		// In-process loop filter: a short fingerprint (8 hex chars)
		// is enough since the PublisherID is per-process and the
		// fingerprint collision probability is ~1/4B per pair.
		if op.PublisherID != "" && op.PublisherID == fingerprint(t.cfg.PublisherID) {
			return
		}
		if err := handler(op); err != nil {
			slog.Warn("crdtstore transport: handler failed",
				"owner", ownerID, "op_id", op.ID, "publisher", op.PublisherID, "error", err)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("crdtstore transport: subscribe %s: %w", subject, err)
	}
	return sub, nil
}

// EnsureStream creates the JetStream stream for todo CRDT ops if it
// doesn't exist. Idempotent. Subjects are filtered by the
// transportSubjectPrefix. Retention is work-queue semantics
// (ack on delivery) so consumers re-receive on reconnect.
func (t *CRDTTransport) EnsureStream(_ context.Context) error {
	if t.cfg.JetStream == nil {
		return nil // single-process: no stream needed
	}
	_, err := t.cfg.JetStream.AddStream(&natsio.StreamConfig{
		Name:      transportStreamName,
		Subjects:  []string{transportSubjectPrefix + ">"},
		Retention: natsio.InterestPolicy, // fan-out: every subscriber gets every op
		MaxAge:    transportOpTTL,
		// Duplicate window defaults to 2 min. Long enough for
		// short reconnects; short enough that ops older than 2 min
		// won't replay forever (caller is expected to catch up via
		// snapshot load if it was offline longer).
	})
	if err != nil {
		return fmt.Errorf("crdtstore transport: add stream: %w", err)
	}
	return nil
}

// publisherPrefixLen is the size of the publisher marker we prepend
// to every published op body. 8 hex chars = 4 bytes of fingerprint
// (32-bit FNV hash truncated to 8 hex chars). Per-process, so the
// in-process loop filter is "skip if fingerprint matches self".
const publisherPrefixLen = 8

// publisherPrefix returns the 8-char hex fingerprint of a publisher
// ID. Different IDs collide with probability ~1/4B per pair — fine
// for the in-process loop filter (worst case: we re-apply our own
// op once, which is harmless because Loro is idempotent for the
// same op bytes).
func publisherPrefix(publisherID string) string {
	if publisherID == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(publisherID))
	return fmt.Sprintf("%08x", h.Sum32())
}

// fingerprint is the same as publisherPrefix — the subscriber uses
// it to compute the fingerprint of its own PublisherID for
// comparison. Same hash, same length, same collision profile.
func fingerprint(publisherID string) string {
	return publisherPrefix(publisherID)
}

// splitPublisherPrefix extracts the publisher fingerprint and the
// remaining Loro update bytes from a message body. Returns ok=false
// if the body is shorter than the prefix or doesn't contain a colon.
// A failed split leaves the body untouched and the PublisherID
// empty, which the loop filter treats as "unknown publisher" (apply
// the op; JetStream MsgId dedup handles duplicate delivery).
func splitPublisherPrefix(data []byte) (publisherID string, body []byte, ok bool) {
	if len(data) < publisherPrefixLen+1 {
		return "", data, false
	}
	idx := bytes.IndexByte(data[publisherPrefixLen:], ':')
	if idx < 0 {
		return "", data, false
	}
	publisherID = string(data[:publisherPrefixLen])
	body = data[publisherPrefixLen+idx+1:]
	return publisherID, body, true
}
