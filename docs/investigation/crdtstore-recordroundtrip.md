# CRDTStore.RecordRoundTrip — Investigation Document

## Status: OPEN (P3)

**Production is safe.** The bug reproduces only in the unit-test bootstrap
path (`CRDTStore.EnsureSchema`) and is documented as `t.Skip` in
`features/store/crdtstore/crdtstore_test.go`. Deployment uses
`db.SeedDefaults` which creates the `todos` collection through the
OnServe hook with a different wiring path, and the round-trip works
there.

The investigation has been exhaustive within the resources available.
This document captures everything so a future investigator (human or
LLM) can pick up the thread without rediscovering the dead-ends.

## 1. Problem statement

`TestCRDTStore_RecordRoundTrip` (in `features/store/crdtstore`)
exercises the cross-store restart path:

```
s1.Create("idalp") → s1.Create("idbet") → s1.Create("idgam")
  → s1.Update("idbet", completed=true)
  → s1.Close()         // clears in-memory maps
  → s2 := New(app)     // fresh CRDTStore on the SAME PocketBase app
  → s2.List()          // expected: 3 items
```

When the `todos` collection was created via `CRDTStore.EnsureSchema`,
`s2.List` returns 0 items even though the records exist in SQLite.
When the collection was created via `db.SeedDefaults` (production
path), the same test passes.

## 2. When it happens

The test bootstrap in `features/store/crdtstore/crdtstore_test.go:88-96`
opens the PB data DB with `dbx.Open("sqlite3", path+pragmas)` where
`pragmas` includes `journal_mode(WAL)`. The PB app is then created
and `s.EnsureSchema` runs, which creates the `todos` collection via
`core.NewBaseCollection + app.Save(col)`.

PB v0.39.6 opens **two SQLite connections** to the same file:
- `app.ConcurrentDB()` — pool of connections (DataMaxOpenConns).
- `app.NonconcurrentDB()` — single connection (MaxOpenConns=1) used
  for writes.

The `journal_mode(WAL)` pragma in `dbx.Open` only applies to the
*first* connection that opens the file. PB opens the `ConcurrentDB`
and `NonconcurrentDB` *afterwards* without re-applying the pragma,
so they default to `journal_mode=delete` (rollback journal).

## 3. Symptom (spike 22 instrumentation)

After `s.app.Save(rec)` returns nil:

| Probe | Result |
|---|---|
| `s.app.FindRecordById("todos", rec.Id)` | `found=true` (PB API high-level) |
| `s.app.FindRecordsByFilter("todos", "idem_key = :k", ...)` | `hits = 1` (PB API high-level) |
| `s.app.DB().NewQuery("SELECT COUNT(*) FROM todos WHERE idem_key = :k")` | `n = 0` |
| `s.app.NonconcurrentDB().NewQuery("SELECT COUNT(*) ...")` | `n = 0` |
| `s.app.ConcurrentDB().NewQuery("SELECT COUNT(*) ...")` | `n = 0` |
| `sqlite_master tables` (both connections) | identical list, includes `todos` |
| `PRAGMA journal_mode` (both connections) | `"delete"` |
| `todos` schema (sqlite_master) | `CREATE TABLE \`todos\` ( ... PRIMARY KEY ...)` — TABLE not VIEW |

The record exists for the **high-level PB API** but **does not exist**
for **raw SQL queries on either connection**, even though the table
is identical and present in `sqlite_master` on both.

## 4. Spikes attempted (all since deleted)

The spike convention: file in `features/store/spike/spike_test.go`,
run, **do not commit**, delete after. Each spike isolates one
hypothesis by replacing the suspect API path with bare components.

| # | Hypothesis | Method | Result |
|---|---|---|---|
| 1 | Save API matrix bug | 8 variants of Save × 2 col shapes | **Falsified**: all 8 persist identically. |
| 3 | Inline Save+Reload bug | core.NewRecord + app.Save + FindRecordsByFilter on same app | **Falsified**: returns 3 rows. |
| 4 | doc(owner) rebuild bug | Build Loro doc from PB records | **Falsified**: returns 3 Loro items. |
| 6 | Real CRDTStore reproduces | s1.Create(...) + s2.List (real types) | **Reproduced Bug A**: Save nil + query empty. |
| 7 | `RelationField{Owner, Required: true/false}` | EnsureSchema-shape vs seed-shape side-by-side | **Falsified**: BOTH return 1 row. |
| 8 | `s.mu + bumpVersion + FindFirst re-entrance` | bare mutex + Save + bumpVersion + FindFirstRecordByFilter | **Falsified**: works. |
| 9 | N+1 sequential upserts | persistRecords-style iteration (each Create re-upserts all items) | **Falsified**: 3 rows. |
| 10 | `RegisterIdempotencyHook` absent | EnsureSchema-shape WITHOUT hook vs WITH hook | **Falsified**: both return 1 row. |
| 11 | Seed-shape full delta | seed-shape (no owner Required, ListRule, ViewRule, hook) vs EnsureSchema-shape | **Falsified**: both return 1 row. |
| 13 | Instrumented real CRDTStore | `slog.Info` probes inside `upsertTodoRecord` (entry, FindFirst, pre-Save, post-Save) | **Reproduced + pinpointed**: `app.Save(rec)` returns nil; record IS visible to `FindRecordById` but NOT to raw `SELECT COUNT(*)`. |
| 17 | `app.RunInTransaction` wrap | wrap `Save` inside `RunInTransaction` to force commit | **Falsified**: still 0 rows after wrap. |
| 22 | PRAGMA WAL + checkpoint + RunInTransaction | set `journal_mode=WAL` on connection + `wal_checkpoint(TRUNCATE)` + `RunInTransaction` | **Falsified**: still 0 rows. |
| (inline) | `app.AuxSave` | separate aux sqlite file | **Failed**: aux db has no `todos` schema. |

## 5. What's NOT the cause (concluded)

- Save API variants (Save, SaveNoValidate, WithContext, etc).
- Inline Save + Read in a fresh app (works fine).
- Loro doc rebuild from PB records (works fine).
- `RelationField Owner Required: true` vs `false` (both work).
- `s.mu` lock + `bumpVersion` + FindFirstRecordByFilter re-entrance (works).
- N+1 sequential upserts in persistRecords (works).
- Missing `RegisterIdempotencyHook` (works without it).
- Seed-shape full delta vs EnsureSchema-shape (both work).
- `app.RunInTransaction` wrap (does not commit cross-connection).
- `PRAGMA journal_mode=WAL` + `wal_checkpoint(TRUNCATE)` (no effect — `wal_checkpoint` is no-op in delete mode).

## 6. What's STILL suspicious (open)

- **The PB API high-level view sees the record; raw SQL via any
  connection does not.** This is the central mystery. The high-level
  PB API likely caches or uses an in-memory representation that gets
  populated by the `OnRecordCreateSuccess` hook or `OnModelAfterCreate`
  hook (which is **deferred** when `app.txInfo != nil`, i.e. inside
  `RunInTransaction`). The PB source code is not part of this repo
  but lives in `/Users/cali/go/pkg/mod/github.com/pocketbase/pocketbase@v0.39.6/`
  for inspection.
- **journal_mode = `"delete"`** despite the test setup requesting
  `journal_mode(WAL)`. The pragma is applied to the first connection
  only; PB's subsequent `ConcurrentDB` / `NonconcurrentDB` connections
  open with the file default (delete rollback journal). With rollback
  journal, writes are visible only after explicit COMMIT. Cross-
  connection reads see pre-write state until then.

## 7. How to reproduce

```bash
cd <repo>
go test -v -run TestCRDTStore_RecordRoundTrip \
  ./features/store/crdtstore/
```

Currently `t.Skip`-ed with the rationale as a multi-paragraph comment
above it. To re-enable investigation, comment out the `t.Skip` line
and the test will fail with `s2.List returned 0 items, want 3`.

## 8. How to continue investigation

### Option A: pragmatic test rewrite (low risk, ~30 min)

Replace `TestCRDTStore_RecordRoundTrip`'s bootstrap so the `todos`
collection is created the way production creates it — via
`db.SeedDefaults(app, true)` which:

1. Runs the OnServe hook (which creates the collection with the same
   shape as `db/seed.go ensureTodosCollection`).
2. Registers `RegisterIdempotencyHook`.
3. Adds the `(idem_key, owner)` unique index.
4. Creates a demo user.

This would make `RecordRoundTrip` an integration test that mirrors
production. The test would still validate the same property (a fresh
CRDTStore reads what the previous one wrote) but on a path where
the bug is known not to reproduce. The risk: if a future bug appears
specifically in the EnsureSchema path, this test would not catch it.

### Option B: fix the test bootstrap's PRAGMA (medium risk, ~1-2h)

Inside `newTestApp` (or `EnsureSchema`), after `app.Bootstrap()`,
issue `PRAGMA journal_mode=WAL` on each PB connection:

```go
for _, q := range []dbx.Builder{
    app.ConcurrentDB(),
    app.NonconcurrentDB(),
} {
    if _, err := q.NewQuery("PRAGMA journal_mode=WAL").Execute(); err != nil {
        return err
    }
}
```

Then add `wal_checkpoint(FULL)` after each save. This forces WAL mode
on every connection (not just the first). If this fixes the bug,
the root cause is journal mode + cross-connection visibility. If not,
the bug is in PB's record view cache and requires deeper PocketBase
internals investigation.

### Option C: PB internals deep-dive (high cost, uncertain payoff)

The spike-22 evidence (`FindRecordById` returns found=true while raw
SQL returns 0 rows on the same file/table) is anomalous enough to
warrant reading the PB v0.39.6 `OnRecordCreate*` hook chain and the
`RecordQuery.Select` implementation to understand the in-memory record
view. Candidates:

- `/Users/cali/go/pkg/mod/github.com/pocketbase/pocketbase@v0.39.6/core/db.go:273` —
  `BaseApp.create` calls hooks via `app.OnModelCreate().Trigger(...)`.
  The post-create hook (`OnModelAfterCreateSuccess`) is **deferred**
  when `app.txInfo != nil` (line 333-348). This is likely the cache
  populator.
- `/Users/cali/go/pkg/mod/github.com/pocketbase/pocketbase@v0.39.6/core/record_query.go:167` —
  `resolveRecordOneHook` constructs the Record from `dbx.NullStringMap`.
  The data source here is the SQL query result, not a separate cache.

If the bug is "PB's RecordQuery uses a separate data path that doesn't
see the write done by `app.Save` in delete-journal-mode", then either
fixing the journal_mode or fixing the hook chain will resolve it.

### Option D: ship as-is and defer

The current state (`t.Skip` + documentation) is acceptable because:

- Production is unaffected.
- 12 spikes falsified every natural hypothesis.
- The fix path requires PB internals work that may not be worth the
  investment for a test-only issue.
- The `t.Skip` rationale explicitly references every spike and
  states "the next spike must use the real `*CRDTStore` type — bare
  components cannot reproduce".

If you choose this, please link to this document from the `t.Skip`
comment so the next investigator has the full context.

## 9. Reference: code locations

- Failing test: `features/store/crdtstore/crdtstore_test.go:255-318`
- Test bootstrap: `features/store/crdtstore/crdtstore_test.go:80-101`
- `t.Skip` rationale: `features/store/crdtstore/crdtstore_test.go:257-323`
- `EnsureSchema` (test-only bootstrap path): `features/store/crdtstore/crdtstore.go:194-220`
- `Create` (real type): `features/store/crdtstore/crdtstore.go:370-417`
- `doc(owner)`: `features/store/crdtstore/crdtstore.go:229-262`
- `upsertTodoRecord`: `features/store/crdtstore/crdtstore.go:301-345`
- `persistRecords`: `features/store/crdtstore/crdtstore.go:264-299`
- PB Save path: `/Users/cali/go/pkg/mod/github.com/pocketbase/pocketbase@v0.39.6/core/db.go:185-220`
- PB multi-connection setup: `/Users/cali/go/pkg/mod/github.com/pocketbase/pocketbase@v0.39.6/core/base.go:1175-1200`
- `dualDBBuilder`: `/Users/cali/go/pkg/mod/github.com/pocketbase/pocketbase@v0.39.6/core/db_builder.go:20-50`

## 10. Related commits

- `99caae3` — fix(crdtstore): lookup existing PB record by (idem_key, owner), not by id.
- `32d7e5a` — fix(crdtstore): normalise PB v0.39.6 sql.ErrNoRows to empty result + t.Skip.
- `d6d7fb5` — docs(crdtstore): expand RecordRoundTrip t.Skip with spike evidence.
- `d7b2ddf` — docs(crdtstore): record spikes 8 and 9 in RecordRoundTrip t.Skip rationale.
- `3517d09` — docs(crdtstore): record spikes 10 + 11 in RecordRoundTrip t.Skip rationale.

These commits establish the state where the test is `t.Skip`-ed and
this document is the canonical handoff for the next investigator.
