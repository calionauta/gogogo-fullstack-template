package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/calionauta/gogogo-template/config"
	"github.com/calionauta/gogogo-template/internal/queue"
)

func TestNew_StoresAllFields(t *testing.T) {
	cfg := &config.Config{Host: "0.0.0.0", Port: 8080}
	q := &queue.Queue{} // zero-value: enough for the struct test, no actual queue ops
	llm := New(cfg, q)

	if llm.Cfg != cfg {
		t.Errorf("Cfg not stored: got %p want %p", llm.Cfg, cfg)
	}
	if llm.Queue != q {
		t.Errorf("Queue not stored: got %p want %p", llm.Queue, q)
	}
	if llm.LLM == nil {
		t.Errorf("LLM client should be constructed (even if unconfigured)")
	}
}

func TestNew_LLMClientCreatedWithoutAPIKey(t *testing.T) {
	// Without an API key, llm.New returns a non-nil client whose
	// Configured() returns false. Handlers must check before calling.
	cfg := &config.Config{} // GoAI.APIKey is the zero value ""
	ctx := New(cfg, &queue.Queue{})

	if ctx.LLM == nil {
		t.Fatal("LLM client should be non-nil even without API key")
	}
	if ctx.LLM.Configured() {
		t.Errorf("LLM.Configured() = true, want false (no API key)")
	}
}

// TestLogStartupSummary_OmitsSecrets asserts the startup log line
// never includes the LLM API key or admin token value — only
// booleans and host/port/data_dir. Operators can safely pipe
// startup logs to a log aggregation service.
func TestLogStartupSummary_OmitsSecrets(t *testing.T) {
	cfg := &config.Config{
		Host:       "127.0.0.1",
		Port:       9000,
		DataDir:    "/var/data/gogogo",
		AdminToken: "super-secret-admin-token",
		GoAI:       config.GoAIConfig{APIKey: "sk-live-THIS-SHOULD-NEVER-LEAK"},
	}
	ctx := New(cfg, &queue.Queue{})

	// Capture slog output via a JSON handler on a buffer.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx.LogStartupSummary()

	out := buf.String()
	// Parse the JSON line to assert exact field presence/absence.
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("slog output not valid JSON: %v\nraw: %s", err, out)
	}
	// Required fields present.
	for _, want := range []string{"host", "port", "llm_configured", "admin_token_set", "data_dir"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing log field %q in: %s", want, out)
		}
	}
	// Sensitive values must NOT appear in the line.
	if strings.Contains(out, "super-secret-admin-token") {
		t.Errorf("admin token leaked in startup log:\n%s", out)
	}
	if strings.Contains(out, "sk-live-THIS-SHOULD-NEVER-LEAK") {
		t.Errorf("LLM API key leaked in startup log:\n%s", out)
	}
}
