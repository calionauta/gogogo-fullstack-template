// SCOPE:layer=feature,removal=feature — Auth-gated read-only /config view (masked secrets)
//
// Package config exposes a SAFE view of the running server
// configuration: every secret-shaped field is masked before the
// template renders. The masking rules below are deliberately
// narrow — the goal is "what is configured?" without "what is the
// value?". Internal users with shell access can read process env
// directly; this view is the convenience panel for browsers.
package config

import (
	"net/url"
	"unicode/utf8"
)

// Mask thresholds. Pulled out as constants so the policy is
// reviewable in one place — changes to a single number here change
// the behaviour across every field that uses the helper.
const (
	maskShortMaxLen = 8  // < this → "***"
	maskMidMaxLen   = 16 // 8-16 → first2 + "***" + last2; > 16 → first4 + "…" + last2

	// Group names surfaced by BuildPageData. Centralised as
	// constants so goconst stops complaining and a rename touches
	// one site per group.
	groupApp     = "app"
	groupStorage = "storage"
	groupAdmin   = "admin"
	groupAI      = "ai"
	groupNATS    = "nats"
	groupDagNats = "dagnats"
	groupSync    = "sync"
	groupSecrets = "secrets"

	// Field keys used 3+ times across mask.go + safe_view.go.
	// Centralised so goconst stops flagging the same string in
	// each map literal / row() argument.
	keyAppName     = "AppName"
	keyBuildLabel  = "BuildLabel"
	keyBuildCommit = "BuildCommit"
	keyDBPath      = "DBPath"
	keyDataDir     = "DataDir"
)

// masked returns a partial-mask of v suitable for display. The
// chosen thresholds prevent short tokens from being identifiable
// by length alone. The first 2 / first 4 chars are useful for
// humans to recognise the value family (e.g. "sk-" prefix for
// OpenAI keys) while not leaking enough material to brute-force.
func masked(v string) string {
	if v == "" {
		return ""
	}
	if !utf8.ValidString(v) {
		return "***"
	}
	n := len(v)
	switch {
	case n < maskShortMaxLen:
		return "***"
	case n <= maskMidMaxLen:
		return v[:2] + "***" + v[n-2:]
	default:
		return v[:4] + "…" + v[n-2:]
	}
}

// maskedURL masks credentials embedded in URLs — e.g.
// "nats://user:secret@host:4222" → "nats://***@host:4222".
// Returns the original string unchanged when url.Parse fails (we
// only redact what we can recognise). Strips userinfo entirely
// (both username and password) so the operator sees only the
// scheme://host:port the leaf node connects to.
func maskedURL(v string) string {
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.User == nil {
		return v
	}
	u.User = url.User("***")
	return u.String()
}

// envMap is the canonical env-var-name lookup. Using a map keeps
// envFromKey out of golangci-lint's cyclomatic-complexity budget
// while letting the operator correlate a /config row with their
// .env file (or container env) without us dumping the values.
//
// Group keys come from safe_view.go's Row.Group; row keys are the
// Go field name as displayed in the template.
var envMap = map[string]map[string]string{
	groupApp: {
		keyAppName:     "APP_NAME",
		keyBuildLabel:  "BUILD_LABEL",
		keyBuildCommit: "BUILD_COMMIT",
		"Host":         "HOST",
		"Port":         "PORT",
		"LogLevel":     "LOG_LEVEL",
		"Dev":          "ENVIRONMENT",
	},
	groupStorage: {
		keyDBPath:       "DATABASE_PATH",
		keyDataDir:      "DATA_DIR",
		"EncryptionKey": "ENCRYPTION_KEY",
	},
	groupAdmin: {
		"AdminToken": "ADMIN_UNLOCK_TOKEN",
	},
	groupAI: { //nolint:gosec // map of field-name → env-name, NOT credentials
		"APIKey":  "GOAI_API_KEY",
		"BaseURL": "GOAI_BASE_URL",
		"Model":   "GOAI_MODEL",
	},
	groupNATS: {
		"Enabled":     "NATS_ENABLED",
		"StoreDir":    "NATS_STORE_DIR",
		"LeafNodeURL": "NATS_LEAFNODE_URL",
	},
	groupDagNats: {
		"Enabled":  "DAGNATS_ENABLED",
		"HTTPAddr": "DAGNATS_HTTP_ADDR",
		"NATSPort": "DAGNATS_NATS_PORT",
		"StoreDir": "DAGNATS_STORE_DIR",
	},
	groupSync: {
		"OfflineEnabled": "OFFLINE_SYNC_ENABLED",
		"EntityStore":    "ENTITY_STORE",
	},
	groupSecrets: {
		"AGESecretKey": "AGE_SECRET_KEY",
	},
}

// envFromKey returns the canonical env-var name for a key, or an
// empty string when unknown. See envMap for the table.
func envFromKey(group, key string) string {
	if g, ok := envMap[group]; ok {
		return g[key]
	}
	return ""
}

// safeValue flags whether the key in a group should be shown
// raw. Mirrors envMap: anything token-shaped, URL-with-creds, or
// filesystem-path that reveals /var/lib/... is hidden behind the
// "set / not-set" boolean.
//
// (storage.DBPath and storage.DataDir reveal deployment paths,
// not secrets — they are surfaced. NATS.LeafNodeURL carries
// credentials → masked.)
func safeValue(group, key string) bool {
	switch group {
	case groupApp:
		return true
	case groupStorage:
		return key == keyDBPath || key == keyDataDir
	case groupAdmin:
		return false
	case groupAI:
		return key == "BaseURL" || key == "Model"
	case groupNATS:
		return key != "LeafNodeURL"
	case groupDagNats:
		return true
	case groupSync:
		return true
	case groupSecrets:
		return false
	}
	return false
}

// maskValue returns the display-friendly version of v based on the
// group/key policy. Empty values are returned as empty so the
// template renders "not set" cleanly. Strings whose group/key is
// unknown are passed through so a future field addition doesn't
// silently drop data.
func maskValue(group, key, v string) string {
	if v == "" {
		return ""
	}
	if !safeValue(group, key) {
		return masked(v)
	}
	return v
}
