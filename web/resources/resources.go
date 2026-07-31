// SCOPE:core - DO NOT REMOVE - Embedded static assets (CSS, JS, etc.).
package resources

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed static/*
var staticEmbed embed.FS

// StaticFS returns an fs.FS for serving embedded static files (the
// contents of the static/ directory). Used both for route discovery and as
// the source for AssetHandler.
func StaticFS() fs.FS {
	sub, err := fs.Sub(staticEmbed, "static")
	if err != nil {
		panic("resources: missing static directory: " + err.Error())
	}
	return sub
}

// asset is one embedded file, pre-read at startup together with its
// content hash and MIME type.
type asset struct {
	data  []byte
	etag  string // quoted sha256 hex, a strong validator for revalidation
	ctype string
}

// AssetHandler serves every file in fsys at URLs under urlPrefix (e.g.
// "/static" maps /static/foo.js to the fsys entry "foo.js"). It is the
// single static-serving path for the app's embedded assets and implements
// the cache contract that prevents stale content after a deploy:
//
//	Cache-Control: public, max-age=0, must-revalidate
//	ETag: <sha256 of content>
//
// Assets are NOT fingerprinted (the URLs are fixed), so the correct best
// practice is to never let a stale copy be served: clients and CDNs may
// cache, but MUST revalidate with the ETag before reuse (cheap 304 when
// unchanged). Because embed.FS carries no mtime, Go's http.FileServer ETag
// would be size-only and collide when a file's bytes change without its
// size changing — this handler hashes the content instead, so a changed
// asset always yields a different ETag and a stale 304 is impossible.
func AssetHandler(fsys fs.FS, urlPrefix string) http.Handler {
	assets := map[string]asset{}
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		assets[p] = asset{
			data:  data,
			etag:  `"` + hex.EncodeToString(sum[:]) + `"`,
			ctype: mime.TypeByExtension(strings.ToLower(path.Ext(p))),
		}
		return nil
	}); err != nil {
		panic("resources: build asset table: " + err.Error())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, urlPrefix)
		name = strings.TrimPrefix(name, "/")
		a, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
		w.Header().Set("ETag", a.etag)
		// Strong revalidation: a matching If-None-Match means the cached
		// copy is still the current content — answer 304 with no body.
		if inm := r.Header.Get("If-None-Match"); inm != "" && (inm == a.etag || inm == "*") {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if a.ctype != "" {
			w.Header().Set("Content-Type", a.ctype)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(a.data)))
		if r.Method == http.MethodHead {
			return
		}
		// A client that goes away mid-write is normal HTTP; there is nothing
		// actionable to do with the error at this layer.
		_, _ = w.Write(a.data)
	})
}
