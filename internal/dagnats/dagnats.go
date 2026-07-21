// SCOPE:layer=infra,removal=plugin — DagNats durable workflow engine
// To remove: delete internal/dagnats/, cmd/web/dagnats.go, router/onboarding_dagnats.go and dagnats_proxy_dagnats.go.
// Package dagnats is an HTTP client for the DagNats workflow engine.
// The server is booted in cmd/web/dagnats.go. DagNats is a DAG-based
// workflow engine built on NATS JetStream: workflows are declarative
// JSON, so refactoring handlers never breaks an in-flight workflow.
// It reuses the embedded NATS JetStream.
package dagnats

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a minimal HTTP client for the DagNats REST API. It registers
// workflow definitions and starts/signals/inspects runs. Keeping it tiny
// (net/http, no SDK dependency) means the app controls retry/timeout and
// error handling explicitly.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client targeting the DagNats API at baseURL
// (e.g. http://127.0.0.1:8090). A missing scheme is normalised to
// http:// so callers can pass the bare host:port from config
// (DAGNATS_HTTP_ADDR) without building invalid URLs downstream.
func NewClient(baseURL string) *Client {
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// RegisterWorkflow registers (or re-registers idempotently) a workflow
// definition. DagNats accepts re-registration of the same name+version, so
// calling this on every startup is safe and keeps the definition in sync
// with the binary without a migration step.
func (c *Client) RegisterWorkflow(ctx context.Context, def []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/workflows", bytes.NewReader(def))
	if err != nil {
		return fmt.Errorf("dagnats: build register request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dagnats: register workflow: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dagnats: register workflow: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// StartRun starts a workflow run and returns its run ID.
func (c *Client) StartRun(ctx context.Context, workflow string, input any) (string, error) {
	body, err := json.Marshal(map[string]any{"workflow": workflow, "input": input})
	if err != nil {
		return "", fmt.Errorf("dagnats: marshal start run: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/runs", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("dagnats: build start run: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("dagnats: start run: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dagnats: start run: status %d: %s", resp.StatusCode, string(respBody))
	}
	var result struct {
		//nolint:tagliatelle // DagNats API uses snake_case (run_id)
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("dagnats: unmarshal run response: %w", err)
	}
	if result.RunID == "" {
		return "", fmt.Errorf("dagnats: start run: empty run_id")
	}
	return result.RunID, nil
}

// Signal delivers a named signal to a run (used to resume a workflow that
// is waiting on an external event, e.g. the user's first todo).
func (c *Client) Signal(ctx context.Context, runID, name string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("dagnats: marshal signal: %w", err)
	}
	url := fmt.Sprintf("%s/runs/%s/signal/%s", c.baseURL, runID, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dagnats: build signal: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dagnats: signal: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dagnats: signal: status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// RunStatus is a trimmed view of a DagNats run used by the progress poller.
//
//nolint:tagliatelle // DagNats API uses snake_case (run_id), keep matching the wire format.
type RunStatus struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	Step   int    `json:"step"`
	Total  int    `json:"total"`
	Detail string `json:"detail"`
}

// GetRun fetches the current status of a run.
func (c *Client) GetRun(ctx context.Context, runID string) (*RunStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/runs/"+runID, nil)
	if err != nil {
		return nil, fmt.Errorf("dagnats: build get run: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dagnats: get run: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dagnats: get run: status %d: %s", resp.StatusCode, string(respBody))
	}
	var st RunStatus
	if err := json.Unmarshal(respBody, &st); err != nil {
		return nil, fmt.Errorf("dagnats: unmarshal run status: %w", err)
	}
	return &st, nil
}

// GetRunRaw fetches the full run JSON (not the trimmed RunStatus) so
// callers can inspect per-step status. pollRun uses it to find the
// in-progress step while the run is paused on WaitForSignal (the overall
// status stays "running", so the trimmed Step field lags behind).
func (c *Client) GetRunRaw(ctx context.Context, runID string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/runs/"+runID, nil)
	if err != nil {
		return nil, fmt.Errorf("dagnats: build get run raw: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dagnats: get run raw: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dagnats: get run raw: status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("dagnats: decode run raw: %w", err)
	}
	return out, nil
}
