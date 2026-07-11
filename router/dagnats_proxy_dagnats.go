//go:build dagnats

package router

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// mountDagNatsDashboard reverse-proxies the DagNats engine's own HTTP
// console (it listens on cfg.DagNats.HTTPAddr, e.g. 127.0.0.1:8090) under
// the app's own origin at /dagnats/.
//
// Why a proxy instead of linking straight to :8090: the app is reached
// through a Cloudflare Tunnel that only forwards 443 -> the app port.
// Port 8090 is NOT tunneled, so a direct link to
// https://gogogo.calionauta.com:8090/ hangs forever (the user-observed
// "loading infinito"). Proxying through the already-tunneled app means
// the DagNats dashboard is reachable with zero extra infra/tunnel config.
//
// The DagNats console is a SPA that emits absolute paths (/console/,
// /ui/, /v1/, /docs, /openapi.json, /hooks/, /metrics, ...). We strip the
// /dagnats prefix on the way in and rewrite those absolute prefixes back
// to /dagnats... on the way out (HTML/JS/CSS bodies + Location headers),
// which is the standard "mount a sub-app under a subpath" technique.
func mountDagNatsDashboard(se *core.ServeEvent, upstream string) {
	// upstream is host:port (e.g. 127.0.0.1:8090) from config; ensure a
	// scheme for url.Parse.
	if !strings.HasPrefix(upstream, "http://") && !strings.HasPrefix(upstream, "https://") {
		upstream = "http://" + upstream
	}
	target, err := url.Parse(upstream)
	if err != nil {
		log.Printf("dagnats proxy: bad upstream %q: %v", upstream, err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	orig := proxy.Director
	proxy.Director = func(r *http.Request) {
		orig(r)
		// Strip the /dagnats prefix so upstream sees e.g. /console/.
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/dagnats")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
	}
	// Rewrite absolute paths in responses back to the /dagnats subpath.
	proxy.ModifyResponse = rewriteDagNatsPaths

	se.Router.GET("/dagnats", func(c *core.RequestEvent) error {
		proxy.ServeHTTP(c.Response, c.Request)
		return nil
	})
	se.Router.GET("/dagnats/{path...}", func(c *core.RequestEvent) error {
		proxy.ServeHTTP(c.Response, c.Request)
		return nil
	})
}

// dagNatsAbsPrefixes are the absolute paths the DagNats SPA emits that
// must be re-prefixed with /dagnats so they resolve through the proxy.
var dagNatsAbsPrefixes = []string{
	"/console", "/ui", "/v1", "/docs", "/openapi.json",
	"/hooks", "/metrics", "/health", "/ready", "/debug",
}

// dagNatsPathRe matches an absolute DagNats path at the start of an
// attribute/URL (e.g. href="/console/...", fetch("/v1/..."),
// src="/ui/..."). It captures the leading quote/brace so we can preserve
// it and only rewrite the path.
var dagNatsPathRe = func() *regexp.Regexp {
	alts := strings.Join(dagNatsAbsPrefixes, "|")
	return regexp.MustCompile(`(["'(= ])(` + alts + `)(/|$)`)
}()

// rewriteDagNatsPaths rewrites absolute DagNats paths in the response body
// (HTML/JS/CSS) and the Location header to the /dagnats subpath.
func rewriteDagNatsPaths(resp *http.Response) error {
	loc := resp.Header.Get("Location")
	if loc != "" {
		for _, p := range dagNatsAbsPrefixes {
			if strings.HasPrefix(loc, p) {
				resp.Header.Set("Location", "/dagnats"+loc)
				break
			}
		}
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") &&
		!strings.Contains(ct, "javascript") &&
		!strings.Contains(ct, "text/css") {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	rewritten := dagNatsPathRe.ReplaceAllStringFunc(string(body), func(m string) string {
		// Re-run a simple capture to prefix only the path part.
		sub := dagNatsPathRe.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		quote, path, tail := sub[1], sub[2], sub[3]
		return quote + "/dagnats" + path + tail
	})
	resp.Body = io.NopCloser(bytes.NewReader([]byte(rewritten)))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", itoa(len(rewritten)))
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
