// SCOPE:layer=infra,removal=plugin — GoAI LLM client (used by Suggest)
// Package fakeserver provides an httptest-compatible OpenAI Chat
// Completions fake for end-to-end tests of the GoAI integration
// without burning real tokens.
//
// # Why this exists
//
// The project's production LLM client (GoAI) talks to any
// OpenAI-compatible /v1/chat/completions endpoint. Testing that
// end-to-end against a real provider costs tokens, requires a
// network, and is slow + flaky. A local fake server exercises:
//
//   - HTTP shape: POST {baseURL}/chat/completions with JSON body
//   - Auth: Authorization: Bearer <key>
//   - Streaming: data: {json}\n\n chunks + final data: [DONE]\n\n
//   - Retries: configurable 500/429 sequence to test the retry layer
//   - Timeouts: slow-response mode to test the context deadline
//
// It does NOT test the model's intelligence. That's a real-provider
// concern. This is for *plumbing*: did the request actually go out,
// was it shaped correctly, did we parse the response correctly, did
// the retry config fire on a 500.
//
// Usage
//
//	srv := fakeserver.New(t, fakeserver.WithResponse("hello world"))
//	defer srv.Close()
//	c := llm.New("test-key", llm.WithBaseURL(srv.URL()))
//	out, _ := c.Chat(ctx, "ignored prompt")
//	// assert out == "hello world"
//
// Streaming
//
//	srv := fakeserver.New(t, fakeserver.WithStreamChunks("hello ", "world"))
//	c.Stream(ctx, "...", func(chunk string) error {
//	    // receive "hello ", then "world", then ""
//	})
//
// Failure injection
//
//	srv := fakeserver.New(t,
//	    fakeserver.WithStatusSequence(500, 500, 200),  // first 2 calls 500
//	    fakeserver.WithResponse("recovered"))
//	// retry layer should succeed on the 3rd call.
package fakeserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// openAIRequest is the minimum OpenAI chat-completions payload we
// need to inspect. We deliberately don't decode all fields — extra
// fields (temperature, top_p, tools, etc.) come from GoAI and we
// shouldn't care in tests.
type openAIRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

// openAIResponse is the minimum non-streaming response we return.
type openAIResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// openAIChunk is the streaming chunk shape.
type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// Options configures a FakeServer. Use With... functions, never
// construct it directly.
type Options struct {
	// cannedResponse is the body returned for non-streaming calls.
	cannedResponse string

	// streamChunks is the sequence of content chunks returned for
	// streaming calls. Empty means use cannedResponse as a single chunk.
	streamChunks []string

	// statusSequence is the per-call HTTP status sequence. If non-empty,
	// each call pops the next status; once exhausted, falls back to
	// statusOK. Useful for retry-layer tests.
	statusSequence []int

	// responseDelay adds a fixed delay before the response is sent.
	// For streaming, applied before the first chunk only (so the
	// stream "starts slow" but then flows normally).
	responseDelay time.Duration

	// acceptKeys is the set of API keys the fake accepts. Empty
	// means "accept any non-empty Bearer token".
	acceptKeys map[string]bool
}

// Option is a functional Option for New.
type Option func(*Options)

// WithResponse sets the response body for non-streaming calls.
func WithResponse(s string) Option { return func(o *Options) { o.cannedResponse = s } }

// WithStreamChunks sets the streaming chunks. Overrides WithResponse
// for streaming calls.
func WithStreamChunks(chunks ...string) Option {
	return func(o *Options) { o.streamChunks = chunks }
}

// WithStatusSequence injects HTTP status codes for sequential calls.
// After the sequence is exhausted, 200 is used.
func WithStatusSequence(codes ...int) Option {
	return func(o *Options) { o.statusSequence = codes }
}

// WithResponseDelay adds a fixed delay before responding.
func WithResponseDelay(d time.Duration) Option {
	return func(o *Options) { o.responseDelay = d }
}

// WithAPIKey restricts accepted API keys. Empty map = accept any.
func WithAPIKey(allowed ...string) Option {
	return func(o *Options) {
		m := make(map[string]bool, len(allowed))
		for _, k := range allowed {
			m[k] = true
		}
		o.acceptKeys = m
	}
}

// FakeServer is a real httptest.Server that implements the slice of
// the OpenAI Chat Completions API the project's GoAI client uses.
// Methods on it are goroutine-safe for concurrent request counting
// and response delay.
type FakeServer struct {
	*httptest.Server
	t         *testing.T
	opts      Options
	callCount atomic.Int64
}

// NewServer starts a fake OpenAI server on a random local port without
// requiring a *testing.T. Useful for production demos / long-running
// simulated clients (e.g. the template's SIMULATE_LLM mode) where no
// test is driving the server. Pass t via New if you want assertion
// logging; otherwise use NewServer and the fake logs nothing.
func NewServer(opts ...Option) *FakeServer {
	o := Options{
		cannedResponse: "hello", // your suggested default
	}
	for _, opt := range opts {
		opt(&o)
	}
	s := &FakeServer{
		opts: o,
	}
	// Mount at /v1 AND at / so GoAI configs that include or omit
	// the /v1 prefix both work without test-side conditionals.
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", s.handle)
	mux.HandleFunc("/v1/chat/completions", s.handle)
	s.Server = httptest.NewServer(mux)
	return s
}

// New starts a fake OpenAI server on a random local port. The test
// argument is required so the fake can log assertions against it.
func New(t *testing.T, opts ...Option) *FakeServer {
	t.Helper()
	s := NewServer(opts...)
	s.t = t
	return s
}

// handle is the single endpoint the GoAI compat provider hits. Each
// step is split into a small helper so this function (and each
// helper) stays under the gocyclo threshold.
func (s *FakeServer) handle(w http.ResponseWriter, r *http.Request) {
	n := s.callCount.Add(1)

	if !s.authorize(r, n) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	req, ok := s.decodeRequest(w, r)
	if !ok {
		return
	}

	if !s.writeStatusOrSuccess(w, n) {
		return
	}

	// Optional latency before responding.
	if s.opts.responseDelay > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(s.opts.responseDelay):
		}
	}

	if req.Stream {
		s.streamResponse(w, req)
		return
	}
	s.singleResponse(w, req)
}

// authorize returns false (and logs) when the request has an API key
// but the fake's acceptKeys map doesn't include it. When no keys are
// configured, all requests are accepted.
func (s *FakeServer) authorize(r *http.Request, n int64) bool {
	if len(s.opts.acceptKeys) == 0 {
		return true
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.opts.acceptKeys[auth] {
		return true
	}
	if s.t != nil {
		s.t.Logf("fake: call %d rejected: bad/unknown API key %q", n, auth)
	}
	return false
}

// decodeRequest reads + JSON-decodes the request body. On error it
// writes a 400 and returns ok=false so the caller can early-return.
func (s *FakeServer) decodeRequest(w http.ResponseWriter, r *http.Request) (openAIRequest, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if s.t != nil {
			s.t.Errorf("fake: read body: %v", err)
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return openAIRequest{}, false
	}
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		if s.t != nil {
			s.t.Errorf("fake: decode request: %v", err)
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return openAIRequest{}, false
	}
	return req, true
}

// writeStatusOrSuccess picks the next status from the configured
// sequence and writes a minimal error body for non-200 responses.
// Returns false when a non-200 was written so the caller does not
// continue into the success path.
func (s *FakeServer) writeStatusOrSuccess(w http.ResponseWriter, n int64) bool {
	status := http.StatusOK
	if i := int(n) - 1; i < len(s.opts.statusSequence) {
		status = s.opts.statusSequence[i]
	}
	if status == http.StatusOK {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := `{"error":{"message":"forced "` + fmt.Sprint(status) + `","type":"server_error"}}`
	_, _ = io.WriteString(w, body)
	return false
}

// singleResponse writes the canned non-streaming reply.
func (s *FakeServer) singleResponse(w http.ResponseWriter, _ openAIRequest) {
	w.Header().Set("Content-Type", "application/json")
	resp := openAIResponse{}
	resp.Choices = append(resp.Choices, struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}{})
	resp.Choices[0].Message.Role = "assistant"
	resp.Choices[0].Message.Content = s.opts.cannedResponse
	_ = json.NewEncoder(w).Encode(resp)
}

// streamResponse writes the streaming chunks as
// `data: {json}\n\n` lines, finishing with `data: [DONE]\n\n`.
//
// The chosen chunks are either WithStreamChunks (if set) or
// WithResponse split by space (so the canned string "hello world"
// streams as "hello ", "world").
func (s *FakeServer) streamResponse(w http.ResponseWriter, _ openAIRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		if s.t != nil {
			s.t.Errorf("fake: ResponseWriter doesn't support Flusher")
		}
		return
	}

	chunks := s.opts.streamChunks
	if len(chunks) == 0 {
		// Fall back: split canned response by word boundary.
		chunks = strings.Fields(s.opts.cannedResponse)
		if len(chunks) > 1 {
			// emit "first word", then remaining as one chunk
			chunks = []string{chunks[0] + " ", strings.Join(chunks[1:], " ")}
		}
	}

	for _, chunk := range chunks {
		c := openAIChunk{}
		c.Choices = append(c.Choices, struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		}{})
		c.Choices[0].Delta.Content = chunk
		data, _ := json.Marshal(c)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// CallCount returns the total number of requests the server has
// received. Tests assert on this to confirm retries happened.
func (s *FakeServer) CallCount() int64 { return s.callCount.Load() }
