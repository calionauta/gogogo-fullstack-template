package datastar

import (
	"encoding/json"
)

// MarshalSignals marshals a struct into a JSON string safe for data-signals.
func MarshalSignals(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
