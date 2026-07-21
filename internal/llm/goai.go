// SCOPE:layer=infra,removal=plugin — GoAI LLM client (used by Suggest)
// To remove: delete internal/llm/ + features/todo/handlers/llm_suggest.go.
// Package llm wires the GoAI SDK (github.com/zendev-sh/goai) into the
// gogogo-fullstack-template template. The Client is configured via env so the
// same binary can talk to any OpenAI-compatible provider: Groq,
// OpenRouter, Together, Cloudflare, Ollama (via OpenAI-compat shim),
// or a self-hosted vLLM.
//
// Environment variables (all read once at New()):
//
//	GOAI_API_KEY   (required)   API key for the provider. No key = no client.
//	GOAI_BASE_URL  (recommended) Base URL with the /v1 suffix, e.g.
//	                            https://api.groq.com/openai/v1
//	                            https://openrouter.ai/api/v1
//	                            https://api.together.xyz/v1
//	                            https://api.openai.com/v1
//	GOAI_MODEL     (default: gpt-4o-mini) Model name as the provider
//	                                         knows it (e.g. llama-3.3-70b-versatile
//	                                         for Groq, or gpt-4o-mini for OpenAI).
//
// Use compat.WithBaseURL(...) so the same client works with every
// OpenAI-compatible provider. We do NOT hardcode a provider in the
// template; the project owner picks.
package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/compat"

	"github.com/calionauta/gogogo-fullstack-template/internal/llm/fakeserver"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// ErrNoAPIKey is returned by Chat/ChatStream when the client was built
// without an API key. The HTTP handler should surface this as a
// user-facing 503 with a clear "configure GOAI_API_KEY" message.
var ErrNoAPIKey = errors.New("llm: GOAI_API_KEY is not configured")

// DefaultBaseURL is the OpenAI public API. The template ships with
// OpenAI as a fallback so New() never returns nil; users override
// with GOAI_BASE_URL for Groq/OpenRouter/etc.
const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultModel is a small, fast, cheap model. Override per-provider
// (e.g. Groq's llama-3.3-70b-versatile is much stronger for the same
// cost on Groq's free tier).
const DefaultModel = "gpt-4o-mini"

// Client is a thin wrapper around the GoAI SDK with project-level
// retry + SSE-friendly streaming. Constructed once at startup
// (config.Load) and shared across handlers.
type Client struct {
	apiKey  string
	baseURL string
	modelID string

	// retry is the project-standard retry config. Same backoff used
	// by the queue workers; tuned for transient LLM API hiccups
	// (5xx, 429) rather than logical errors.
	retry queue.RetryConfig

	// server is the in-process fake LLM used by NewSimulated. Nil for
	// real clients. Closed via Close().
	server *fakeserver.FakeServer
}

// New builds a Client from env. If apiKey is empty, New still
// returns a usable Client whose Chat/ChatStream return ErrNoAPIKey —
// the rest of the app can detect this and surface a clean error.
func New(apiKey string) *Client {
	baseURL := os.Getenv("GOAI_BASE_URL")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	modelID := os.Getenv("GOAI_MODEL")
	if modelID == "" {
		modelID = DefaultModel
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		modelID: modelID,
		retry: queue.RetryConfig{
			Attempts: 3,
			Delay:    500 * time.Millisecond,
			MaxDelay: 5 * time.Second,
		},
	}
}

// simulatedResponseDelay is the deliberate per-call delay the in-process
// fake LLM adds to the second (successful) request. Long enough for
// the user to see the "got 3 suggestions" toast arrive AFTER the
// "attempt failed, retrying…" toast in the demo; short enough that
// the demo doesn't drag.
const simulatedResponseDelay = 1500 * time.Millisecond

// NewSimulated returns a Client wired to an in-process fake LLM server
// that scripts error → retry → slow response, so the queue + retry demo
// works without a real API key. The fake returns a 500 on the first call
// (triggering a worker retry with visible SSE feedback), then 200 on the
// second call after a short delay. The client's own retry is disabled
// (Attempts: 1) so the QUEUE WORKER is the unit that retries and streams
// per-attempt toasts — that's the behavior we want to showcase.
//
// The caller owns the lifecycle; call Close() to shut the fake server down.
func NewSimulated() *Client {
	srv := fakeserver.NewServer(
		fakeserver.WithStatusSequence(500, 200),
		fakeserver.WithResponseDelay(simulatedResponseDelay),
		fakeserver.WithResponse(`["Review the pull request","Write the meeting notes","Ping the on-call engineer"]`),
	)
	return &Client{
		apiKey:  "simulated",
		baseURL: srv.URL,
		modelID: "simulated-model",
		retry: queue.RetryConfig{
			Attempts: 1,
			Delay:    100 * time.Millisecond,
			MaxDelay: time.Second,
		},
		server: srv,
	}
}

// Close shuts down the in-process fake LLM server, if any. Safe to call
// on any Client (no-op for real clients).
func (c *Client) Close() {
	if c.server != nil {
		c.server.Close()
	}
}

// Configured reports whether the client has the minimum env to call
// the provider. Use this from HTTP handlers to skip the route when
// the user hasn't set a key yet.
func (c *Client) Configured() bool { return c.apiKey != "" }

// ModelID returns the configured model name (for UI/debugging).
func (c *Client) ModelID() string { return c.modelID }

// BaseURL returns the configured base URL (for UI/debugging).
func (c *Client) BaseURL() string { return c.baseURL }

// model builds a GoAI language model via the compat (OpenAI-compatible)
// provider. Returns nil if no key is set.
func (c *Client) model() provider.LanguageModel {
	if c.apiKey == "" {
		return nil
	}
	return compat.Chat(
		c.modelID,
		compat.WithBaseURL(c.baseURL),
		compat.WithAPIKey(c.apiKey),
	)
}

// Chat calls the LLM with project-level exponential backoff. Returns
// ErrNoAPIKey if the client was built without a key (no retries — the
// configuration problem won't fix itself on retry).
func (c *Client) Chat(ctx context.Context, prompt string) (string, error) {
	if c.apiKey == "" {
		return "", ErrNoAPIKey
	}
	m := c.model()
	if m == nil {
		return "", ErrNoAPIKey
	}

	var result string
	err := c.retry.Do(ctx, nil, "", "llm.chat", func() error {
		// Disable goai's internal retry: our c.retry (and, for queued
		// work, the worker's retry) is the single retry layer. If goai
		// retried here it would absorb transient 5xx before our retry
		// policy / SSE feedback ever saw them.
		r, genErr := goai.GenerateText(ctx, m, goai.WithPrompt(prompt), goai.WithMaxRetries(0))
		if genErr != nil {
			return fmt.Errorf("llm: generate: %w", genErr)
		}
		result = r.Text
		return nil
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// ChatSuggest is a higher-level helper for the "AI suggest next todo"
// feature: sends the partial title as a prompt that asks the model
// for 3 short completions, returns them as a string slice. Same
// safeSeedTasks is a curated pool of short, safe, actionable todo items.
// Every entry starts with an infinitive/imperative verb and is
// non-violent, non-harmful, and non-prejudiced. ChatSuggest falls back to
// these when the user supplies no seed text, so the one-click "AI Suggest"
// still produces useful output offline or without an API key.
var safeSeedTasks = []string{
	"Read the project README",
	"Write the meeting notes",
	"Review the pull request",
	"Plan the sprint backlog",
	"Drink a glass of water",
	"Stretch for five minutes",
	"Reply to the customer email",
	"Update the changelog",
	"Refactor the helper function",
	"Sketch the architecture diagram",
	"Test the login flow",
	"Backup the database",
	"Organize the download folder",
	"Schedule the standup",
	"Learn one new keyboard shortcut",
	"Clean up the todo list",
	"Prepare the demo script",
	"Verify the deployment",
	"Write a unit test",
	"Take a short walk",
	"Brainstorm feature names",
	"Summarize the article",
	"Fix the failing CI job",
	"Message the on-call engineer",
}

// randomTasks returns 3 distinct safe seed tasks in random order.
func randomTasks() []string {
	idx := rand.Perm(len(safeSeedTasks))
	out := make([]string, 0, 3)
	for _, i := range idx[:3] {
		out = append(out, safeSeedTasks[i])
	}
	return out
}

// ChatSuggest returns 3 short, distinct, actionable todo suggestions. If
// partial is empty it does NOT error: it returns a few random safe tasks
// instead, so the user is never forced to type seed text first (and the
// feature works offline / without an API key).
//
// When partial is non-empty it asks the LLM for completions, with the same
// retry semantics as Chat. Empty slice + nil error on "I don't know".
func (c *Client) ChatSuggest(ctx context.Context, partial string) ([]string, error) {
	if partial == "" {
		return randomTasks(), nil
	}
	prompt := fmt.Sprintf(
		"You are helping a user write a todo list item. The user has typed %q so far. "+
			"Suggest exactly 3 short, distinct, actionable completions (each under 60 characters). "+
			"Return them as a JSON array of strings, no other text, no markdown fences.",
		partial,
	)
	text, err := c.Chat(ctx, prompt)
	if err != nil {
		return nil, err
	}
	// The model is told to return a JSON array. Parse defensively.
	out, parseErr := parseStringArray(text)
	if parseErr != nil {
		slog.Warn("llm: suggest returned non-JSON; using raw", "raw", text, "err", parseErr)
		// Fall back to splitting on newlines so the UI still gets
		// something to show.
		return splitLines(text), nil
	}
	return out, nil
}

// MustConfigured returns nil if the client has an API key, else
// returns a descriptive error suitable for surfacing in HTTP 503.
func (c *Client) MustConfigured() error {
	if c.Configured() {
		return nil
	}
	const setKeyHint = "Set GOAI_API_KEY in your environment or in the " +
		"age-encrypted secrets file (run bin/init-secrets)"
	return fmt.Errorf("%w. %s", ErrNoAPIKey, setKeyHint)
}

// ChatStream pipes tokens from the LLM to fn as they arrive. Same
// retry as Chat. Used by StreamToSSE for Datastar token-by-token
// rendering. Returns ErrNoAPIKey if not configured.
func (c *Client) ChatStream(ctx context.Context, prompt string, fn func(string) error) error {
	if c.apiKey == "" {
		return ErrNoAPIKey
	}
	m := c.model()
	if m == nil {
		return ErrNoAPIKey
	}
	return c.retry.DoSilent(ctx, func() error {
		// See Chat: goai's internal retry is disabled so our retry
		// policy is the only one in play.
		stream, err := goai.StreamText(ctx, m, goai.WithPrompt(prompt), goai.WithMaxRetries(0))
		if err != nil {
			return fmt.Errorf("llm: stream: %w", err)
		}
		for text := range stream.TextStream() {
			if cbErr := fn(text); cbErr != nil {
				return cbErr
			}
		}
		// Drain the stream and surface any deferred error. No Close()
		// method exists; the doc says consume the channel or cancel
		// the context, both of which we just did.
		if err := stream.Err(); err != nil {
			return fmt.Errorf("llm: stream: %w", err)
		}
		return nil
	})
}
