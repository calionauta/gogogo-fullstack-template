// SCOPE:layer=feature,removal=feature — Collaborative whiteboard (Loro CRDT canvas)
// Embedded static assets for the whiteboard feature: rough.min.js (Rough.js
// hand-drawn canvas rendering) and whiteboard.js (Datastar/SSE canvas wiring).
package whiteboard

import (
	"embed"
	"io/fs"
)

//go:embed static/*
var staticEmbed embed.FS

// StaticFS returns an fs.FS for serving whiteboard static assets.
// Wrap with http.FS when passing to http.FileServer.
func StaticFS() fs.FS {
	sub, err := fs.Sub(staticEmbed, "static")
	if err != nil {
		panic("whiteboard: missing static directory: " + err.Error())
	}
	return sub
}
