package components

import (
	"encoding/json"
	"strings"
)

// jsonMarshal is a thin wrapper that lets SafeJSON stay allocation-light.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// escapeSingleQuotes HTML-escapes single quotes so the JSON string can be
// safely embedded inside a Datastar data-* attribute (which uses single
// quotes as delimiters in expressions like data-class="{'open': ...}").
func escapeSingleQuotes(s string) string { return strings.ReplaceAll(s, "'", "&#39;") }
