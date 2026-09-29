package main

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGoogleCallbackCreatesUserAndSession(t *testing.T) {
	key, jwksURL := testJWKS(t)
	google := configuredGoogle(t)
	google.verifier = newJWTVerifier(jwksURL, []string{"https://accounts.google.com"}, "client-id")
	a := newTestAuth(t, google)

	state, st, stateCookie, _ := startOAuth(t, a, google)
	idToken := testIDToken(t, key, map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            "client-id",
		"sub":            "google-sub",
		"email":          "bird@example.com",
		"email_verified": true,
		"nonce":          st.Nonce,
		"exp":            time.Now().Add(time.Hour).Unix(),
	})
	google.conf.Endpoint.TokenURL = testTokenServer(t, idToken)

	req := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=code123&state="+url.QueryEscape(state), nil)
	req.AddCookie(stateCookie)
	rr := httptest.NewRecorder()
	a.handleOAuthCallback(google).ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("callback code = %d (%s), want 302", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/" {
		t.Errorf("redirect = %q, want /", loc)
	}
	assertSessionUser(t, a, rr, "google:google-sub")
	if u, ok, _ := a.users.Get("google:google-sub"); !ok || u.Email != "bird@example.com" {
		t.Fatalf("persisted user = %+v (ok=%v)", u, ok)
	}
}

// TestGoogleCallbackSetsDefaultLanguageFromIP checks that a brand-new
// account's first login seeds its language preference from the signup IP's
// geolocated country, and that a returning account's login never overwrites
// whatever preference (including none) it already has.
func TestGoogleCallbackSetsDefaultLanguageFromIP(t *testing.T) {
	geoip, err := loadGeoIPStore()
	if err != nil {
		t.Fatalf("loadGeoIPStore: %v", err)
	}
	// Verified against data/geoip_country.json.gz.
	const spanishIP = "1.178.22.10:5555"
	const frenchIP = "1.178.90.5:5555"

	key, jwksURL := testJWKS(t)

	login := func(a *Auth, google *googleProvider, sub, remoteAddr string) *httptest.ResponseRecorder {
		state, st, stateCookie, _ := startOAuth(t, a, google)
		idToken := testIDToken(t, key, map[string]any{
			"iss":            "https://accounts.google.com",
			"aud":            "client-id",
			"sub":            sub,
			"email":          sub + "@example.com",
			"email_verified": true,
			"nonce":          st.Nonce,
			"exp":            time.Now().Add(time.Hour).Unix(),
		})
		google.conf.Endpoint.TokenURL = testTokenServer(t, idToken)

		req := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=code123&state="+url.QueryEscape(state), nil)
		req.AddCookie(stateCookie)
		req.RemoteAddr = remoteAddr
		rr := httptest.NewRecorder()
		a.handleOAuthCallback(google).ServeHTTP(rr, req)
		if rr.Code != http.StatusFound {
			t.Fatalf("callback code = %d (%s), want 302", rr.Code, rr.Body.String())
		}
		return rr
	}

	t.Run("new account from spanish IP gets es", func(t *testing.T) {
		google := configuredGoogle(t)
		google.verifier = newJWTVerifier(jwksURL, []string{"https://accounts.google.com"}, "client-id")
		a := newTestAuth(t, google)
		a.geoip = geoip

		login(a, google, "es-sub", spanishIP)

		lang, ok, err := a.users.PreferredLanguage("google:es-sub")
		if err != nil {
			t.Fatalf("PreferredLanguage: %v", err)
		}
		if !ok || lang != "es" {
			t.Errorf("PreferredLanguage = (%q, %v), want (\"es\", true)", lang, ok)
		}
	})

	t.Run("new account from unrecorded IP gets no preference", func(t *testing.T) {
		google := configuredGoogle(t)
		google.verifier = newJWTVerifier(jwksURL, []string{"https://accounts.google.com"}, "client-id")
		a := newTestAuth(t, google)
		a.geoip = geoip

		login(a, google, "unrecorded-sub", "192.0.2.1:5555") // TEST-NET-1, not in the table

		_, ok, err := a.users.PreferredLanguage("google:unrecorded-sub")
		if err != nil {
			t.Fatalf("PreferredLanguage: %v", err)
		}
		if ok {
			t.Errorf("PreferredLanguage ok = true, want false (falls back to defaultLang)")
		}
	})

	t.Run("returning account's login never overwrites its preference", func(t *testing.T) {
		google := configuredGoogle(t)
		google.verifier = newJWTVerifier(jwksURL, []string{"https://accounts.google.com"}, "client-id")
		a := newTestAuth(t, google)
		a.geoip = geoip

		login(a, google, "returning-sub", spanishIP) // first login: seeds "es"
		if err := a.users.SetLanguage("google:returning-sub", "fr"); err != nil {
			t.Fatalf("SetLanguage: %v", err)
		}

		login(a, google, "returning-sub", frenchIP) // second login, from a different country

		lang, ok, err := a.users.PreferredLanguage("google:returning-sub")
		if err != nil {
			t.Fatalf("PreferredLanguage: %v", err)
		}
		if !ok || lang != "fr" {
			t.Errorf("PreferredLanguage = (%q, %v), want (\"fr\", true) — login must not override an existing preference", lang, ok)
		}
	})
}

func TestGoogleCallbackRejectsStateMismatch(t *testing.T) {
	key, jwksURL := testJWKS(t)
	google := configuredGoogle(t)
	google.verifier = newJWTVerifier(jwksURL, []string{"https://accounts.google.com"}, "client-id")
	a := newTestAuth(t, google)

	_, st, stateCookie, _ := startOAuth(t, a, google)
	idToken := testIDToken(t, key, map[string]any{
		"iss": "https://accounts.google.com", "aud": "client-id", "sub": "s",
		"nonce": st.Nonce, "exp": time.Now().Add(time.Hour).Unix(),
	})
	google.conf.Endpoint.TokenURL = testTokenServer(t, idToken)

	req := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=code123&state=wrong", nil)
	req.AddCookie(stateCookie)
	rr := httptest.NewRecorder()
	a.handleOAuthCallback(google).ServeHTTP(rr, req)

	if rr.Code != http.StatusFound || !strings.HasPrefix(rr.Header().Get("Location"), "/login?error=") {
		t.Fatalf("callback = %d %q, want 302 to /login?error=", rr.Code, rr.Header().Get("Location"))
	}
	if cookie := findCookie(rr, sessionCookieName); cookie != nil {
		t.Error("session cookie issued despite state mismatch")
	}
}

func TestGoogleCallbackRejectsBadSignature(t *testing.T) {
	_, jwksURL := testJWKS(t)
	otherKey, _ := testJWKS(t) // key that is NOT in the JWKS
	google := configuredGoogle(t)
	google.verifier = newJWTVerifier(jwksURL, []string{"https://accounts.google.com"}, "client-id")
	a := newTestAuth(t, google)

	state, st, stateCookie, _ := startOAuth(t, a, google)
	idToken := testIDToken(t, otherKey, map[string]any{
		"iss": "https://accounts.google.com", "aud": "client-id", "sub": "s",
		"nonce": st.Nonce, "exp": time.Now().Add(time.Hour).Unix(),
	})
	google.conf.Endpoint.TokenURL = testTokenServer(t, idToken)

	req := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=code123&state="+url.QueryEscape(state), nil)
	req.AddCookie(stateCookie)
	rr := httptest.NewRecorder()
	a.handleOAuthCallback(google).ServeHTTP(rr, req)

	if cookie := findCookie(rr, sessionCookieName); cookie != nil {
		t.Error("session cookie issued for a token signed by the wrong key")
	}
}

func TestAppleCallbackCreatesUserAndSession(t *testing.T) {
	key, jwksURL := testJWKS(t)
	apple := configuredApple(t)
	apple.verifier = newJWTVerifier(jwksURL, []string{appleTokenAudience}, "client-id")
	a := newTestAuth(t, apple)

	state, st, stateCookie, location := startOAuth(t, a, apple)

	// Apple's authorize URL must carry the nonce and force form_post.
	loc, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse apple redirect: %v", err)
	}
	if loc.Query().Get("nonce") != st.Nonce {
		t.Errorf("apple redirect nonce = %q, want %q", loc.Query().Get("nonce"), st.Nonce)
	}
	if loc.Query().Get("response_mode") != "form_post" {
		t.Errorf("apple redirect response_mode = %q, want form_post", loc.Query().Get("response_mode"))
	}

	idToken := testIDToken(t, key, map[string]any{
		"iss":   appleTokenAudience,
		"aud":   "client-id",
		"sub":   "apple-sub",
		"email": "apple@example.com",
		"nonce": st.Nonce,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	apple.conf.Endpoint.TokenURL = testTokenServer(t, idToken)

	form := url.Values{
		"code":  {"code123"},
		"state": {state},
		"user":  {`{"name":{"firstName":"Ada","lastName":"Lovelace"}}`},
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/apple/callback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(stateCookie)
	rr := httptest.NewRecorder()
	a.handleOAuthCallback(apple).ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("callback code = %d (%s), want 302", rr.Code, rr.Body.String())
	}
	assertSessionUser(t, a, rr, "apple:apple-sub")
	u, ok, _ := a.users.Get("apple:apple-sub")
	if !ok || u.DisplayName != "Ada Lovelace" {
		t.Fatalf("persisted apple user = %+v (ok=%v)", u, ok)
	}
}

func TestAppleClientSecretIsSignedJWT(t *testing.T) {
	apple := configuredApple(t)
	secret, err := apple.clientSecret(time.Now())
	if err != nil {
		t.Fatalf("clientSecret: %v", err)
	}
	parts := strings.Split(secret, ".")
	if len(parts) != 3 {
		t.Fatalf("client secret is not a JWT: %q", secret)
	}

	headerBytes, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var header map[string]string
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "ES256" || header["kid"] != testKid {
		t.Errorf("header = %v, want alg ES256 kid %s", header, testKid)
	}

	// Verify the ES256 signature against the provider's own public key.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("bad signature encoding: len=%d err=%v", len(sig), err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&apple.privateKey.PublicKey, digest[:], r, s) {
		t.Fatal("client secret signature did not verify")
	}

	claimBytes, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != "TEAMID1234" || claims["sub"] != "client-id" || claims["aud"] != appleTokenAudience {
		t.Errorf("claims = %v", claims)
	}
}

func TestCallbackUnconfiguredProvider(t *testing.T) {
	google := newGoogleProvider(oauthEnv{})
	a := newTestAuth(t, google)
	rr := httptest.NewRecorder()
	a.handleOAuthCallback(google).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/auth/google/callback", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}

// assertSessionUser checks that the response set a valid session cookie for
// wantUser.
func assertSessionUser(t *testing.T, a *Auth, rr *httptest.ResponseRecorder, wantUser string) {
	t.Helper()
	c := findCookie(rr, sessionCookieName)
	if c == nil {
		t.Fatal("no session cookie set")
	}
	user, ok := a.signer.verifySession(c.Value)
	if !ok || user != wantUser {
		t.Fatalf("session user = (%q, %v), want (%q, true)", user, ok, wantUser)
	}
}

func findCookie(rr *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}
