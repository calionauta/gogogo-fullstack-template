// SCOPE:layer=infra,removal=plugin — Loro CRDT + DocStore + sync workers + presence
package collab

import (
	"log/slog"

	"github.com/aholstenson/loro-go"
	natsio "github.com/nats-io/nats.go"
)

// Publisher pushes local Loro updates onto the JetStream subject
// app.sync.<docID> so the central SyncWorker (and other edges) converge.
// This is the desktop/edge side of the sync design (Phase C). The
// transport is whatever NATS the process already owns — a Leaf Node when
// NatsLeafNodeURL is set (replicates to central on reconnect), or a
// standalone embedded server for local-only realtime.
type Publisher struct {
	nc *natsio.Conn
}

// NewPublisher builds a publisher over an established NATS connection.
func NewPublisher(nc *natsio.Conn) *Publisher {
	return &Publisher{nc: nc}
}

// PublishUpdate exports d since the given version vector and publishes the
// update on app.sync.<docID>. since may be nil to publish a full delta from
// the doc's origin (common for small collaborative docs). On publish error
// it logs and returns the error so the caller can decide whether to retry.
func (p *Publisher) PublishUpdate(d *Doc, since *loro.VersionVector) error {
	update, err := d.EncodeUpdate(since)
	if err != nil {
		return err
	}
	subject := "app.sync." + d.ID()
	if pubErr := p.nc.Publish(subject, update); pubErr != nil {
		slog.Warn("collab: publish update failed", "doc", d.ID(), "error", pubErr)
		return pubErr
	}
	return nil
}
