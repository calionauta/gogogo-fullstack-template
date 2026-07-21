// SCOPE:layer=infra,removal=core — EntityStore interface (the contract every storage
//
// Package store defines the plugin interface that abstracts how
// domain entities (todos, notes, future entities) are persisted.
// Two strategies live behind this interface today:
//
//   - PBStore (features/store/pbstore): PocketBase records + the
//     idempotency hook installed by db/idempotency_hook.go. Default.
//   - CRDTStore (features/store/crdtstore, future): Loro doc per
//     owner + JetStream MsgId for transport. Trade-off for the
//     whiteboard-style collaborative use case (multi-user, multi-device).
//
// Both strategies satisfy the same EntityStore[T] interface, so the
// HTTP handlers and templates don't know or care which one is wired.
// The choice is made at startup via config.EntityStore.
//
// Why generic: a future Note or Task entity should reuse the same
// pattern (Create / Get / List / Update / Delete with owner-scoped
// queries and idempotency on create). Tying the interface to Todo
// would force every new entity to either duplicate the boilerplate
// or refactor this file; Go generics let each strategy implement
// once for any Entity and each caller pin T to its own domain type.
//
// Trade-offs documented in ARCHITECTURE.md.
package store

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a Get/Update/Delete targets an entity
// the strategy can't find (either it never existed, or it belongs to
// a different owner — the strategy MUST NOT leak the existence of
// another owner's row, so it returns NotFound rather than Forbidden).
var ErrNotFound = errors.New("store: entity not found")

// ErrNotImplemented is returned by optional operations a strategy
// hasn't implemented (currently just ClearCompleted; CRDTStore
// could either implement it via Loro ops or return this and have
// the caller fall back to List + per-row Delete).
var ErrNotImplemented = errors.New("store: operation not implemented")

// EntityStore is the plugin interface every storage strategy
// implements. The type parameter T is the domain entity type (Todo,
// Note, etc); the strategy is responsible for translating T to/from
// its native representation (PB records, Loro snapshot, ...).
type EntityStore[T any] interface {
	// Create persists a new entity owned by ownerID. idemKey is a
	// client-generated UUID used to dedup offline replays; strategies
	// that have a native dedup primitive (PB unique index + hook,
	// JetStream MsgId) use it, others ignore it. Returns the persisted
	// entity (with any server-assigned fields filled in — id, timestamps).
	Create(ctx context.Context, e T, ownerID, idemKey string) (T, error)

	// Get returns the entity owned by ownerID. Returns ErrNotFound
	// if the entity doesn't exist or belongs to another owner.
	Get(ctx context.Context, ownerID, id string) (T, error)

	// List returns entities owned by ownerID, optionally filtered.
	// filter is strategy-defined (e.g. "active", "completed", "" for
	// all). The strategy documents its accepted filter values.
	List(ctx context.Context, ownerID, filter string) ([]T, error)

	// Update applies patch to the entity owned by ownerID. patch is a
	// map of field name → new value. Returns the updated entity.
	// Returns ErrNotFound if the entity doesn't exist.
	Update(ctx context.Context, ownerID, id string, patch map[string]any) (T, error)

	// Delete removes the entity owned by ownerID. Idempotent — a
	// second delete on the same id returns nil. Returns ErrNotFound
	// if the entity doesn't exist (callers can ignore this on retry).
	Delete(ctx context.Context, ownerID, id string) error

	// ClearCompleted removes all completed entities owned by ownerID.
	// Returns the number deleted. Strategies without a native bulk
	// delete can return ErrNotImplemented; the caller falls back to
	// List + per-row Delete.
	ClearCompleted(ctx context.Context, ownerID string) (int, error)

	// Count returns the number of entities owned by ownerID. Cheap
	// (count query, no full load) so callers can use it on hot paths
	// like the realtime broadcast badge.
	Count(ctx context.Context, ownerID string) (int, error)
}
