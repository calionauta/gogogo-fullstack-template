package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

func TestNew_DefaultsToOpenAI(t *testing.T) {
	c := New("")
	if c.Configured() {
		t.Fatal("empty API key should not be configured")
	}
	if c.BaseURL() != DefaultBaseURL {
		t.Fatalf("default base URL = %q, want %q", c.BaseURL(), DefaultBaseURL)
	}
	if c.ModelID() != DefaultModel {
		t.Fatalf("default model = %q, want %q", c.ModelID(), DefaultModel)
	}
}

func TestChat_NoAPIKey_ReturnsErrNoAPIKey(t *testing.T) {
	c := New("")
	_, err := c.Chat(context.Background(), "hello")
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("expected ErrNoAPIKey, got %v", err)
	}
}

func TestMustConfigured_DescriptiveError(t *testing.T) {
	c := New("")
	err := c.MustConfigured()
	if err == nil {
		t.Fatal("expected error for unconfigured client")
	}
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("expected ErrNoAPIKey, got %v", err)
	}
	// The message should mention GOAI_API_KEY so the user knows what
	// to set.
	if !strings.Contains(err.Error(), "GOAI_API_KEY") {
		t.Fatalf("error message should mention GOAI_API_KEY, got %q", err.Error())
	}
}

func TestChatSuggest_EmptyPartial_ReturnsRandomTasks(t *testing.T) {
	// An empty partial must NOT error: ChatSuggest returns a few random
	// safe tasks so the one-click AI Suggest works without seed text (and
	// offline / without an API key).
	c := New("test-key")
	got, err := c.ChatSuggest(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error for empty partial: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 random tasks, got %d: %v", len(got), got)
	}
	seen := map[string]bool{}
	for _, s := range got {
		if s == "" {
			t.Fatalf("empty task in result: %v", got)
		}
		if seen[s] {
			t.Fatalf("duplicate task in result: %v", got)
		}
		seen[s] = true
	}
}

func TestParseStringArray(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", `["a","b","c"]`, []string{"a", "b", "c"}},
		{"with prose", `Here you go: ["x","y","z"] done.`, []string{"x", "y", "z"}},
		{"with markdown fence", "```json\n[\"p1\",\"p2\",\"p3\"]\n```", []string{"p1", "p2", "p3"}},
		{"cap at 3", `["a","b","c","d","e"]`, []string{"a", "b", "c"}},
		{"trimmed", `["  spaced  ","b"]`, []string{"spaced", "b"}},
		{"empty entries", `["a","","b"]`, []string{"a", "b"}},
		{"not array", "no array here", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStringArray(tc.in)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("idx %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestSplitLines(t *testing.T) {
	in := "- first\n- second\n- third"
	got := splitLines(in)
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("len: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("idx %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRetryConfig_UsedByChat verifies that the Client wires a
// RetryConfig with sane defaults. The retry itself can't be exercised
// in a unit test without a fake provider, so this just confirms
// the configuration is sensible (3 attempts, fast initial delay).
func TestRetryConfig_UsedByChat(t *testing.T) {
	c := New("test-key")
	if c.retry.Attempts != 3 {
		t.Fatalf("default attempts = %d, want 3", c.retry.Attempts)
	}
	if c.retry.Delay > time.Second {
		t.Fatalf("default delay too long for tests: %v", c.retry.Delay)
	}
	if c.retry.MaxDelay < c.retry.Delay {
		t.Fatalf("MaxDelay %v < Delay %v", c.retry.MaxDelay, c.retry.Delay)
	}
}

// TestQueueRetryDo_RunsFunctionAtLeastOnce is a sanity check on the
// underlying retry primitive the LLM client depends on.
func TestQueueRetryDo_RunsFunctionAtLeastOnce(t *testing.T) {
	cfg := queue.RetryConfig{
		Attempts: 2,
		Delay:    time.Millisecond,
		MaxDelay: 10 * time.Millisecond,
	}
	calls := 0
	err := cfg.DoSilent(context.Background(), func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call on success, got %d", calls)
	}
}
