// SCOPE:core - DO NOT REMOVE - Server configuration (env vars, secrets).
//
// ── Env vars (all optional, see Load() for defaults) ──
//
//	PORT                (default: 8080)          — HTTP listen port
//	HOST                (default: "0.0.0.0")     — HTTP listen address
//	ENVIRONMENT         (default: "development") — set to "production" for prod mode
//	APP_NAME            (default: binary name)   — project name (secrets scope)
//	LOG_LEVEL           (default: "INFO")
//	DATABASE_PATH       (default: "data/app.db")
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
//	DAGNATS_STORE_DIR   (default: "data/dagnats")
//	OFFLINE_SYNC_ENABLED (default: true) — toggle hybrid offline sync
//	ENTITY_STORE         (default: "pb") — todo persistence strategy
//	                       (see features/store/store.go)
//
// ── Runtime constants (tune in config.go, consumed across packages) ──
//
//	DefaultReplayBufferSize     (64)  — SSEHub per-client ring-buffer
//	DefaultClientQueueSize      (64)  — SSEHub per-client channel buffer
//	DefaultSSEHeartbeatInterval (15s) — SSE heartbeat ticker interval
package config

import (
	"log"
	"os"
	"strconv"
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

	// DagNats holds the DagNats durable-workflow engine settings. Built
	// with -tags dagnats. DagNats reuses the embedded NATS JetStream that
	// the jetstream build already starts, so it needs no extra infra.
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
	// in docs/decisions.md v0.20.0 ADR. Phase 2 (JetStream op transport)
	// is the only way to get multi-instance sync on crdt.
	EntityStore string

	GoAI GoAIConfig
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
		Host:          getEnv("HOST", "0.0.0.0"),
		Port:          port,
		LogLevel:      getEnv("LOG_LEVEL", "INFO"),
		Dev:           dev,
		DBPath:        getEnv("DATABASE_PATH", "data/app.db"),
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

	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool reads a boolean env var, falling back to def when unset or
// unparseable. This lets a build tag supply the default (e.g. -tags
// jetstream implies NATS on) while still allowing an explicit override
// via the env var (NATS_ENABLED=false).
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
