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
// Collections are NOT auto-created by PocketBase on first access — that comment
// was a lie. Each collection must be explicitly registered here so a fresh
// `make dev` clone can create todos out of the box without hitting the admin
// UI first.
func SeedDefaults(app *pocketbase.PocketBase) error {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		if err := ensureTodosCollection(se.App); err != nil {
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
func ensureTodosCollection(app core.App) error {
	col, err := app.FindCollectionByNameOrId("todos")
	if err != nil {
		col = core.NewBaseCollection("todos")
		col.Fields.Add(
			&core.TextField{Name: "title", Required: true},
			&core.BoolField{Name: "completed"},
			&core.DateField{Name: "created"},
			&core.DateField{Name: "updated"},
		)
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

	if err := app.Save(col); err != nil {
		return fmt.Errorf("seed: save todos collection: %w", err)
	}
	return nil
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
		return nil
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
		return nil
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

// ptr returns a pointer to v. Tiny helper so rule fields (which are
// *string) can be set without a local variable at each call site.
//
//go:fix inline
