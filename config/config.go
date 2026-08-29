// SCOPE:core - DO NOT REMOVE - Server configuration (env vars, secrets).
//
// ── Env vars (all optional, see Load() for defaults) ──
//
//	PORT                (default: 8080)          — HTTP listen port
//	HOST                (default: "0.0.0.0")     — HTTP listen address
//	ENVIRONMENT         (default: "development") — set to "production" for prod mode
//	APP_NAME            (default: binary name)   — project name (secrets scope)
//	LOG_LEVEL           (default: "INFO")
//	DATABASE_PATH       (default: "data/data.db") — the SQLite file the
//	                    credits ledger shares with PocketBase (see DATA_DIR)
//	DATA_DIR            (default: "data")
//	ENCRYPTION_KEY      (default: "") — PocketBase encryption key
//	ADMIN_UNLOCK_TOKEN  (default: "") — master password for admin endpoints
//	GOAI_API_KEY        (default: "") — LLM provider API key
//	GOAI_BASE_URL       (default: "https://api.openai.com/v1") // LLM base URL
//	GOAI_MODEL          (default: "gpt-4o-mini")  — LLM model ID
//	SIMULATE_LLM        (default: "true" in dev) — enable simulated LLM
//	NATS_ENABLED        (default: true)  — enable NATS JetStream
//	NATS_STORE_DIR      (default: "data/nats")
//	NATS_LEAFNODE_URL   (default: "")    — connect as NATS Leaf Node
//	DAGNATS_ENABLED     (default: true)  — enable DagNats workflows
//	DAGNATS_HTTP_ADDR   (default: "127.0.0.1:8090")
//	DAGNATS_NATS_PORT   (default: 4222)
//	CREDITS_ENABLED     (default: false) — enable ai-credits managed/BYOK billing
//	CREDITS_ENC_KEY     (default: "") — 32 raw bytes or 64 hex chars for BYOK key storage
//	BYOK_PROVIDERS      (default: "") — provider=url pairs for the BYOK relay
//	DAGNATS_STORE_DIR   (default: "data/dagnats")
//	OFFLINE_SYNC_ENABLED (default: true) — toggle hybrid offline sync
//	ENTITY_STORE         (default: "pb") — todo persistence strategy
//	                       (see features/store/store.go)
//	UI_SKIN              (default: "daisyui") — active UI skin: "daisyui", "basecoat", "morpheus"
//	                       (see web/skins/)
//
// ── Runtime constants (tune in config.go, consumed across packages) ──
//
//	DefaultReplayBufferSize     (64)  — SSEHub per-client ring-buffer
//	DefaultClientQueueSize      (64)  — SSEHub per-client channel buffer
//	DefaultSSEHeartbeatInterval (15s) — SSE heartbeat ticker interval
package config

import (
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/calionauta/gogogo-fullstack-template/internal/secrets"
)

// ── Runtime constants ──

// DefaultReplayBufferSize is the per-client SSEHub ring-buffer length.
// Sized for ~64KB of Datastar patch-signals at ~1KB each.
const DefaultReplayBufferSize = 64

// DefaultClientQueueSize is the per-client SSEHub channel buffer.
// Both the todo and whiteboard SSE handlers use 64 slots — tune
// this constant globally.
const DefaultClientQueueSize = 64

// DefaultSSEHeartbeatInterval is how often the SSE handler writes a
// comment line (: heartbeat) to detect client disconnection. 15s is
// the industry standard for SSE heartbeats.
const DefaultSSEHeartbeatInterval = 15 * time.Second

type Config struct {
	// AppName is used as the scope for the secrets file
	// (~/.secrets/<AppName>.env.age) and the project name in logs.
	// Derived from APP_NAME env or the binary name; never empty.
	AppName string

	// BuildLabel is the human-readable build identifier (e.g.
	// "v0.21.0" or "dev"). Surfaced on the navbar version badge so
	// a tester can verify which binary is running by visual
	// inspection. Set via BUILD_LABEL env var (overwritten by the
	// Makefile via -ldflags="-X main.Version" at build time).
	BuildLabel string

	// BuildCommit is the short git commit hash. Surfaced alongside
	// BuildLabel on the version badge. Set via BUILD_COMMIT env var
	// (overwritten by -ldflags="-X main.CommitHash").
	BuildCommit string

	Host     string
	Port     int
	LogLevel string
	Dev      bool

	DBPath        string
	DataDir       string
	EncryptionKey string

	// AdminToken, when non-empty, unlocks the admin endpoints (e.g. the
	// Todo "clear all" via token). Loaded from the age-decrypted
	// secrets file, NOT from the host environment directly. This is the
	// canonical example of "use a real secret in the demo app" — see
	// README's "Admin unlock" section.
	AdminToken string

	NATS struct {
		Enabled  bool
		StoreDir string
		// LeafNodeURL, when set, makes this instance a NATS Leaf Node that
		// syncs with a central NATS server (e.g. the demo server). Used by
		// the desktop/mobile edge to replicate JetStream streams offline
		// and replay on reconnect. Empty = standalone embedded NATS.
		LeafNodeURL string
	}

	// DagNats holds the DagNats durable-workflow engine settings. It is
	// always compiled; set DAGNATS_ENABLED=false to no-op it at runtime.
	// DagNats reuses the embedded NATS JetStream that the realtime build
	// already starts, so it needs no extra infra.
	DagNats struct {
		Enabled  bool
		HTTPAddr string // HTTP/API/console listen addr (separate port from the app)
		NATSPort int    // NATS port the engine owns (default 4222; the realtime broadcaster connects here)
		StoreDir string
	}

	// OfflineSync controls the hybrid offline-sync-online strategy.
	// When enabled (default), the system works offline and syncs when
	// online via Service Worker (web) + NATS CRUD proxy (desktop/edge).
	// When disabled, all requests go directly to PocketBase with no
	// offline caching or background sync — the simplest path for
	// always-online deployments.
	//
	// Toggle via OFFLINE_SYNC_ENABLED=false in the environment.
	// Default: true (opt-out). When disabled, no Service Worker is
	// registered, no NATS CRUD stream is created, and no unnecessary
	// code paths are traversed.
	OfflineSync struct {
		Enabled bool
	}

	// EntityStore selects the persistence strategy for todos (and
	// future domain entities). "pb" (default) uses PocketBase records
	// + the OnRecordCreateRequest hook for offline-replay dedup.
	// "crdt" uses Loro CRDTs per owner + a snapshot to PB; trade-off
	// See ARCHITECTURE.md (Phase 2: JetStream op transport).
	// is the only way to get multi-instance sync on crdt.
	EntityStore string

	// Skin selects the active UI skin. Default "daisyui" (the template's
	// core theme). Other options: "basecoat" (shadcn-style) and "morpheus"
	// (web components, pre-alpha). The skin is resolved at runtime by the
	// skin dispatcher in web/skins/. All skins are compiled into the
	// binary; there is no build-tag selection.
	Skin string

	GoAI GoAIConfig

	// Credits gates the AI credits + BYOK plugin (lib calionauta/ai-credits).
	// When Credits.Enabled is false the plugin registers no routes and
	// credits.New is never built — the binary runs billing-free.
	Credits CreditsConfig
}

// CreditsConfig holds the env-driven knobs for the AI credits plugin. Every
// field defaults to billing-off; the plugin is only live when Enabled is
// true AND the lib's schema can be applied to the app PocketBase SQLite.
type CreditsConfig struct {
	Enabled             bool
	EncKey              []byte // CREDITS_ENC_KEY, 32 bytes; empty = BYOK store disabled
	DefaultMode         string // managed | byok | explicit
	MonthlyCredits      int64
	Model               string            // default managed model id (fallback GOAI_MODEL)
	PricingFile         string            // optional JSON prices file; empty = lib defaults
	ByokProviders       map[string]string // provider -> OpenAI-compatible base URL
	StripeSecretKey     string
	StripeWebhookSecret string
}

// GoAIConfig holds the LLM client settings. Currently just an
// APIKey + a model; expanded in internal/llm as more knobs
// (GOAI_BASE_URL, GOAI_MODEL, etc.) are read from env.
type GoAIConfig struct {
	APIKey string
}

// Load builds the Config. Order matters: secrets must be decrypted
// BEFORE reading the rest of the env so admin/LLM/NATS values can
// come from the encrypted file.
// cached is the global config singleton, loaded once.
var (
	cached *Config
	once   sync.Once
)

// Get returns the cached config singleton, loading it on first call.
func Get() *Config {
	once.Do(func() {
		cached = Load()
	})
	return cached
}

func Load() *Config {
	appName := os.Getenv("APP_NAME")
	if appName == "" {
		appName = defaultAppName()
	}

	// Decrypt ~/.secrets/<appName>.env.age into the process env. Silent
	// skip when AGE_SECRET_KEY or the secrets file is missing.
	secrets.Load(appName)

	dev := os.Getenv("ENVIRONMENT") != "production"

	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			log.Printf("config: invalid PORT=%q, using %d: %v", p, port, err)
		} else {
			port = parsed
		}
	}

	cfg := &Config{
		AppName:       appName,
		BuildLabel:    getEnv("BUILD_LABEL", "dev"),
		BuildCommit:   getEnv("BUILD_COMMIT", ""),
		Host:          getEnv("HOST", "0.0.0.0"),
		Port:          port,
		LogLevel:      getEnv("LOG_LEVEL", "INFO"),
		Dev:           dev,
		DBPath:        getEnv("DATABASE_PATH", "data/data.db"),
		DataDir:       getEnv("DATA_DIR", "data"),
		EncryptionKey: os.Getenv("ENCRYPTION_KEY"),
		AdminToken:    os.Getenv("ADMIN_UNLOCK_TOKEN"),
		GoAI: GoAIConfig{
			APIKey: os.Getenv("GOAI_API_KEY"),
		},
	}

	cfg.NATS.Enabled = envBool("NATS_ENABLED", defaultNATSEnabled())
	cfg.NATS.StoreDir = getEnv("NATS_STORE_DIR", "data/nats")
	cfg.NATS.LeafNodeURL = getEnv("NATS_LEAFNODE_URL", "")

	cfg.DagNats.Enabled = envBool("DAGNATS_ENABLED", defaultDagNatsEnabled())
	cfg.DagNats.HTTPAddr = getEnv("DAGNATS_HTTP_ADDR", "127.0.0.1:8090")
	cfg.DagNats.NATSPort = envInt("DAGNATS_NATS_PORT", defaultDagNatsNATSPort)
	cfg.DagNats.StoreDir = getEnv("DAGNATS_STORE_DIR", "data/dagnats")

	cfg.OfflineSync.Enabled = envBool("OFFLINE_SYNC_ENABLED", true)
	cfg.EntityStore = getEnv("ENTITY_STORE", "pb")
	cfg.Skin = getEnv("UI_SKIN", "daisyui")

	cfg.Credits.Enabled = envBool("CREDITS_ENABLED", false)
	cfg.Credits.DefaultMode = getEnv("CREDITS_DEFAULT_MODE", "explicit")
	cfg.Credits.MonthlyCredits = int64(envInt("CREDITS_MONTHLY_CREDITS", 0))
	cfg.Credits.PricingFile = getEnv("CREDITS_PRICING_FILE", "")
	cfg.Credits.Model = getEnv("CREDITS_MODEL", getEnv("GOAI_MODEL", "gpt-4o-mini"))
	cfg.Credits.ByokProviders = parseProviders(getEnv("BYOK_PROVIDERS", ""))
	if k := os.Getenv("CREDITS_ENC_KEY"); k != "" {
		cfg.Credits.EncKey = parseCreditsEncKey(k)
	}
	cfg.Credits.StripeSecretKey = os.Getenv("STRIPE_SECRET_KEY")
	cfg.Credits.StripeWebhookSecret = os.Getenv("STRIPE_WEBHOOK_SECRET")

	return cfg
}

const creditsEncKeyBytes = 32

// parseCreditsEncKey accepts the normal secret-manager representation (64 hex
// chars for 32 key bytes) while retaining support for a legacy raw 32-byte
// value. Invalid values remain invalid and are rejected fail-closed by the
// credits plugin when a BYOK provider is configured.
func parseCreditsEncKey(raw string) []byte {
	if len(raw) == hex.EncodedLen(creditsEncKeyBytes) {
		if key, err := hex.DecodeString(raw); err == nil {
			return key
		}
	}
	return []byte(raw)
}

// parseProviders parses the BYOK_PROVIDERS env var
// ("openai=https://api.openai.com/v1,anthropic=https://api.anthropic.com/v1")
// into a provider->base URL map. Malformed entries are dropped.
func parseProviders(raw string) map[string]string {
	out := map[string]string{}
	for part := range strings.SplitSeq(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool reads a boolean env var, falling back to def when unset or
// unparseable. The fallback lets a config file or env var (e.g.
// NATS_ENABLED=false) override the default at runtime; every feature is
// always compiled, so there is no build tag to flip.
func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// envInt reads an integer env var, falling back to def when unset or
// unparseable.
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// defaultDagNatsNATSPort is the conventional NATS port the DagNats engine
// owns. The realtime broadcaster connects here (single-NATS convention).
const defaultDagNatsNATSPort = 4222

// defaultAppName falls back to the binary name when APP_NAME is unset
// so the secrets file scope tracks whatever the project owner actually
// compiled. Uses os.Args[0] (binary path) trimmed to base name; if that
// fails (e.g. tests), it returns a hard-coded stable name.
func defaultAppName() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "gogogo-fullstack-template"
	}
	base := exe
	for i := len(exe) - 1; i >= 0; i-- {
		if exe[i] == '/' {
			base = exe[i+1:]
			break
		}
	}
	if base == "" {
		return "gogogo-fullstack-template"
	}
	return base
}
