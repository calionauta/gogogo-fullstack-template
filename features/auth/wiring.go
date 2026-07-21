// SCOPE:layer=feature,removal=core — Auth route registration (/login, /logout, cookie middleware)
package auth

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// RegisterAuth wires all auth-related routes and middleware DIRECTLY on
// the supplied ServeEvent's router. Designed to be called from inside
// another OnServe hook handler (typically router.Init) — NOT as a
// nested app.OnServe().BindFunc, because PocketBase's `Hook.Trigger`
// snapshots the registered handlers before any of them run, so any
// BindFunc added during a Trigger never fires.
//
// Routes wired:
//
//   - GET  /login           renders the standalone LoginPage (no navbar)
//   - POST /login           handles credentials, sets pb_auth cookie
//   - POST /logout          clears cookie and redirects
//
// Cookie attributes: HttpOnly (set via CookieSecure for production).
//
// The /login route checks `e.Auth` first — already-signed-in users get
// redirected to /. This folded redirect avoids the bindfunc-chain
// ordering pitfalls we hit when /login had separate `RedirectIfAuthed`
// + `handleLoginGet` handlers.
func RegisterAuth(se *core.ServeEvent) {
	se.Router.GET("/login", handleLoginGetWithRedirect)
	se.Router.POST("/login", HandlePasswordLogin)
	se.Router.POST("/logout", HandleLogout)
}

// HandleLoginGetForTest is the exported alias used by features that
// wire the login route outside the OnServe hook (e.g. integration
// tests that drive routes via PocketBase's router instead of the
// full server). Identical behaviour to handleLoginGet; exported so
// external test packages can bind it.
func HandleLoginGetForTest(e *core.RequestEvent) error {
	return handleLoginGet(e)
}

// handleLoginGet renders the standalone login form. Kept tiny so the
// HTTP route lives next to the wiring. Reads ?next= so the user
// returns to the page they tried to reach.
func handleLoginGet(e *core.RequestEvent) error {
	errMsg := ""
	if cookie, cookieErr := e.Request.Cookie(cookieName); cookieErr == nil && cookie.Value != "" {
		// Re-prompt for the password only if the cookie is
		// malformed, not when it's just an expired session.
		if _, err := e.App.FindAuthRecordByToken(cookie.Value, core.TokenTypeAuth); err != nil {
			errMsg = "Session expired. Please sign in again."
		}
	}
	return renderLoginPageTo(e, errMsg)
}

// handleLoginGetWithRedirect renders the login form unless the user is
// already signed in, in which case it sends them to /todo. Folding
// the redirect into the render handler keeps the route's middleware
// chain flat (a single handler, no BindFunc composition).
// (Pre-refactor: redirected to "/" which served the todo page.
// After the landing-page refactor, "/" is the marketing hero —
// signed-in users go to /todo instead.)
func handleLoginGetWithRedirect(e *core.RequestEvent) error {
	if e.Auth != nil {
		return e.Redirect(http.StatusSeeOther, "/todo")
	}
	return handleLoginGet(e)
}

// renderLoginPageTo writes the login form to the response. Used by
// the package's HTTP handler so we don't need a separate
// RequestEvent-bound render path inside the OnServe callback.
func renderLoginPageTo(e *core.RequestEvent, errMsg string) error {
	next := e.Request.URL.Query().Get("next")
	if next == "" {
		// Default post-login destination is /todo (the demo app),
		// not "/" (which is now the public landing page).
		next = "/todo"
	}
	e.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	e.Response.WriteHeader(http.StatusOK)
	return LoginPage(next, errMsg).Render(e.Request.Context(), e.Response)
}
