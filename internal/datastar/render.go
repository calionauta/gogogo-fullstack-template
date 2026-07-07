package datastar

import (
	"context"
	"strings"

	"github.com/a-h/templ"
	datastarsdk "github.com/starfederation/datastar-go/datastar"
)

// RenderAndPatch renders a Templ component and patches it into the DOM via SSE.
func RenderAndPatch(
	sse *datastarsdk.ServerSentEventGenerator,
	component templ.Component,
	opts ...datastarsdk.PatchElementOption,
) error {
	var buf strings.Builder
	if err := component.Render(context.Background(), &buf); err != nil {
		return err
	}
	return sse.PatchElements(buf.String(), opts...)
}

// MergeSignals merges signals into the frontend state via SSE.
func MergeSignals(sse *datastarsdk.ServerSentEventGenerator, signals any) error {
	return sse.MarshalAndPatchSignals(signals)
}
