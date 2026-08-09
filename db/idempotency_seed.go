// SCOPE:plugin - REMOVE if you don't need idempotent offline replay.
//
// Adds the `idem_key` field (and optionally a unique index) to a
// `todos`-like collection. Split into two helpers so the call site in
// db/seed.go can decide what to wire based on OfflineSync.Enabled:
//
//   - AddIdemKeyField     — always called. The idem_key column is sent
//     by every Create POST (the createForm
//     hidden input). It is also useful as a
//     request dedupe token even when offline-sync
//     is disabled (retries / double-click).
//   - AddIdemKeyUniqueIndex — called ONLY when offline-sync is on.
//     The (idem_key, owner) unique index is
//     what the OnRecordCreateRequest hook
//     relies on to dedupe replays of the SW
//     queue. Without offline-sync there are
//     no replays to dedupe; the index is dead
//     weight.
//   - enableTodosIdempotency — the original combined helper; field +
//     index. Kept for tests that want both.
//
// Companion: db/idempotency_hook.go (the OnRecordCreateRequest dedup
// hook itself). Remove by deleting this file + idempotency_hook.go +
// dropping the calls below + removing the hidden `name="idem_key"`
// input from the form.
package db

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// BackfillIdemKey assigns a unique idem_key to existing rows that have a
// missing/empty idem_key (legacy data created before the idem_key field
// existed). Without this, adding the (idem_key, owner) UNIQUE index fails
// on pre-existing rows that share an owner — they all get the same empty
// default and collide.
//
// Call this BEFORE AddIdemKeyUniqueIndex when offlineSync is enabled.
// Idempotent: rows that already have a non-empty idem_key are untouched.
func BackfillIdemKey(app core.App, collectionName string) error {
	res, err := app.DB().NewQuery(
		"UPDATE `" + collectionName + "` SET `idem_key` = ('migrated-' || lower(hex(randomblob(8)))) " +
			"WHERE `idem_key` = '' OR `idem_key` IS NULL",
	).Execute()
	if err != nil {
		return fmt.Errorf("seed: backfill idem_key on %s: %w", collectionName, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		slog.Info("seed: backfilled unique idem_key", "collection", collectionName, "rows", n)
	}
	return nil
}

// AddIdemKeyField adds the `idem_key` text field to a todos-like
// collection. Idempotent: skipped if the field already exists.
func AddIdemKeyField(col *core.Collection) {
	if col.Fields.GetByName("idem_key") == nil {
		col.Fields.Add(&core.TextField{Name: "idem_key", Max: 100})
		slog.Info("seed: ensured todos.idem_key field")
	}
}

// AddIdemKeyUniqueIndex adds the (idem_key, owner) unique index.
// Idempotent (skipped if an index with the same name exists).
// Call only when offline-sync is on.
func AddIdemKeyUniqueIndex(col *core.Collection) {
	const indexName = "idem_key_owner_idx"
	for _, sql := range col.Indexes {
		// Indexes are stored as raw SQL CREATE INDEX statements in
		// PocketBase; match by the index name we set below.
		if strings.Contains(sql, `"`+indexName+`"`) {
			return
		}
	}
	col.AddIndex(indexName, true, "idem_key,owner", "")
}

// enableTodosIdempotency is the combined helper (field + index), kept
// for callers that always want both.
func enableTodosIdempotency(col *core.Collection) {
	AddIdemKeyField(col)
	AddIdemKeyUniqueIndex(col)
}
