// Package auth implements a single-user demo login on top of
// PocketBase's built-in auth API.
//
// Flow:
//
//   - GET /login renders the DaisyUI login form.
//   - POST /login validates credentials via PocketBase's
//     core.RecordUpsert + record password set helper; on success it
//     sets the `pb_auth` cookie (HttpOnly + SameSite=Lax) and redirects
//     to /.
//   - POST /logout clears the cookie and redirects to /login.
//   - LoadAuthFromCookie is a PocketBase middleware that reads the
//     cookie on every request and sets e.Auth — handlers that need
//     the current user just check e.Auth != nil.
//
// The demo user (demo@demo.app / demo1234456) is seeded by
// db.SeedDefaults on first run; on a fresh clone it is the only
// way in. Change it in production.
package auth

import (
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
)

const cookieName = "pb_auth"

// CookieSecure is set at startup from config (true in production,
// false in dev so HTTP testing works).
var CookieSecure bool

// LoadAuthFromCookie is a PocketBase middleware. Reads the pb_auth
// cookie, validates the token via the SDK, and sets e.Auth so the
// rest of the request pipeline sees the logged-in user. Invalid
// tokens clear the cookie to avoid stale state.
func LoadAuthFromCookie(e *core.RequestEvent) error {
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

// RedirectIfAuthed sends already-signed-in users from /login to /
// so they don't see a login form when they're already in.
func RedirectIfAuthed(e *core.RequestEvent) error {
	if e.Auth != nil {
		return e.Redirect(http.StatusSeeOther, "/")
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
	// + SameSite=Lax is the right attribute set for a session cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
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
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
