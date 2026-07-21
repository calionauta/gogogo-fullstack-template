// SCOPE:layer=infra,removal=core — Queue handlers + retry + workers
package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"maragu.dev/goqite"
)

// WorkerPool drains the underlying goqite queue, dispatches each
// message to a registered Handler (looked up via the HandlerRegistry),
// wraps the handler invocation in RetryConfig.Do so transient failures
// stream SSE feedback to the user, and deletes the message after the
// handler returns (successfully or after exhausting retries).
//
// This is the production path for SSE-aware async work: handlers can
// stream progress chunks via hub.Send/Broadcast, retry policy is
// centralized in RetryConfig, and the worker pool never blocks longer
// than MaxDelay between attempts.
type WorkerPool struct {
	q        *goqite.Queue
	qMu      sync.RWMutex // protects q against concurrent Close()
	hub      *SSEHub
	reg      *HandlerRegistry
	retry    *RetryConfig
	count    int
	stopCh   chan struct{}
	wg       sync.WaitGroup
	ctx      context.Context //nolint:containedctx // lifecycle context for the pool, not request-scoped
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// NewWorkerPool wires a worker pool with default retry settings
// (DefaultRetryConfig: 3 attempts, 2s→30s exponential backoff, ±20% jitter).
// Call SetRetry to override.
func NewWorkerPool(q *goqite.Queue, hub *SSEHub, reg *HandlerRegistry, count int) *WorkerPool {
	return &WorkerPool{
		q:      q,
		hub:    hub,
		reg:    reg,
		retry:  &DefaultRetryConfig,
		count:  count,
		stopCh: make(chan struct{}),
	}
}

// SetRetry overrides the default retry config. Must be called before Start.
func (wp *WorkerPool) SetRetry(cfg RetryConfig) {
	wp.retry = &cfg
}

// Start launches count workers. Each worker is a goroutine that loops:
//  1. Receive a message from the queue (with backoff on empty).
//  2. Decode the Job envelope.
//  3. Look up the handler in the registry.
//  4. Invoke the handler under retry.Do with SSE feedback.
//  5. Delete the message from the queue.
//
// Start creates a cancelable context so Stop() can interrupt an idle
// worker blocked inside ReceiveAndWait (which polls forever on a
// Background context) and let the pool shut down cleanly.
func (wp *WorkerPool) Start() {
	wp.ctx, wp.cancel = context.WithCancel(context.Background())
	for i := range wp.count {
		wp.wg.Add(1)
		go wp.worker(i)
	}
	slog.Info("queue workers started", "count", wp.count, "retry_attempts", wp.retry.Attempts)
}

// Stop drains in-flight workers (up to RetryConfig.MaxDelay between
// attempts) and blocks until all goroutines have exited. It is
// idempotent and safe to call before Start or after a prior Stop.
func (wp *WorkerPool) Stop() {
	wp.stopOnce.Do(func() {
		if wp.cancel != nil {
			wp.cancel() // unblock ReceiveAndWait on idle workers
		}
		close(wp.stopCh)
	})
	wp.wg.Wait()
	slog.Info("queue workers stopped")
}

//nolint:gocyclo // worker drain/stop loop; extracted helpers would obscure the deadlock fix.
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()

	for {
		select {
		case <-wp.stopCh:
			return
		default:
		}

		q := wp.qGuard()
		if q == nil {
			return // Queue was closed; stop draining.
		}

		msg, err := q.ReceiveAndWait(wp.ctx, time.Second)
		slog.Info("queue worker: received", "worker_id", id, "has_msg", msg != nil, "err", err)
		if err != nil {
			select {
			case <-wp.stopCh:
				return
			case <-wp.ctx.Done():
				return // context cancelled (Stop) — no need to keep polling
			default:
				slog.Warn("queue worker: receive error", "worker_id", id, "error", err)
				time.Sleep(time.Second)
				continue
			}
		}
		if msg == nil {
			select {
			case <-wp.stopCh:
				return
			case <-wp.ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
				continue
			}
		}

		wp.processMessage(context.Background(), msg)

		if q := wp.qGuard(); q != nil {
			if err := q.Delete(context.Background(), msg.ID); err != nil {
				slog.Warn("queue worker: delete error", "worker_id", id, "error", err)
			}
		}
	}
}

// processMessage decodes the Job envelope, looks up the handler, and
// invokes it under RetryConfig.Do. The retry layer streams SSE feedback
// ({"type":"retry","attempt":N,"status":"attempt|success"}) between
// attempts so the user sees the retry happening instead of waiting in
// silence.
func (wp *WorkerPool) processMessage(ctx context.Context, msg *goqite.Message) {
	if msg == nil {
		return
	}
	slog.Info("queue worker: processMessage", "type", jobTypeOf(msg.Body))
	job, err := DecodeJob(msg.Body)
	if err != nil {
		// Decode failures are non-retryable: bad bytes will never parse.
		// Log loudly and drop the message rather than spinning forever.
		slog.Error("queue worker: decode failed",
			"worker_id", -1, "error", err, "body", string(msg.Body))
		return
	}

	handler := wp.reg.Lookup(job.Type)
	operation := job.Type
	clientID := job.ClientID

	err = wp.retry.Do(ctx, wp.hub, clientID, operation, func() error {
		return handler(ctx, wp.hub, job)
	})
	if err != nil {
		slog.Error("queue worker: handler failed after retries",
			"worker_id", -1, "type", job.Type, "client_id", clientID, "error", err)
	}
}

func jobTypeOf(body []byte) string {
	var j struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return ""
	}
	return j.Type
}

// qGuard returns the underlying goqite queue or nil if it has been closed.
func (wp *WorkerPool) qGuard() *goqite.Queue {
	wp.qMu.RLock()
	defer wp.qMu.RUnlock()
	return wp.q
}
