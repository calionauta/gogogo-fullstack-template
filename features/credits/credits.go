// SCOPE:layer=feature,removal=plugin — AI credits + BYOK (lib ai-credits).
// To remove: delete features/credits/ + router/credits.go + config fields.
//
// Wires the calionauta/ai-credits library (immutable ledger + pricing +
// reservations + monthly grants + reconcile + BYOK relay) into the app's
// PocketBase SQLite. The lib takes a *sql.DB; the app exposes neither via
// app.DB() (it returns an opaque dbx.Builder hiding the concrete *sql.DB),
// so — like the goqite queue (internal/queue) — we open a second connection
// to the same DB file. WAL + busy_timeout make cross-connection writes safe.
// Billing is fully opt-in: when CREDITS_ENABLED=false the plugin builds
// nothing and registers no routes (see router/credits.go).
package credits

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	_ "github.com/ncruces/go-sqlite3/driver"

	"github.com/calionauta/ai-credits/credits"
	"github.com/calionauta/gogogo-fullstack-template/config"
)

// Service bundles the credits engine plus the app-facing deps handlers need.
type Service struct {
	Credits *credits.Service
	Store   *credits.CredentialStore
	Relay   *credits.ByokRelay
	Cfg     *config.CreditsConfig
	Model   string // default managed model id
	AppKey  string // app-level GOAI_API_KEY for managed mode
	BaseURL string // app-level GOAI_BASE_URL for managed mode
	Now     func() time.Time
}

// New builds the credits engine on the app's SQLite DB file (a fresh
// connection to cfg.DBPath), applies the lib-owned schema idempotently, and
// constructs the BYOK credential store + relay. Returns (nil, nil) when the
// feature is disabled — handlers then no-op and the binary boots
// billing-free.
func New(cfg *config.Config) (*Service, error) {
	if !cfg.Credits.Enabled {
		return nil, nil
	}
	db, err := openCreditsDB(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	svc, err := credits.New(db, credits.Config{
		DefaultBillingMode: cfg.Credits.DefaultMode,
		MonthlyCredits:     cfg.Credits.MonthlyCredits,
		ReservationTimeout: defaultReservationTimeout,
		PricingReader:      pricingReader(cfg.Credits.PricingFile),
		Now:                time.Now,
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := svc.EnsureSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	var store *credits.CredentialStore
	if len(cfg.Credits.EncKey) == keyLen32 {
		var k [keyLen32]byte
		copy(k[:], cfg.Credits.EncKey)
		store = svc.NewCredentialStore(k)
	} else {
		slog.Warn("credits: BYOK store disabled (CREDITS_ENC_KEY not 32 bytes)")
	}

	var relay *credits.ByokRelay
	if store != nil {
		relay = svc.NewByokRelay(store, cfg.Credits.ByokProviders, slog.Default())
	}

	return &Service{
		Credits: svc,
		Store:   store,
		Relay:   relay,
		Cfg:     &cfg.Credits,
		Model:   cfg.Credits.Model,
		AppKey:  cfg.GoAI.APIKey,
		BaseURL: defaultBaseURL,
		Now:     time.Now,
	}, nil
}

// defaultBaseURL matches the internal/llm default when GOAI_BASE_URL is unset.
const defaultBaseURL = "https://api.openai.com/v1"

// keyLen32 is the XChaCha20-Poly1305 key size (32 bytes) the BYOK credential
// store requires.
const keyLen32 = 32

// openCreditsDB opens a second SQLite connection to the app DB file, using
// the same ncruces driver the goqite queue loads. WAL (set by PocketBase at
// startup) allows concurrent readers/writers across connections.
func openCreditsDB(path string) (*sql.DB, error) {
	if path == "" {
		path = "data/app.db"
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	return db, nil
}

// defaultReservationTimeout is the lib default (5m) mirrored here so the
// plugin is self-documenting without coupling to lib internals.
const defaultReservationTimeout = 5 * time.Minute

// newRequestID returns a fresh idempotency-scoped request id for reserves,
// usage records, and ledger keys (req:<id>). UUID v4; collisions negligible.
func newRequestID() string { return uuid.NewString() }

// pricingReader returns the configured pricing JSON reader, or nil for the
// lib's built-in defaults.
func pricingReader(path string) io.Reader {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		slog.Warn("credits: open pricing file", "path", path, "err", err)
		return nil
	}
	return f
}
