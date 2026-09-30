package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newAuthForTest builds an Auth through the real newAuth path (unlike
// newTestAuth, which constructs one directly), so provider selection and
// open-mode setup are exercised. The UserStore is closed at cleanup.
func newAuthForTest(t *testing.T, env oauthEnv) (*Auth, *UserStore, error) {
	t.Helper()
	users, err := NewUserStore(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { users.Close() })
	signer, err := newCookieSigner("test-secret")
	if err != nil {
		t.Fatalf("newCookieSigner: %v", err)
	}
	a, err := newAuth(env, users, signer, nil)
	return a, users, err
}

func enabledGoogleEnv() oauthEnv {
	return oauthEnv{
		googleEnabled:      true,
		redirectBase:       "https://app.test",
		googleClientID:     "client-id",
		googleClientSecret: "client-secret",
	}
}

func enabledAppleEnv(t *testing.T) oauthEnv {
	t.Helper()
	return oauthEnv{
		appleEnabled:    true,
		redirectBase:    "https://app.test",
		appleTeamID:     "TEAMID1234",
		appleServicesID: "client-id",
		appleKeyID:      testKid,
		applePrivateKey: testECKeyPEM(t),
	}
}

func TestNewAuthOpenModeWhenNoProviderEnabled(t *testing.T) {
	a, _, err := newAuthForTest(t, oauthEnv{})
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	if !a.openMode() {
		t.Fatal("expected open mode when neither flag is set")
	}
	if len(a.providers) != 0 {
		t.Fatalf("providers = %v, want none", a.providers)
	}
}

func TestNewAuthCredentialsAloneDoNotEnableProvider(t *testing.T) {
	// Complete-looking credentials but no enable flag must still leave the
	// app open: enabling is explicit.
	env := oauthEnv{
		redirectBase:       "https://app.test",
		googleClientID:     "client-id",
		googleClientSecret: "client-secret",
	}
	a, _, err := newAuthForTest(t, env)
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	if !a.openMode() {
		t.Fatal("credentials alone enabled a provider; enabling must be explicit")
	}
}

func TestNewAuthGoogleOnly(t *testing.T) {
	a, _, err := newAuthForTest(t, enabledGoogleEnv())
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	if a.openMode() {
		t.Fatal("google-only must keep the login gate active")
	}
	if a.byName["google"] == nil {
		t.Error("google provider missing")
	}
	if a.byName["apple"] != nil {
		t.Error("apple provider present despite its flag being unset")
	}
	if len(a.providers) != 1 {
		t.Fatalf("providers = %v, want just google", a.providers)
	}
}

func TestNewAuthAppleOnly(t *testing.T) {
	a, _, err := newAuthForTest(t, enabledAppleEnv(t))
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	if a.openMode() {
		t.Fatal("apple-only must keep the login gate active")
	}
	if a.byName["apple"] == nil {
		t.Error("apple provider missing")
	}
	if a.byName["google"] != nil {
		t.Error("google provider present despite its flag being unset")
	}
	if len(a.providers) != 1 {
		t.Fatalf("providers = %v, want just apple", a.providers)
	}
}

func TestNewAuthBothEnabled(t *testing.T) {
	env := enabledAppleEnv(t)
	env.googleEnabled = true
	env.googleClientID = "client-id"
	env.googleClientSecret = "client-secret"

	a, _, err := newAuthForTest(t, env)
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	if a.openMode() {
		t.Fatal("both-enabled must keep the login gate active")
	}
	if a.byName["google"] == nil || a.byName["apple"] == nil {
		t.Fatalf("both providers expected, got %v", a.providers)
	}
}

func TestNewAuthGoogleEnabledButIncompleteFails(t *testing.T) {
	_, _, err := newAuthForTest(t, oauthEnv{googleEnabled: true})
	if err == nil {
		t.Fatal("expected a fatal configuration error")
	}
	for _, want := range []string{"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "OAUTH_REDIRECT_BASE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name missing %s", err, want)
		}
	}
}

func TestNewAuthAppleEnabledButIncompleteFails(t *testing.T) {
	_, _, err := newAuthForTest(t, oauthEnv{appleEnabled: true})
	if err == nil {
		t.Fatal("expected a fatal configuration error")
	}
	for _, want := range []string{"APPLE_TEAM_ID", "APPLE_SERVICES_ID", "APPLE_KEY_ID", "APPLE_PRIVATE_KEY", "OAUTH_REDIRECT_BASE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name missing %s", err, want)
		}
	}
}

func TestNewAuthAppleEnabledBadPrivateKeyFails(t *testing.T) {
	env := enabledAppleEnv(t)
	env.applePrivateKey = "not a valid PEM key"
	if _, _, err := newAuthForTest(t, env); err == nil {
		t.Fatal("expected a fatal error for an unparseable APPLE_PRIVATE_KEY")
	}
}

func TestEnvTruthy(t *testing.T) {
	for _, on := range []string{"1", "true", "TRUE", "Yes", "on", " true "} {
		if !envTruthy(on) {
			t.Errorf("envTruthy(%q) = false, want true", on)
		}
	}
	for _, off := range []string{"", "0", "false", "no", "off", "enabled", "2"} {
		if envTruthy(off) {
			t.Errorf("envTruthy(%q) = true, want false", off)
		}
	}
}

func TestGateOpenModeInjectsDevAccount(t *testing.T) {
	a, _, err := newAuthForTest(t, oauthEnv{})
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	var seen string
	h := a.gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = userIDFromContext(r)
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("open-mode request = %d, want 200", rr.Code)
	}
	if seen != devAccountID {
		t.Errorf("context user = %q, want %q", seen, devAccountID)
	}
}

func TestGateProviderModeAdmitsGuests(t *testing.T) {
	a, _, err := newAuthForTest(t, enabledGoogleEnv())
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	var seen string
	h := a.gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = userIDFromContext(r)
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/", "/api/hotspots", "/api/compare"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("guest GET %s = %d, want 200", path, rr.Code)
		}
		if seen != "" {
			t.Errorf("guest context user = %q, want none", seen)
		}
	}

	token, err := a.signer.issueSession("google:1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/hotspots", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if seen != "google:1" {
		t.Errorf("signed-in context user = %q, want google:1", seen)
	}
}

func TestProviderModeAccountRoutesRequireSignIn(t *testing.T) {
	a, _, err := newAuthForTest(t, enabledGoogleEnv())
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	h := a.gate(a.requireFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("guest /api/me = %d, want 401", rr.Code)
	}

	token, err := a.signer.issueSession("google:1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("signed-in /api/me = %d, want 200", rr.Code)
	}
}

// TestOpenModeRoutesReachableWithoutSession covers the top-level mux in open
// mode: "/" and /api/me are served directly, and /api/me reports the fixed
// development account.
func TestOpenModeRoutesReachableWithoutSession(t *testing.T) {
	a, users, err := newAuthForTest(t, oauthEnv{})
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	srv := &Server{users: users}
	appMux := http.NewServeMux()
	appMux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	appMux.HandleFunc("GET /api/me", srv.handleGetProfile)
	mux := buildRootMux(a, appMux, newIPRateLimiter("test", 30, 30))

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rr.Code)
	}

	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/me = %d (%s), want 200", rr.Code, rr.Body.String())
	}
	var p Profile
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if p.ID != devAccountID {
		t.Errorf("profile id = %q, want %q", p.ID, devAccountID)
	}
	if p.Language != defaultLang {
		t.Errorf("profile language = %q, want default %q", p.Language, defaultLang)
	}
}

// TestProviderModeLoginPageOffersOnlyEnabledProvider checks the top-level mux
// with Google (and not Apple) enabled: the page is reachable and offers Google
// only, and the Apple routes are inert (no redirect to Apple).
func TestProviderModeLoginPageOffersOnlyEnabledProvider(t *testing.T) {
	a, _, err := newAuthForTest(t, enabledGoogleEnv())
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	appMux := http.NewServeMux()
	appMux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux := buildRootMux(a, appMux, newIPRateLimiter("test", 30, 30))

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Sign in with Google") {
		t.Errorf("login page does not offer Google:\n%s", body)
	}
	if strings.Contains(body, "Sign in with Apple") {
		t.Errorf("login page offered Apple despite its flag being unset:\n%s", body)
	}

	// Apple's start route is not registered, so it is never a redirect to Apple.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/auth/apple", nil))
	if loc := rr.Header().Get("Location"); strings.Contains(loc, "appleid.apple.com") {
		t.Errorf("Apple start route is still active: redirect = %q", loc)
	}

	// Google's start route is live and redirects to Google.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/auth/google", nil))
	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "accounts.google.com") {
		t.Errorf("GET /auth/google = %d %q, want 302 to Google", rr.Code, rr.Header().Get("Location"))
	}
}

func TestHotspotRateLimitKeyUsesIPInOpenMode(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/hotspots", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, devAccountID))

	if got := hotspotRateLimitKey(true)(req); got != "203.0.113.9" {
		t.Errorf("open-mode key = %q, want the client IP", got)
	}
	if got := hotspotRateLimitKey(false)(req); got != devAccountID {
		t.Errorf("gated-mode key = %q, want the account id", got)
	}

	guest := httptest.NewRequest(http.MethodGet, "/api/hotspots", nil)
	guest.RemoteAddr = "203.0.113.9:1234"
	if got := hotspotRateLimitKey(false)(guest); got != "203.0.113.9" {
		t.Errorf("guest key = %q, want the client IP", got)
	}
}

// TestOpenModeRateLimiterSeparatesClientsByIP confirms the open-mode limiter
// throttles one IP without collapsing every client into the dev account's
// single bucket.
func TestOpenModeRateLimiterSeparatesClientsByIP(t *testing.T) {
	l := newKeyedRateLimiter("test", 1, 0)
	h := l.middleware(hotspotRateLimitKey(true), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	do := func(ip string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/hotspots", nil)
		req.RemoteAddr = ip + ":1234"
		req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, devAccountID))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	if code := do("198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first request from an IP = %d, want 200", code)
	}
	if code := do("198.51.100.1"); code != http.StatusTooManyRequests {
		t.Fatalf("second request from the same IP = %d, want 429", code)
	}
	if code := do("198.51.100.2"); code != http.StatusOK {
		t.Fatalf("request from a different IP = %d, want 200 (separate bucket)", code)
	}
}
