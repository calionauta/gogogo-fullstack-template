package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSmoke_BootedBinaryServesAndSyncs boots the REAL compiled binary (not a
// httptest server) and drives the full realtime path end-to-end:
//
//  1. /health returns 200 (the container healthcheck contract)
//  2. GET / renders the page with the centralized #sse-opener + datastar-ready
//     script, so the browser will actually open the SSE stream
//  3. opening GET /api/todos/stream returns 200 + text/event-stream, proving
//     the SSE transport is wired and a browser would connect (the exact
//     regression we shipped: "SSE never auto-connected")
//
// Running against the actual artifact catches build/flag/wiring breakage that
// the in-process httptest fixtures cannot (they bypass the binary's serve
// flags and the rendered HTML).
//
// The mutation-via-broadcast path (create in tab A → arrives in tab B) is
// covered by the in-process broadcast_probe_test.go; this test proves the
// binary itself boots and exposes the realtime transport.
//
// It is gated behind RUN_SMOKE=1 because it spawns a real server process that
// binds a TCP port. CI sets RUN_SMOKE=1; local `go test ./...` skips it so
// developers don't need a free port (the rendered-HTML assertion is also
// covered by TestLayoutRendersSSEOpener, which needs no server).
func TestSmoke_BootedBinaryServesAndSyncs(t *testing.T) {
	if os.Getenv("RUN_SMOKE") != "1" {
		t.Skip("set RUN_SMOKE=1 to boot the real binary and run the end-to-end smoke test")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping binary smoke test")
	}

	dir := t.TempDir()
	binPath := filepath.Join(dir, "gogogo-smoke")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binPath, ".")
	build.Stderr = &bytes.Buffer{}
	if err := build.Run(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, build.Stderr)
	}

	port := 18199
	dataDir := filepath.Join(dir, "data")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "serve", "--http", fmt.Sprintf("127.0.0.1:%d", port), "--dir", dataDir)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}
	defer func() {
		if cmd.Process == nil {
			return
		}
		if kerr := cmd.Process.Kill(); kerr != nil {
			t.Logf("smoke server kill: %v", kerr)
		}
		if _, werr := cmd.Process.Wait(); werr != nil {
			t.Logf("smoke server wait: %v", werr)
		}
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	if err := waitForHealthy(ctx, base); err != nil {
		t.Fatalf("binary never became healthy: %v", err)
	}
	assertPageWiresSSE(ctx, t, base)
	assertSSETransportOpens(ctx, t, base)
}

// waitForHealthy polls /health until it returns 200 or ctx expires.
func waitForHealthy(ctx context.Context, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return err
	}
	return waitFor(ctx, func() bool {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

// assertPageWiresSSE checks the rendered / page opens the realtime stream.
func assertPageWiresSSE(ctx context.Context, t *testing.T, base string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
	if err != nil {
		t.Fatalf("build / request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read / body: %v", err)
	}
	html := string(body)
	for _, want := range []string{`id="sse-opener"`, `datastar-ready`, `/api/todos/stream`} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered / missing %q — the SSE opener wiring is broken; browsers would not connect", want)
		}
	}
	if strings.Contains(html, "data-on:load=") {
		msg := "rendered / still uses a data-on:load= attribute; the SSE stream " +
			"would never open because Datastar v1 does not fire on:load on <div>/<body>"
		t.Errorf("%s", msg)
	}
}

// assertSSETransportOpens checks the SSE endpoint returns the right status
// and content type for a connected client.
func assertSSETransportOpens(ctx context.Context, t *testing.T, base string) {
	t.Helper()
	clientID := "smoke-" + time.Now().Format("150405")
	url := base + "/api/todos/stream?clientID=" + clientID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE stream status=%d, want 200 (stream did not open)", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Errorf("SSE stream Content-Type=%q, want text/event-stream", resp.Header.Get("Content-Type"))
	}
}

// waitFor polls cond every 200ms until it returns true or ctx expires.
func waitFor(ctx context.Context, cond func() bool) error {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if cond() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
