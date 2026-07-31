// SCOPE:core - Collection creation and schema setup.
// The demo user seed (DemoUserEmail, DemoUserPassword, ensureDemoUser)
// is SCOPE:feature — delete if you don't need demo credentials.
// Collection rule locking is CORE (keeps the public demo safe).
package db

import (
	"fmt"
	"log/slog"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

// Demo credentials seeded on first run. The owner of a deployed
// gogogo-fullstack-template MUST override these or disable the seed before
// exposing the app to the internet. Left as exported vars so a
// downstream project can swap them from cmd/web/main.go.
var (
	DemoUserEmail    = "demo@demo.app"
	DemoUserPassword = "demo1234456"
)

// SeedDefaults creates default collections and data on first run.
//
// Pass offlineSyncEnabled to wire (or skip) the offline-first idempotency
// machinery. With offline-sync disabled:
//
//   - RegisterIdempotencyHook is NOT installed (no SW queue → no replays
//     to dedupe; PB realtime alone keeps the UI live).
//   - The (idem_key, owner) unique index is NOT created on the todos
//     collection (the hook + index pair has no purpose without a queue).
//
// The idem_key FIELD is always added to todos regardless of the flag:
// every Create POST sends it as a request-dedupe token, useful for
// retry / double-click even when offline-sync is off.
//
// Collections are NOT auto-created by PocketBase on first access — that comment
// was a lie. Each collection must be explicitly registered here so a fresh
// `make dev` clone can create todos out of the box without hitting the admin
// UI first.
func SeedDefaults(app *pocketbase.PocketBase, offlineSyncEnabled bool) error {
	// Idempotency dedup hook for the todos collection (see
	// idempotency_hook.go). Only registered when offline-sync is on;
	// without a queue replaying requests there are no duplicates to catch.
	if offlineSyncEnabled {
		RegisterIdempotencyHook(app)
	}

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		if err := ensureTodosCollection(se.App, offlineSyncEnabled); err != nil {
			slog.Error("seed: ensureTodosCollection failed", "error", err)
		}
		if err := ensureDemoUser(se.App); err != nil {
			slog.Error("seed: ensureDemoUser failed", "error", err)
		}
		// Lock the users collection so the public demo can't create or
		// delete accounts through the API / admin UI (the demo superuser
		// still can). See ensureUsersCollectionRules.
		if err := ensureUsersCollectionRules(se.App); err != nil {
			slog.Error("seed: ensureUsersCollectionRules failed", "error", err)
		}
		// Collaborative whiteboards (Loro CRDT snapshots) — the SyncWorker
		// (internal/collab) persists resolved docs here.
		if err := ensureWhiteboardsCollection(se.App); err != nil {
			slog.Error("seed: ensureWhiteboardsCollection failed", "error", err)
		}
		return se.Next()
	})
	return nil
}

// ensureTodosCollection creates the "todos" collection (with an owner
// relation to the `users` auth collection) if it doesn't exist, and
// backfills the owner relation on collections created by older seeds
// that lacked it. Todos are scoped to a tenant via owner so the demo
// user — and any authenticated user — only sees their own todos.
//
// The idem_key FIELD is always added (the request dedupe token has
// uses outside of offline-sync). The (idem_key, owner) unique INDEX
// is added only when offlineSyncEnabled — it pairs with the
// RegisterIdempotencyHook to dedupe SW queue replays, which never
// happen when offline-sync is off.
func ensureTodosCollection(app core.App, offlineSyncEnabled bool) error {
	col, err := app.FindCollectionByNameOrId("todos")
	if err != nil {
		col = core.NewBaseCollection("todos")
		col.Fields.Add(
			&core.TextField{Name: "title", Required: true},
			&core.BoolField{Name: "completed"},
		)
	}

	// Fix CAL-19: created/updated must be PocketBase AutodateFields so PB
	// stamps them on create/update. A previous seed declared them as plain
	// DateFields, which are never auto-stamped — every record then carried a
	// zero (0001-01-01) created time, and the list query (which sorts by
	// -created) broke once the shadowed columns were dropped. Replace them
	// on existing collections; a fresh collection gets them from the same
	// helper.
	if ensureTodoTimestampFields(col) {
		slog.Info("seed: restored created/updated as AutodateFields on todos (system timestamps re-enabled)")
	}

	// Idempotent: only add the owner relation if it is missing (covers
	// both brand-new collections and existing ones from older seeds).
	if col.Fields.GetByName("owner") == nil {
		usersCol, uErr := app.FindCollectionByNameOrId("users")
		if uErr != nil {
			return fmt.Errorf("seed: users collection not found, cannot add owner relation: %w", uErr)
		}
		col.Fields.Add(&core.RelationField{
			Name:         "owner",
			MaxSelect:    1,
			CollectionId: usersCol.Id,
		})
		slog.Info("seed: ensured todos.owner relation -> users")
	}

	// idem_key field is always added — the request dedupe token is
	// useful outside of offline-sync (retry / double-click guard).
	// The (idem_key, owner) unique index is the hook's dedupe target
	// and only matters when offline-sync replays queued requests.
	AddIdemKeyField(col)
	if offlineSyncEnabled {
		AddIdemKeyUniqueIndex(col)
	}

	// Realtime + REST access: a user may only view THEIR OWN todos.
	// PocketBase realtime delivers a record event to a subscriber only if
	// that subscriber's auth passes the collection's list/view rule; a nil
	// rule means superuser-only, so the demo user would never receive
	// create/update/delete events (this is what broke the PB-realtime
	// record path — the hub broadcast masked it in tests). Owner-scoping
	// also prevents cross-user todo leaks. The app's own server-side list
	// rendering bypasses these API rules (it queries via the Dao with an
	// owner filter), so this only governs the PB realtime channel + raw
	// REST API.
	const todoViewRule = "@request.auth.id != '' && owner = @request.auth.id"
	if col.ListRule == nil || *col.ListRule != todoViewRule {
		r := todoViewRule
		col.ListRule = &r
	}
	if col.ViewRule == nil || *col.ViewRule != todoViewRule {
		r := todoViewRule
		col.ViewRule = &r
	}

	if err := app.Save(col); err != nil {
		return fmt.Errorf("seed: save todos collection: %w", err)
	}
	return nil
}

// ensureTodoTimestampFields makes sure the collection's created/updated are
// AutodateFields (auto-stamped by PocketBase on create/update), replacing
// plain DateFields if a previous seed declared them that way. Returns true
// if the schema changed. See the CAL-19 note in ensureTodosCollection.
func ensureTodoTimestampFields(col *core.Collection) bool {
	changed := false
	ensure := func(name string, onCreate, onUpdate bool) {
		existing := col.Fields.GetByName(name)
		if _, ok := existing.(*core.AutodateField); ok {
			return
		}
		if existing != nil {
			col.Fields.RemoveByName(name)
		}
		col.Fields.Add(&core.AutodateField{Name: name, OnCreate: onCreate, OnUpdate: onUpdate})
		changed = true
	}
	ensure("created", true, false)
	ensure("updated", true, true)
	return changed
}

// ensureWhiteboardsCollection creates the "whiteboards" collection that
// stores resolved Loro CRDT snapshots from the collaborative SyncWorker.
// doc_id is the unique key (matches the JetStream subject app.sync.<docID>);
// snapshot is the base64 Loro doc; version is a monotonic counter for
// idempotent upserts.
func ensureWhiteboardsCollection(app core.App) error {
	col, err := app.FindCollectionByNameOrId("whiteboards")
	if err != nil {
		col = core.NewBaseCollection("whiteboards")
		col.Fields.Add(
			&core.TextField{Name: "doc_id", Required: true},
			&core.TextField{Name: "snapshot"},
			&core.NumberField{Name: "version"},
			&core.DateField{Name: "updated"},
		)
	}
	// RULES: any authenticated user may list/view whiteboards so the
	// PocketBase realtime subscription works for every logged-in user.
	// The app's own server-side rendering bypasses these API rules (it
	// queries via the Dao), so this only governs the PB realtime channel.
	wbViewRule := "@request.auth.id != ''"
	if col.ListRule == nil || *col.ListRule != wbViewRule {
		r := wbViewRule
		col.ListRule = &r
	}
	if col.ViewRule == nil || *col.ViewRule != wbViewRule {
		r := wbViewRule
		col.ViewRule = &r
	}

	if err := app.Save(col); err != nil {
		return fmt.Errorf("seed: save whiteboards collection: %w", err)
	}
	return nil
}

// `users` collection (the auth collection). Uses email lookup +
// password re-set so the seed is idempotent across restarts and so
// the demo password is always current.
func ensureDemoUser(app core.App) error {
	col, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		// First run, users collection might not exist yet.
		return nil //nolint:nilerr // collection not created yet; skip seed, not an error
	}
	if existing, err := app.FindAuthRecordByEmail(col.Name, DemoUserEmail); err == nil && existing != nil {
		// Refresh the password so a cloned template always uses
		// the documented demo password even if someone reset it
		// through the admin UI.
		existing.SetPassword(DemoUserPassword)
		if saveErr := app.Save(existing); saveErr != nil {
			return saveErr
		}
		return nil
	}
	record := core.NewRecord(col)
	record.SetEmail(DemoUserEmail)
	record.SetPassword(DemoUserPassword)
	if saveErr := app.Save(record); saveErr != nil {
		return saveErr
	}
	slog.Info("seed: created demo user", "email", DemoUserEmail)
	return nil
}

// ensureUsersCollectionRules hardens the built-in `users` auth collection
// for demo deployments: non-superusers may view + update their own record
// but CANNOT create new users or delete any user via the API or the admin
// UI. Only the PocketBase superuser (the first admin created via the
// install link in the server logs) retains full CRUD. This keeps the
// public demo safe from account-spam while still letting visitors log in
// as the seeded demo user and manage their own profile.
//
// The rules are idempotent: they only write when a rule differs from the
// locked-down value, so re-running the seed is a no-op after the first set.
func ensureUsersCollectionRules(app core.App) error {
	col, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		// Users collection not created yet (very first boot before
		// PocketBase's own bootstrap); skip — SeedDefaults runs on every
		// serve, so it will lock it on the next boot once it exists.
		return nil //nolint:nilerr // collection not yet created; will retry on next boot
	}

	const locked = "@request.auth.superuser = true"
	changed := false
	if col.CreateRule == nil || *col.CreateRule != locked {
		col.CreateRule = new(locked)
		changed = true
	}
	if col.DeleteRule == nil || *col.DeleteRule != locked {
		col.DeleteRule = new(locked)
		changed = true
	}
	// List/View/Update: allow the record owner + superuser (default
	// PocketBase behavior) — keep a safe non-open rule.
	if col.ListRule == nil || *col.ListRule == "" {
		rule := "@request.auth.superuser = true || @request.auth.id = @id"
		col.ListRule = new(rule)
		changed = true
	}

	if !changed {
		return nil
	}
	if err := app.Save(col); err != nil {
		return fmt.Errorf("seed: lock users collection: %w", err)
	}
	slog.Info("seed: locked users collection (no public create/delete)")
	return nil
}
