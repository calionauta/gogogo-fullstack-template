package auth

import (
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterAuth wires all auth-related routes and middleware on the
// given PocketBase app. Designed to be called from router.Init, which
// owns the wiring across features. The login flow:
//
//   - GET  /login           renders the standalone LoginPage (no navbar)
//   - POST /login           handles credentials, sets pb_auth cookie
//   - POST /logout          clears cookie and redirects
//
// Middleware (in priority order, applied to every request):
//
//   - LoadAuthFromCookie   populates e.Auth from the cookie
//
// Cookie attributes: HttpOnly (set via CookieSecure for production).
func RegisterAuth(app *pocketbase.PocketBase) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Render the login form via the templ component. We assign
		// to the package-level renderLogin so auth.go stays in
		// "handler-only, no templ imports" territory. The captured
		// handler is bound later (right before request handling); the
		// router handles the actual GET wiring.
		se.Router.GET("/login", RedirectIfAuthed).BindFunc(handleLoginGet)
		se.Router.POST("/login", HandlePasswordLogin)
		se.Router.POST("/logout", HandleLogout)

		// Middleware: load auth from cookie on every request.
		se.Router.BindFunc(LoadAuthFromCookie)

		return se.Next()
	})
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

// HandleLoginGetForTest is the exported alias used by features that
// wire the login route outside the OnServe hook (e.g. integration
// tests that drive routes via PocketBase's router instead of the
// full server). Identical behaviour to handleLoginGet; exported so
// external test packages can bind it.
func HandleLoginGetForTest(e *core.RequestEvent) error {
	return handleLoginGet(e)
}

// renderLoginPageTo writes the login form to the response. Used by
// the package's HTTP handler so we don't need a separate
// RequestEvent-bound render path inside the OnServe callback.
func renderLoginPageTo(e *core.RequestEvent, errMsg string) error {
	next := e.Request.URL.Query().Get("next")
	if next == "" {
		next = "/"
	}
	e.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	e.Response.WriteHeader(http.StatusOK)
	return LoginPage(next, errMsg).Render(e.Request.Context(), e.Response)
}

// silence unused-import warnings if the package is built with strict
// linters that flag the strconv import separately.
var _ = strconv.Itoa
