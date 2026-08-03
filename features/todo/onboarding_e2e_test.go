package todo_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danmestas/dagnats/server"
	"github.com/danmestas/dagnats/worker"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/db"
	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/features/todo/handlers"
	"github.com/calionauta/gogogo-fullstack-template/internal/dagnats"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// e2eDagNatsHTTP / e2eNATS are a dedicated DagNats engine port for this
// test so it never clashes with TestOnboarding_ResumeSignalsRun (which
// uses 127.0.0.1:18097 / 4222) when the dagnats-tagged suite runs.
const (
	e2eDagNatsHTTP = "127.0.0.1:18098"
	e2eNATS        = 4223
)

// buildFixtureDagNats boots the SAME full stack the production app serves
// (PocketBase + goqite + todo routes + SSE + auth) AND a real DagNats
// engine wired to the onboarding handler. That lets the durable
// onboarding workflow be driven end-to-end over the REAL HTTP + SSE path
// (login → POST /api/onboarding/start → SSE step-2 await → create first
// todo → resume signal → SSE completion), which is exactly what a browser
// does — just without pixel rendering. Reliable where a literal browser
// E2E is not (agent_browser drops the auth cookie on SSE connections).
func buildFixtureDagNats(t *testing.T) (
	string, *queue.Queue, *pocketbase.PocketBase, *handlers.TodoHandler, *dagnats.Client, func(),
) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "todo-dagnats-e2e-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	cfg := &config.Config{
		Host:          "127.0.0.1",
		Port:          0,
		Dev:           true,
		DataDir:       tmpDir,
		DBPath:        tmpDir + "/app.db",
		EncryptionKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	app := newPocketBaseApp(cfg)
	if bootErr := app.Bootstrap(); bootErr != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Bootstrap: %v", bootErr)
	}
	if collErr := createTodosCollection(app); collErr != nil {
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("create todos collection: %v", collErr)
	}
	q, err := queue.New(cfg)
	if err != nil {
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("queue.New: %v", err)
	}
	h := handlers.New(app, q, cfg)
	h.RegisterHandlers(q.Registry())
	broadcaster := nats.NewInMemoryBroadcaster(q.Hub())
	h.SetBroadcaster(broadcaster)

	workers := q.StartWorkers()

	if seedErr := db.SeedDefaults(app, true); seedErr != nil {
		t.Fatalf("SeedDefaults: %v", seedErr)
	}
	if err = seedDemoUserInline(app); err != nil {
		t.Fatalf("seedDemoUserInline: %v", err)
	}

	r := router.NewRouter[*core.RequestEvent](newRequestEventFactory(app))
	auth.CookieSecure = false
	r.BindFunc(auth.LoadAuthFromCookie)
	h.RegisterRoutesOn(r)
	r.GET("/login", auth.RedirectIfAuthed).BindFunc(auth.HandleLoginGetForTest)
	r.POST("/login", auth.HandlePasswordLogin)
	r.POST("/logout", auth.HandleLogout)

	// Boot a real DagNats engine (same wiring cmd/web/dagnats.go uses).
	srv := server.New(server.Config{
		DataDir:       t.TempDir(),
		HTTPAddr:      e2eDagNatsHTTP,
		NATSPort:      e2eNATS,
		MaxStoreBytes: 1 << 30,
	})
	shim := server.EmbeddedWorker(srv)
	shim.Handle("onboarding-greet", func(ctx worker.TaskContext) error {
		// Forward the run input (root step) so the create-todo steps
		// receive {"user":..., "todos":[...]} — mirrors production.
		return ctx.Complete(ctx.Input())
	})
	shim.Handle("onboarding-await-first-todo", func(ctx worker.TaskContext) error {
		if _, waitErr := ctx.WaitForSignal("first-todo", 50*time.Minute); waitErr != nil {
			return ctx.Fail(waitErr)
		}
		// Forward the owner payload through to the create-todo steps.
		return ctx.Complete(ctx.Input())
	})
	shim.Handle("onboarding-create-todo", func(ctx worker.TaskContext) error {
		// Mirror the production worker (cmd/web/dagnats.go): the run
		// input is {"user":..., "todos":[...]}, threaded forward by the
		// root step; each create-todo step pops one title, creates that
		// todo scoped to the owner, and completes with the remainder.
		var input struct {
			User  string   `json:"user"`
			Todos []string `json:"todos"`
		}
		if len(ctx.Input()) > 0 {
			_ = json.Unmarshal(ctx.Input(), &input)
		}
		text := ""
		if len(input.Todos) > 0 {
			text, input.Todos = input.Todos[0], input.Todos[1:]
		}
		if text == "" {
			text = "Onboarding task"
		}
		if createErr := h.CreateTodoForOnboarding(text, input.User); createErr != nil {
			return ctx.Fail(createErr)
		}
		out, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			return ctx.Fail(marshalErr)
		}
		return ctx.Complete(out)
	})
	shim.Handle("onboarding-finalize", func(ctx worker.TaskContext) error {
		return ctx.Complete([]byte(`"done"`))
	})
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run() }()
	t.Cleanup(func() {
		srv.Stop()
		if runErrVal := <-runErr; runErrVal != nil {
			t.Logf("dagnats test server stopped: %v", runErrVal)
		}
	})
	waitForDagNatsReady(t, e2eDagNatsHTTP)

	client := dagnats.NewClient("http://" + e2eDagNatsHTTP)
	registerOnboardingWorkflow(t, client)

	// Wire the onboarding HTTP route (real browser path) onto the SAME
	// router the SSE stream + todos are served from, mirroring
	// router/onboarding_dagnats.go.
	handlers.RegisterOnboardingRoutes(app, q, "http://"+e2eDagNatsHTTP, r, broadcaster, h)

	mux, err := r.BuildMux()
	if err != nil {
		workers.Stop()
		q.Close()
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("BuildMux: %v", err)
	}
	server := httptest.NewServer(mux)
	cleanup := func() {
		server.Close()
		workers.Stop()
		q.Close()
		mustReset(t, app)
		os.RemoveAll(tmpDir)
	}
	return server.URL, q, app, h, client, cleanup
}

// waitForDagNatsReady polls the engine's /ready endpoint until it
// answers 200 (the embedded NATS + REST API are up). Mirrors the helper
// used by TestOnboarding_ResumeSignalsRun.
func waitForDagNatsReady(t *testing.T, addr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := "http://" + addr + "/ready"
	for range 120 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("ready request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("DagNats engine at %s never became ready", addr)
}

// registerOnboardingWorkflow uploads the durable onboarding workflow
// definition to the engine (idempotent re-registration).
func registerOnboardingWorkflow(t *testing.T, client *dagnats.Client) {
	t.Helper()
	if err := client.RegisterWorkflow(context.Background(), []byte(dagnats.OnboardingWorkflowJSON)); err != nil {
		t.Fatalf("RegisterWorkflow: %v", err)
	}
}

// TestOnboarding_E2ERunsToCompletion is the end-to-end regression guard
// for the durable onboarding workflow driven through the REAL HTTP + SSE
// path a browser uses:
//
//   - login (gogogo_auth cookie)
//   - open the authenticated SSE stream
//   - POST /api/onboarding/start → the run starts and reaches step 2
//     (await-first-todo): the SSE stream shows "step":2
//   - create the first todo → handleCreate calls ResumeOnboarding → the
//     "first-todo" signal fires → the run resumes and runs to completion
//   - the SSE stream shows the "workflow-completed" broadcast
//
// If someone breaks the start route, the SSE progress broadcast, the
// create→resume wiring, or the workflow definition, this fails before
// production does — replacing the engine-only TestOnboarding_ResumeSignalsRun
// (which drives the handler directly) with the full request/SSE path.
func TestOnboarding_E2ERunsToCompletion(t *testing.T) {
	base, _, _, _, client, cleanup := buildFixtureDagNats(t)
	defer cleanup()
	_ = client

	ctx := context.Background()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	httpClient := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	loginUser(ctx, t, httpClient, base, demoEmail, demoPassword)

	clientID := "e2e-onboarding-" + time.Now().Format(clientIDSuffixFormat)
	stream := openSSEWithClient(ctx, t, httpClient, base, clientID)
	defer func() { _ = stream.Body.Close() }()

	// 1) Start the durable workflow (real HTTP route, authed).
	startResp, err := doPostForm(ctx, httpClient, base+"/api/onboarding/start?clientID="+clientID, url.Values{})
	if err != nil {
		t.Fatalf("start onboarding: %v", err)
	}
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("start status=%d", startResp.StatusCode)
	}
	_ = startResp.Body.Close()

	// 2) The workflow must reach step 2 (await-first-todo) via the SSE
	//    stream before we resume it. The progress job's "step" field is
	//    merged into the "onboardingStep" signal on the wire.
	full1 := pumpSSEUntil(t, stream, 10*time.Second, func(s string) bool {
		return strings.Contains(s, `"onboardingStep":2`)
	})
	if !strings.Contains(full1, `"onboardingStep":2`) {
		t.Fatalf("durable workflow never reached step 2 (await-first-todo) — tail:\n%s", tailString(full1, 1000))
	}

	// 3) Create the first todo → ResumeOnboarding signals "first-todo".
	createResp, err := doPostForm(ctx, httpClient, base+"/api/todos", url.Values{titleField: {"first e2e todo"}})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d", createResp.StatusCode)
	}
	_ = createResp.Body.Close()

	// 4) The run must now run to completion. The final progress job
	//    publishes onboardingStep:6, and the workflow-completed broadcast
	//    fires as well — either proves the run finished.
	full2 := pumpSSEUntil(t, stream, 30*time.Second, func(s string) bool {
		return strings.Contains(s, "workflow-completed") || strings.Contains(s, `"onboardingStep":6`)
	})
	if !strings.Contains(full2, "workflow-completed") && !strings.Contains(full2, `"onboardingStep":6`) {
		t.Fatalf("durable workflow did not complete — tail:\n%s", tailString(full2, 1500))
	}

	// 5) The 3 example todos must be visible in the user's list, scoped
	//    to the demo user (the owner that started the run). The worker
	//    created them server-side, so re-fetch the list fragment and
	//    assert each example title shows up (bug 2 regression: they were
	//    previously created owner-less and never surfaced).
	fragReq, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/todos/fragment", nil)
	if reqErr != nil {
		t.Fatalf("new fragment request: %v", reqErr)
	}
	frag, err := httpClient.Do(fragReq)
	if err != nil {
		t.Fatalf("fetch todo fragment: %v", err)
	}
	fragBody, _ := io.ReadAll(frag.Body)
	_ = frag.Body.Close()
	if frag.StatusCode != http.StatusOK {
		t.Fatalf("fragment status=%d body=%s", frag.StatusCode, fragBody)
	}
	for _, title := range dagnats.ExampleTodoTexts {
		if !strings.Contains(string(fragBody), title) {
			t.Fatalf("example todo %q missing from user list after workflow — body:\n%s", title, fragBody)
		}
	}
}
