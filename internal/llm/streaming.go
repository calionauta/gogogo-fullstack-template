// SCOPE:layer=infra,removal=plugin — GoAI LLM client (used by Suggest)
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	ds "github.com/starfederation/datastar-go/datastar"
)

// streamBufferSize is the read buffer for ConsumeStream. 4 KB matches
// the typical LLM SDK chunk size and bounds per-iteration allocations.
const streamBufferSize = 4096

// jsonArrayRE extracts the first balanced JSON array from a string,
// tolerating surrounding prose or markdown fences the model may add.
var jsonArrayRE = regexp.MustCompile(`(?s)\[.*?\]`)

// parseStringArray extracts a JSON array of strings from a model
// response. Defensive: handles surrounding markdown fences and
// prose. Returns the first 3 elements to keep the UI clean even
// if the model returned more.
func parseStringArray(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	// Strip markdown fences ```json ... ``` if present.
	if strings.HasPrefix(raw, "```") {
		raw = stripCodeFence(raw)
	}
	// Find the first array literal.
	match := jsonArrayRE.FindString(raw)
	if match == "" {
		return nil, errors.New("llm: no JSON array found in response")
	}
	var arr []string
	if err := json.Unmarshal([]byte(match), &arr); err != nil {
		return nil, fmt.Errorf("llm: decode array: %w", err)
	}
	// Trim and cap to 3 short items.
	out := make([]string, 0, 3)
	for _, s := range arr {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
		if len(out) == 3 {
			break
		}
	}
	return out, nil
}

// stripCodeFence removes a leading ```json and trailing ``` from a
// model response so the inner JSON parses cleanly.
func stripCodeFence(s string) string {
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "```"); i > 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// splitLines is the fallback for when the model returns plain text
// instead of JSON. Splits on newlines and trims bullets/dashes.
func splitLines(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		// Strip leading "- " or "* " bullets.
		line = strings.TrimLeft(line, "-* \t")
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) == 3 {
			break
		}
	}
	return out
}

// StreamToSSE sends LLM tokens to a Datastar SSE connection. The first
// event flips the streaming signal on; subsequent events patch one
// token at a time. Returns the underlying ChatStream error verbatim.
func StreamToSSE(sse *ds.ServerSentEventGenerator, client *Client, prompt string) error {
	if err := sse.Send(ds.EventTypePatchSignals, []string{`{"streaming":true}`}); err != nil {
		return fmt.Errorf("llm: send streaming=true: %w", err)
	}
	return client.ChatStream(sse.Context(), prompt, func(chunk string) error {
		data, err := json.Marshal(map[string]string{"token": chunk})
		if err != nil {
			return fmt.Errorf("llm: marshal token: %w", err)
		}
		return sse.Send(ds.EventTypePatchSignals, []string{string(data)})
	})
}

// ConsumeStream reads an io.ReadCloser and sends each chunk to fn.
// Returns the first non-EOF read error or the first fn error.
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
