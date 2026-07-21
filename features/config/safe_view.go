// SCOPE:layer=feature,removal=feature — Auth-gated read-only /config view (masked secrets)
//
// SafeView builds the read-only data shape that the /config page
// renders. Field-group boundaries in the schema mirror the
// top-level sub-structs of *config.Config so operators can correlate
// "what's set in .env" with "what's wired in code" at a glance.
//
// Two safety rules govern the output:
//   - Secret-shaped fields (Token, Key, URL-with-creds) only ever
//     surface as masked — never the literal value.
//   - AGE_SECRET_KEY is excluded entirely; we render a boolean only.
//     Any agent running with shell access can `env | grep AGE_`, but
//     we deliberately don't pipe it to a browser.
package config

import (
	"os"
	"strconv"

	"github.com/calionauta/gogogo-fullstack-template/config"
)

// ageSecretKey is the constant field name shown on the /config
// page for the master age-decryption key (boolean-only).
//
// Note: group-name constants (groupApp, groupStorage, …) live in
// mask.go where they back envMap; the same package, one block.
const (
	ageSecretKey = "AGESecretKey"
	secretsGroup = "Secrets"
)

// Row is one entry on the /config page: a label, the env var name
// the operator can grep, and a Display variant that the templ
// layer switches on.
type Row struct {
	Group       string
	Key         string // field name as seen in code, e.g. "APIKey"
	Env         string // env var name, e.g. "GOAI_API_KEY"
	Display     string // value to render when group/key policy allows
	Masked      bool   // true when Display is a partial mask
	BoolOnly    bool   // true for fields like AGE_SECRET_KEY (no value at all)
	Set         bool   // true when the underlying env-backed string was non-empty
	Description string // one-liner, sourced from config.go's header comment
	URLShape    bool   // true when Display is a URL we trust (LeafNodeURL after masking)
}

// PageData holds the rows grouped for rendering. Strings are
// pre-grouped so the .templ stays presentation-only.
type PageData struct {
	AppName    string
	BuildLabel string
	Groups     []Group
}

// Group is a labelled slice of Rows. Order is stable across
// renders so operators see the same ordering they know from the
// file tree.
type Group struct {
	Name string
	Rows []Row
	Note string // optional HTML-safe note (e.g. sensitivity caveat)
}

// BuildPageData reads the runtime *config.Config and produces a
// redacted view. SAFE — never returns the original value of a
// masked field. The function is pure: no DB, no side effects.
//
// AGE_SECRET_KEY is read from the process env (it never lands in
// the config.Config struct, only in the secrets loader). We surface
// its set-state but never its value.
func BuildPageData(cfg *config.Config) PageData {
	if cfg == nil {
		return PageData{}
	}

	pd := PageData{
		AppName:    cfg.AppName,
		BuildLabel: cfg.BuildLabel,
	}

	ageSet := os.Getenv("AGE_SECRET_KEY") != ""

	// App group — never secret. Direct passthrough.
	pd.Groups = append(pd.Groups, Group{
		Name: "App",
		Rows: []Row{
			row("app", keyAppName, cfg.AppName, "Project name; used as the secrets-file scope."),
			row("app", keyBuildLabel, cfg.BuildLabel, "Build identifier. 'dev' for go run."),
			row("app", keyBuildCommit, cfg.BuildCommit, "Short git commit hash."),
			row("app", "Host", cfg.Host, "HTTP listen address."),
			row("app", "Port", intToStr(cfg.Port), "HTTP listen port."),
			row("app", "LogLevel", cfg.LogLevel, "log/slog level threshold."),
			rowBool("app", "Dev", cfg.Dev, "True when ENVIRONMENT != 'production'."),
		},
	})

	// Storage group — paths surfaced, encryption key masked.
	pd.Groups = append(pd.Groups, Group{
		Name: "Storage",
		Rows: []Row{
			row("storage", keyDBPath, cfg.DBPath, "SQLite file path used by PocketBase."),
			row("storage", keyDataDir, cfg.DataDir, "Directory used by goqite, NATS, and DagNats."),
			rowSecret("storage", "EncryptionKey", cfg.EncryptionKey, "PocketBase encryption key (masked)."),
		},
	})

	// Admin group — token always masked.
	pd.Groups = append(pd.Groups, Group{
		Name: "Admin",
		Rows: []Row{
			rowSecret("admin", "AdminToken", cfg.AdminToken, "Master token for admin endpoints (masked)."),
		},
		Note: "Loaded from the age-decrypted secrets file. Never from the host env directly.",
	})

	// AI group — base URL + model surfaced, API key masked.
	// BaseURL/Model are read directly by internal/llm/goai.go, not
	// stored on the Config struct; we read os.Getenv here so the
	// /config page stays a single source of truth from the user's
	// perspective.
	pd.Groups = append(pd.Groups, Group{
		Name: "AI (GoAI)",
		Rows: []Row{
			rowSecret("ai", "APIKey", cfg.GoAI.APIKey, "LLM provider API key (masked)."),
			row("ai", "BaseURL", os.Getenv("GOAI_BASE_URL"), "OpenAI-compatible base URL."),
			row("ai", "Model", os.Getenv("GOAI_MODEL"), "Model ID passed to the provider."),
		},
	})

	// NATS group.
	pd.Groups = append(pd.Groups, Group{
		Name: "NATS",
		Rows: []Row{
			rowBool("nats", "Enabled", cfg.NATS.Enabled, "Embedded NATS JetStream toggle."),
			row("nats", "StoreDir", cfg.NATS.StoreDir, "Where NATS persists streams."),
			rowURL(
				"nats", "LeafNodeURL", cfg.NATS.LeafNodeURL,
				"Connect as Leaf Node when set; credentials inside the URL are masked.",
			),
		},
	})

	// DagNats group.
	pd.Groups = append(pd.Groups, Group{
		Name: "DagNats",
		Rows: []Row{
			rowBool("dagnats", "Enabled", cfg.DagNats.Enabled, "Durable workflow engine."),
			row("dagnats", "HTTPAddr", cfg.DagNats.HTTPAddr, "Console/API listen (separate port)."),
			row("dagnats", "NATSPort", intToStr(cfg.DagNats.NATSPort), "NATS port the engine owns."),
			row("dagnats", "StoreDir", cfg.DagNats.StoreDir, "Where DagNats persists workflows."),
		},
	})

	// Sync group — operational.
	pd.Groups = append(pd.Groups, Group{
		Name: "Sync",
		Rows: []Row{
			rowBool("sync", "OfflineEnabled", cfg.OfflineSync.Enabled, "Service Worker + NATS CRUD proxy."),
			row("sync", "EntityStore", cfg.EntityStore, "Persistence strategy: 'pb' or 'crdt'."),
		},
	})

	// Secrets group — boolean-only. AGE_SECRET_KEY literally never
	// leaves the host process.
	pd.Groups = append(pd.Groups, Group{
		Name: secretsGroup,
		Rows: []Row{
			{
				Group:       groupSecrets,
				Key:         ageSecretKey,
				Env:         envFromKey(groupSecrets, ageSecretKey),
				BoolOnly:    true,
				Set:         ageSet,
				Description: "age-decryption master key for ~/.secrets/. Always boolean-only; the value never leaves the process.",
			},
		},
		Note: "The value of AGE_SECRET_KEY is not exposed by design. To rotate it, edit the file at ~/.secrets/<APP_NAME>.env.age.", //nolint:lll // single-line readability over column budget
	})

	return pd
}

// row builds a non-secret Row from a string value. The masking
// layer still runs (maskValue is a safe passthrough when
// safeValue==true) so future policy changes don't require touching
// every call site.
func row(group, key, value, desc string) Row {
	r := Row{
		Group:       group,
		Key:         key,
		Env:         envFromKey(group, key),
		Set:         value != "",
		Description: desc,
	}
	if value == "" {
		return r
	}
	r.Display = maskValue(group, key, value)
	return r
}

// rowSecret is the masked-only Row constructor. The Display is the
// partial mask, Masked=true. Empty values stay empty (so the
// template can render "not set" without "***").
func rowSecret(group, key, value, desc string) Row {
	r := Row{
		Group:       group,
		Key:         key,
		Env:         envFromKey(group, key),
		Set:         value != "",
		Description: desc,
	}
	if value == "" {
		return r
	}
	r.Masked = true
	r.Display = masked(value)
	return r
}

// rowURL is for the LeafNodeURL row only. It uses maskedURL so the
// scheme://host survives but credentials are stripped. Empty stays
// empty.
func rowURL(group, key, value, desc string) Row {
	r := Row{
		Group:       group,
		Key:         key,
		Env:         envFromKey(group, key),
		Set:         value != "",
		URLShape:    true,
		Description: desc,
	}
	if value == "" {
		return r
	}
	r.Masked = true
	r.Display = maskedURL(value)
	return r
}

// rowBool builds a "true/false — env name" row. Used for toggles
// where the value is its own meaning.
func rowBool(group, key string, value bool, desc string) Row {
	env := envFromKey(group, key)
	if value {
		return Row{Group: group, Key: key, Env: env, Set: true, Display: "true", Description: desc}
	}
	return Row{Group: group, Key: key, Env: env, Set: false, Display: "false", Description: desc}
}

// intToStr is a tiny convenience to keep the table builder terse.
// Empty when zero so the template can render "not set" cleanly.
func intToStr(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}
