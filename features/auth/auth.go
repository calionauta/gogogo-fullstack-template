// SCOPE:layer=feature,removal=core — Auth middleware (cookie + login/logout)
// Package auth implements a single-user demo login on top of
// PocketBase's built-in auth API.
//
// Flow:
//
//   - GET /login renders the DaisyUI login form.
//   - POST /login validates credentials via PocketBase's
//     core.RecordUpsert + record password set helper; on success it
//     sets the `gogogo_auth` cookie (HttpOnly + SameSite=Lax) and
//     redirects to /.
//   - POST /logout clears the cookie and redirects to /login.
//   - LoadAuthFromCookie is a PocketBase middleware that reads the
//     cookie on every request and sets e.Auth — handlers that need
//     the current user just check e.Auth != nil.
//
// IMPORTANT: the app uses its OWN cookie name (`gogogo_auth`), NOT
// PocketBase's `pb_auth`. WHY (PocketBase gotcha, issues #5050/#1780):
// PB keeps `_superusers` (admin) and regular users as SEPARATE auth
// namespaces with different endpoints, and a single client holds only
// ONE auth state (one cookie). Reusing `pb_auth` for the app session
// would clobber the admin session in the same browser (and vice-versa)
// — the admin UI then shows "The authorized record is not allowed to
// perform this action." The two cookies are INTENTIONAL, not tech debt
// to "simplify" away. BEST PRACTICE: run the admin UI on a separate
// origin/port (e.g. :8090/_/) so even pb_auth never collides.
//
// The demo user (demo@demo.app / demo1234456) is seeded by
// db.SeedDefaults on first run; on a fresh clone it is the only
// way in. Change it in production.
package auth

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// cookieName is the app's OWN session cookie, kept distinct from
// pb_auth ON PURPOSE (see package doc — do NOT merge them, or the
// admin/app session clobber returns). pbAuthCookieName mirrors the same
// token under PocketBase's native cookie name so PB-native surfaces —
// most importantly the /api/realtime SSE channel for record-change
// subscriptions — authenticate as the same user. Without pb_auth set,
// /api/realtime is unauthenticated and PB's per-subscriber record-access
// check silently drops every record event.
const (
	cookieName       = "gogogo_auth"
	pbAuthCookieName = "pb_auth"
)

// CookieSecure is set at startup from config (true in production,
// false in dev so HTTP testing works).
var CookieSecure bool

// onLoginHook is an optional callback fired after a successful password
// login, with the authenticated user's PocketBase record id. Used by the
// onboarding flow to start a per-user workflow on login. Nil
// unless SetOnLoginHook is called by the onboarding wiring.
var onLoginHook func(userID string)

// SetOnLoginHook registers a callback invoked after every successful
// password login. The app's custom cookie auth does not go through
// PocketBase's native OnRecordAuthWithPasswordRequest hook, so this is
// the bridge that lets other packages react to logins. Safe to call
// once at wiring time; subsequent calls replace the hook.
func SetOnLoginHook(fn func(userID string)) {
	onLoginHook = fn
}

// fireOnLogin invokes the registered login hook, if any.
func fireOnLogin(userID string) {
	if onLoginHook != nil {
		onLoginHook(userID)
	}
}

// LoadAuthFromCookie is a PocketBase middleware. Reads the gogogo_auth
// cookie, validates the token via the SDK, and sets e.Auth so the
// rest of the request pipeline sees the logged-in user. Invalid
// tokens clear the cookie to avoid stale state.
//
// It deliberately skips PocketBase's own surfaces (the /_/ admin
// dashboard and everything under /api/, plus /health and /static/).
// PocketBase authenticates those itself via the pb_auth cookie
// (superuser for /_/ and admin API endpoints). If we set e.Auth here
// from the app cookie, PB's admin handlers would see a regular user
// record and reject admin actions with "The authorized record is not
// allowed to perform this action." The app's own routes (/, /todos,
// /login, /logout) are NOT skipped, so they still get e.Auth.
//
// NOTE: the app's JSON API under /api/ is also skipped here. None of
// the current /api/todos* handlers read e.Auth (they use c.App), so
// that is fine today. If a future /api handler needs e.Auth, bind
// LoadAuthFromCookie on that specific route instead of relying on
// this global middleware.
//
// Exception: /api/realtime is NOT skipped. PocketBase realtime sets the
// subscriber's auth (RealtimeClientAuthKey) from e.Auth at SSE connect
// time, then uses it to evaluate the collection's list/view rule for
// every delivered record event. If e.Auth is nil on /api/realtime, the
// rule fails for every subscriber and NO record events are delivered —
// which would make the PB-realtime record path (used for cross-tab todo
// sync) silently dead. The app issues BOTH gogogo_auth and pb_auth
// cookies (same token), so reading gogogo_auth here authenticates the
// realtime connection correctly.
func LoadAuthFromCookie(e *core.RequestEvent) error {
	p := e.Request.URL.Path
	switch {
	case p == "/health",
		strings.HasPrefix(p, "/_/"),
		strings.HasPrefix(p, "/api/") && p != "/api/realtime",
		strings.HasPrefix(p, "/static/"):
		return e.Next()
	}
	cookie, err := e.Request.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return e.Next()
	}
	record, err := e.App.FindAuthRecordByToken(cookie.Value, core.TokenTypeAuth)
	if err != nil {
		clearAuthCookie(e.Response)
		return e.Next()
	}
	e.Auth = record
	return e.Next()
}

// LoadAppAuth populates e.Auth from the app's gogogo_auth cookie WITHOUT
// the global /api/ skip used by LoadAuthFromCookie. Bind it on specific
// /api routes that need the authenticated user (e.g. the durable-workflow
// onboarding endpoint), so the request pipeline sees the logged-in demo
// user and can scope created records to their tenant.
//
// It returns e.Next() (leaving e.Auth nil) when there is no valid cookie,
// so handlers that bind it still work for anonymous requests and fall
// back to a default user.
func LoadAppAuth(e *core.RequestEvent) error {
	cookie, err := e.Request.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return e.Next()
	}
	record, err := e.App.FindAuthRecordByToken(cookie.Value, core.TokenTypeAuth)
	if err != nil {
		return e.Next()
	}
	e.Auth = record
	return e.Next()
}

// RequireAuthOrRedirect returns e.Next() when e.Auth is set, otherwise
// 303 redirects to /login. Use on routes that require login — but for
// a public demo, we use RedirectOnlyAnonymous to bounce guests to
// /login and keep signed-in users where they wanted to be.
func RequireAuthOrRedirect(e *core.RequestEvent) error {
	if e.Auth != nil {
		return e.Next()
	}
	return e.Redirect(http.StatusSeeOther, "/login")
}

// RedirectIfAuthed sends already-signed-in users from /login to
// /todo so they don't see a login form when they're already in.
// (Pre-refactor: redirected to "/" which served the todo page.
// After the landing-page refactor, "/" is the marketing hero and
// is not the user's destination after authenticating — /todo is.)
func RedirectIfAuthed(e *core.RequestEvent) error {
	if e.Auth != nil {
		return e.Redirect(http.StatusSeeOther, "/todo")
	}
	return e.Next()
}

// HandlePasswordLogin parses the form, validates credentials against
// the PocketBase `users` collection, sets the pb_auth cookie, and
// redirects. On failure it re-renders /login with an error message.
func HandlePasswordLogin(e *core.RequestEvent) error {
	if err := e.Request.ParseForm(); err != nil {
		return renderLoginPageTo(e, "Invalid form submission")
	}
	email := e.Request.FormValue("email")
	password := e.Request.FormValue("password")
	if email == "" || password == "" {
		return renderLoginPageTo(e, "Email and password required")
	}

	// Validate against the `users` collection (PocketBase's built-in
	// auth collection).
	record, err := e.App.FindAuthRecordByEmail("users", email)
	if err != nil {
		return renderLoginPageTo(e, "Wrong email or password")
	}
	if !record.ValidatePassword(password) {
		return renderLoginPageTo(e, "Wrong email or password")
	}

	// Mint a token and set the cookie.
	token, err := record.NewAuthToken()
	if err != nil {
		return renderLoginPageTo(e, "Could not issue auth token")
	}
	setAuthCookie(e.Response, token)
	// Fire the login hook (e.g. start the per-user durable onboarding
	// flow). Scoped to this user's record id, not a global broadcast.
	fireOnLogin(record.Id)
	return e.Redirect(http.StatusSeeOther, e.Request.FormValue("next"))
}

// HandleLogout clears the cookie and redirects to /login.
func HandleLogout(e *core.RequestEvent) error {
	clearAuthCookie(e.Response)
	return e.Redirect(http.StatusSeeOther, "/login")
}

// sessionMaxAgeSeconds keeps the auth cookie valid for 7 days. After
// that, users re-login with the same form.
const sessionMaxAgeSeconds = 7 * 24 * 60 * 60

func setAuthCookie(w http.ResponseWriter, token string) {
	// #nosec G124 — HttpOnly + Secure (configurable via CookieSecure)
	// + SameSite=LaxMode is the right attribute set for a session cookie.
	// Set BOTH the app's custom cookie (gogogo_auth, consumed by
	// LoadAppAuth) and PocketBase's native cookie (pb_auth, consumed by
	// /api/realtime). Same token, two names — see the const doc above.
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   sessionMaxAgeSeconds,
	})
	// #nosec G124 — same attribute set as the app cookie above
	// (HttpOnly + Secure configurable via CookieSecure + SameSite=Lax).
	http.SetCookie(w, &http.Cookie{
		Name:     pbAuthCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   sessionMaxAgeSeconds,
	})
}

var _ = strconv.Itoa // keep import for future rate-limit / nonce math
func clearAuthCookie(w http.ResponseWriter) {
	// #nosec G124 — HttpOnly is set; Secure is configurable via
	// CookieSecure (true in production, false in local dev for
	// http://127.0.0.1); SameSiteLaxMode is set. Clear BOTH cookies.
	for _, name := range []string{cookieName, pbAuthCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   CookieSecure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
	}
}
