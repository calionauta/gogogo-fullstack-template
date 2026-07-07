// Package llm wires LLM providers (OpenAI, Anthropic, Groq, Ollama) into
// the GoAI SDK and exposes streaming helpers that plug into Datastar SSE
// patch-signals. The streaming code is intentionally small: each token
// is one SSE event, the client reassembles the response with Datastar's
// data-on:signal patterns.
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	ds "github.com/starfederation/datastar-go/datastar"
)

// streamBufferSize is the read buffer for ConsumeStream. 4 KB matches
// the typical LLM SDK chunk size and bounds per-iteration allocations.
const streamBufferSize = 4096

// StreamToSSE sends LLM tokens to a Datastar SSE connection. The first
// event flips the streaming signal on; subsequent events patch one
// token at a time. Returns the underlying ChatStream error verbatim
// (no wrapping) so callers can match specific SDK error types.
func StreamToSSE(sse *ds.ServerSentEventGenerator, client *Client, prompt string) error {
	if sendErr := sse.Send(ds.EventTypePatchSignals, []string{`{"streaming":true}`}); sendErr != nil {
		return fmt.Errorf("llm: send streaming=true: %w", sendErr)
	}

	return client.ChatStream(sse.Context(), prompt, func(chunk string) error {
		data, err := json.Marshal(map[string]string{"token": chunk})
		if err != nil {
			return fmt.Errorf("llm: marshal token: %w", err)
		}
		if sendErr := sse.Send(ds.EventTypePatchSignals, []string{string(data)}); sendErr != nil {
			return fmt.Errorf("llm: send token: %w", sendErr)
		}
		return nil
	})
}

// ConsumeStream reads an io.ReadCloser and sends each chunk to fn.
// Returns the first non-EOF read error or the first fn error. The
// stream is closed in a deferred call so callers don't have to
// remember to Close it.
func ConsumeStream(stream io.ReadCloser, fn func(string) error) error {
	defer func() { _ = stream.Close() }()
	buf := make([]byte, streamBufferSize)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if cbErr := fn(string(buf[:n])); cbErr != nil {
				return fmt.Errorf("llm: stream callback: %w", cbErr)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("llm: read stream: %w", readErr)
		}
	}
}
