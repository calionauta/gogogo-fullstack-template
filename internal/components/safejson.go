// SCOPE:layer=infra,removal=core — JSON marshalling helpers for Datastar component attributes
package components

import (
	"encoding/json"
	"strings"
)

// jsonMarshal is a thin wrapper that lets SafeJSON stay allocation-light.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// escapeSingleQuotes HTML-escapes single quotes so the JSON string can be
// safely embedded inside a Datastar data-* attribute.
func escapeSingleQuotes(s string) string { return strings.ReplaceAll(s, "'", "&#39;") }
