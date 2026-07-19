package basecoat

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

func marshalSignals(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func safeJSString(s string) string {
	return strconv.Quote(s)
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 min"
		}
		return fmt.Sprintf("%d min", m)
	default:
		return t.Format("02/01")
	}
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
