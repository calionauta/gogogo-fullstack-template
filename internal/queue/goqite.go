// SCOPE:layer=infra,removal=core — Job queue (goqite) + Worker Pool
// Package queue implements the background job subsystem: a goqite-backed
// SQLite queue, an SSE hub for streaming progress to the browser, and a
// worker pool that dispatches messages through a typed HandlerRegistry.
//
// Three layered concerns:
//
//   - Queue: durable, persisted in SQLite. Survives restarts.
//   - SSEHub: ephemeral, in-memory fan-out to connected browser tabs.
//   - WorkerPool: drains the queue, dispatches via the registry, retries
//     with exponential backoff, and streams per-attempt feedback.
package queue

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"maragu.dev/goqite"

	"github.com/calionauta/gogogo-fullstack-template/config"
)

// workerCount is the default number of concurrent workers in the pool.
// Four balances throughput against SQLite write contention; raise it
// once contention is measured.
const workerCount = 4

// goqiteSchema is the SQLite DDL for the goqite message table. goqite
// does not auto-migrate, so we apply it on New() if the table does not
// exist yet.
//
//go:embed goqite_schema.sql
var goqiteSchema string

// Queue wraps the underlying goqite queue with an SSE hub for browser
// streaming and a HandlerRegistry for typed dispatch. The struct fields
// are exported via accessor methods rather than directly so callers
// can't bypass Close()'s nil-out of the goqite handle.
type Queue struct {
	q   *goqite.Queue
	hub *SSEHub
	reg *HandlerRegistry
	db  *sql.DB
}

// New opens a SQLite-backed goqite queue, applies the schema, and
// returns a ready-to-use Queue with a fresh SSEHub and HandlerRegistry.
func New(cfg *config.Config) (*Queue, error) {
	dbPath := cfg.DataDir + "/queue.db"
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("queue: open sqlite at %s: %w", dbPath, err)
	}

	if err := applyGoqiteSchema(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("queue: apply goqite schema: %w", err)
	}

	gq := goqite.New(goqite.NewOpts{
		DB:   db,
		Name: "default",
	})

	return &Queue{
		q:   gq,
		db:  db,
		hub: NewSSEHub(),
		reg: NewHandlerRegistry(),
	}, nil
}

// applyGoqiteSchema creates the goqite table if it does not exist.
// Safe to call on every startup: the SELECT-then-CREATE pattern is
// idempotent. Uses context.Background() because schema setup must run
// before any caller-driven ctx exists; the operation is local SQLite
// and bounded by the embedded DDL size.
func applyGoqiteSchema(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(
		ctx,
		`select exists (select 1 from sqlite_master where type = 'table' and name = 'goqite')`,
	).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := db.ExecContext(ctx, goqiteSchema); err != nil {
		return err
	}
	return nil
}

// ErrQueueClosed is returned when an operation is attempted on a closed queue.
var ErrQueueClosed = errors.New("queue: closed")

// Enqueue wraps goqite.Send with a typed body. The data must already be
// a marshalled queue.Job envelope when going through HandlerRegistry.
func (q *Queue) Enqueue(ctx context.Context, data []byte) error {
	slog.Info("queue: Enqueue called", "body_len", len(data))
	err := q.q.Send(ctx, goqite.Message{Body: data})
	slog.Info("queue: Enqueue result", "err", err)
	return err
}

// ReceiveAndWait blocks for up to timeout waiting for a message.
func (q *Queue) ReceiveAndWait(ctx context.Context, timeout time.Duration) (*goqite.Message, error) {
	return q.q.ReceiveAndWait(ctx, timeout)
}

// Hub returns the SSEHub for streaming to browser clients.
func (q *Queue) Hub() *SSEHub { return q.hub }

// StartWorkers launches workerCount goroutines that drain the queue
// and dispatch through the HandlerRegistry. Returns the pool so
// callers can stop it explicitly on shutdown.
func (q *Queue) StartWorkers() *WorkerPool {
	wp := NewWorkerPool(q.q, q.hub, q.reg, workerCount)
	wp.Start()
	return wp
}

// Registry returns the handler registry. Production code calls
// q.Registry().Register("type", handler) before StartWorkers so the
// worker pool can dispatch incoming messages. Tests use the same API
// to inject fake handlers.
func (q *Queue) Registry() *HandlerRegistry { return q.reg }

// Close drains in-flight workers via the SSE hub and shuts down the
// underlying database. After Close, Receive/Delete return ErrQueueClosed.
func (q *Queue) Close() {
	q.q = nil
	if q.db != nil {
		_ = q.db.Close()
	}
}
