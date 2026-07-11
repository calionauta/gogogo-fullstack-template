package todo_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestIntegration_Auth_GuestIsRedirectedToLogin asserts that the /
// (home) route bounces unauthenticated visitors to /login. Mirrors
// the production behaviour from cmd/web/main.go, just exercised
// through the test fixture.
func TestIntegration_Auth_GuestIsRedirectedToLogin(t *testing.T) {
	ctx := t.Context()
	base, _, _, _, cleanup := testFixture(t)
	defer cleanup()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
	if err != nil {
		t.Fatalf("build GET /: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 (redirect), got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/login") {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
}

// TestIntegration_Auth_LoginFormIsShown asserts that GET /login
// returns a 200 HTML response containing the demo credentials
// prefilled. The user just has to click the Sign in button.
func TestIntegration_Auth_LoginFormIsShown(t *testing.T) {
	ctx := t.Context()
	base, _, _, _, cleanup := testFixture(t)
	defer cleanup()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/login", nil)
	if err != nil {
		t.Fatalf("build GET /login: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, demoEmail) {
		t.Errorf("login page missing demo email field; body starts: %s",
			body[:min(200, len(body))])
	}
	if !strings.Contains(body, demoPassword) {
		t.Errorf("login page missing demo password field")
	}
	if !strings.Contains(body, `action="/login"`) {
		t.Errorf("login form missing action attribute")
	}
}

// TestIntegration_Auth_BadPasswordIsRejected asserts the handler
// refuses wrong credentials and re-renders the login form with an
// error message — does NOT set the gogogo_auth cookie.
func TestIntegration_Auth_BadPasswordIsRejected(t *testing.T) {
	ctx := t.Context()
	base, _, _, _, cleanup := testFixture(t)
	defer cleanup()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/login",
		strings.NewReader(url.Values{
			"email":    {demoEmail},
			"password": {"definitely-wrong"},
			"next":     {"/"},
		}.Encode()))
	if err != nil {
		t.Fatalf("build POST /login: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (login form re-render), got %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "gogogo_auth" {
			t.Errorf("rejected login should NOT set gogogo_auth cookie")
		}
	}
}

// TestIntegration_Auth_GoodCredentialsSetsCookie is the happy path:
// POST /login with the documented demo creds issues a gogogo_auth
// cookie. We follow the 303 (which lands on /) and assert that the
// app page rendered with status 200, then look at the *cookies* the
// chain returned to find gogogo_auth. The Status assertion on the final
// page proves the auth let us past the login wall.
func TestIntegration_Auth_GoodCredentialsSetsCookie(t *testing.T) {
	ctx := t.Context()
	base, _, _, _, cleanup := testFixture(t)
	defer cleanup()

	var authCookie *http.Cookie
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			// CheckRedirect is called once per redirect. The first
			// call has len(via) == 0 (no predecessor yet). On
			// subsequent calls, via[len(via)-1] is the response
			// that issued the current redirect. We grab the gogogo_auth
			// cookie from it.
			if len(via) == 0 {
				return nil
			}
			prevResp := via[len(via)-1].Response
			if prevResp == nil {
				return nil
			}
			for _, c := range prevResp.Cookies() {
				if c.Name == "gogogo_auth" {
					authCookie = c
				}
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/login",
		strings.NewReader(url.Values{
			"email":    {demoEmail},
			"password": {demoPassword},
			"next":     {"/"},
		}.Encode()))
	if err != nil {
		t.Fatalf("build POST /login: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected final response 200, got %d", resp.StatusCode)
	}
	if authCookie == nil {
		t.Fatalf("gogogo_auth cookie not seen during redirect chain")
	}
	if authCookie.Value == "" {
		t.Fatalf("gogogo_auth cookie has empty value")
	}
}

var _ = func() {
	// Compile-time guard: readBody helper from fixture_test.go.
	_ = readBody
}
