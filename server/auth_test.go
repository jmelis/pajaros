package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRequireRedirectsBrowserNavigation(t *testing.T) {
	a := newTestAuth(t)
	protected := a.require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/some/page", nil)
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?next=") {
		t.Fatalf("Location = %q, want a /login?next= redirect", loc)
	}
	if got := extractNext(t, loc); got != "/some/page" {
		t.Errorf("next = %q, want /some/page", got)
	}
}

func TestRequireReturns401ForAPI(t *testing.T) {
	a := newTestAuth(t)
	protected := a.require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/hotspots?lat=0&lng=0", nil)
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestRequireAllowsValidSession(t *testing.T) {
	a := newTestAuth(t)
	var seen string
	protected := a.require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = userIDFromContext(r)
		w.WriteHeader(http.StatusOK)
	}))

	token, err := a.signer.issueSession("google:42", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	if seen != "google:42" {
		t.Fatalf("context user = %q, want google:42", seen)
	}
}

func TestRequireRejectsTamperedCookie(t *testing.T) {
	a := newTestAuth(t)
	protected := a.require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "forged.signature"})
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)
	if rr.Code == http.StatusOK {
		t.Fatal("tampered session cookie was accepted")
	}
}

func TestLoginPageWithoutConfiguredProviders(t *testing.T) {
	a := newTestAuth(t)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rr := httptest.NewRecorder()
	a.handleLoginPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, `href="/auth/`) {
		t.Error("login page offered a provider link despite no providers being configured")
	}
	if !strings.Contains(body, "No sign-in providers") {
		t.Errorf("login page did not explain that no providers are configured:\n%s", body)
	}
}

func TestLoginPageRendersConfiguredProviders(t *testing.T) {
	a := newTestAuth(t, configuredGoogle(t), configuredApple(t))
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rr := httptest.NewRecorder()
	a.handleLoginPage(rr, req)

	body := rr.Body.String()
	for _, want := range []string{
		"Sign in with Google",
		"Sign in with Apple",
		`href="/auth/google`,
		`href="/auth/apple`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("login page missing %q:\n%s", want, body)
		}
	}
}

func TestLoginPageShowsDisabledButtonForUnconfiguredProvider(t *testing.T) {
	apple, err := newAppleProvider(oauthEnv{})
	if err != nil {
		t.Fatal(err)
	}
	a := newTestAuth(t, configuredGoogle(t), apple)
	rr := httptest.NewRecorder()
	a.handleLoginPage(rr, httptest.NewRequest(http.MethodGet, "/login", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "Sign in with Apple (unavailable)") {
		t.Errorf("expected a disabled Apple button:\n%s", body)
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("expected disabled attribute:\n%s", body)
	}
}

func TestOAuthStartSetsStateAndPKCE(t *testing.T) {
	google := configuredGoogle(t)
	a := newTestAuth(t, google)

	state, st, _, location := startOAuth(t, a, google)
	if state == "" || st.Nonce == "" {
		t.Fatalf("empty state/nonce: %+v", st)
	}
	if st.Verifier == "" {
		t.Error("google start did not generate a PKCE verifier")
	}
	if st.Provider != "google" {
		t.Errorf("provider = %q, want google", st.Provider)
	}

	loc, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	q := loc.Query()
	if q.Get("state") != state {
		t.Errorf("redirect state = %q, want %q", q.Get("state"), state)
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("redirect missing PKCE challenge: %v", q)
	}
	if q.Get("nonce") != st.Nonce {
		t.Errorf("redirect nonce = %q, want %q", q.Get("nonce"), st.Nonce)
	}
}

func TestOAuthStartUnconfiguredProvider(t *testing.T) {
	google := newGoogleProvider(oauthEnv{}) // not configured
	a := newTestAuth(t, google)
	rr := httptest.NewRecorder()
	a.handleOAuthStart(google).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/auth/google", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}

func TestOAuthStartRejectsOpenRedirectNext(t *testing.T) {
	google := configuredGoogle(t)
	a := newTestAuth(t, google)

	req := httptest.NewRequest(http.MethodGet, "/auth/google?next=https://evil.example/", nil)
	rr := httptest.NewRecorder()
	a.handleOAuthStart(google).ServeHTTP(rr, req)
	var stateCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == oauthStateCookieName {
			stateCookie = c
		}
	}
	payload, _ := a.signer.unsign(stateCookie.Value)
	var st oauthState
	_ = json.Unmarshal(payload, &st)
	if st.Next != "/" {
		t.Errorf("next = %q, want /", st.Next)
	}
}

func TestLogoutClearsSessionCookie(t *testing.T) {
	a := newTestAuth(t)
	rr := httptest.NewRecorder()
	a.handleLogout(rr, httptest.NewRequest(http.MethodPost, "/auth/logout", nil))

	if rr.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
	var cleared *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookieName {
			cleared = c
		}
	}
	if cleared == nil {
		t.Fatal("logout did not clear the session cookie")
	}
	if cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("logout cookie not expired: %+v", cleared)
	}
}

func TestLogoutMakesNextRequestUnauthenticated(t *testing.T) {
	a := newTestAuth(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/logout", a.handleLogout)
	mux.Handle("/", a.require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := srv.Client()
	client.Jar = jar
	// Observe redirects rather than following them, so the post-logout 302 is
	// visible and the test doesn't chase /login.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	base, _ := url.Parse(srv.URL)

	token, err := a.signer.issueSession("google:1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(base, []*http.Cookie{{Name: sessionCookieName, Value: token, Path: "/"}})

	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated request = %d, want 200", resp.StatusCode)
	}

	logoutReq, err := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	logoutResp, err := client.Do(logoutReq)
	if err != nil {
		t.Fatal(err)
	}
	logoutResp.Body.Close()

	// The jar dropped the cleared session cookie, so the next request must be
	// treated as unauthenticated.
	next, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	next.Body.Close()
	if next.StatusCode != http.StatusFound {
		t.Fatalf("request after logout = %d, want 302", next.StatusCode)
	}
	if loc := next.Header.Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Fatalf("request after logout redirected to %q, want /login", loc)
	}
}

func TestSanitizeNext(t *testing.T) {
	cases := map[string]string{
		"":                      "/",
		"/dashboard":            "/dashboard",
		"/dashboard?x=1":        "/dashboard?x=1",
		"https://evil.example/": "/",
		"//evil.example/":       "/",
		`/\evil.example/`:       "/",
		"javascript:alert(1)":   "/",
		"/login":                "/",
		"/auth/google":          "/",
		"relative":              "/",
	}
	for in, want := range cases {
		if got := sanitizeNext(in); got != want {
			t.Errorf("sanitizeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func extractNext(t *testing.T, location string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	return u.Query().Get("next")
}
