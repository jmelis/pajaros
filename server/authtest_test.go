package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"html/template"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testKid = "test-kid"

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// testJWKS returns an RSA key and the URL of a JWKS server publishing its
// public half. Used to exercise the ID-token verification path without
// touching Google's or Apple's real endpoints.
func testJWKS(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	doc := map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"kid": testKid,
			"use": "sig",
			"alg": "RS256",
			"n":   b64(key.N.Bytes()),
			"e":   b64(big.NewInt(int64(key.E)).Bytes()),
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return key, srv.URL
}

func testIDToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "kid": testKid, "typ": "JWT"}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := b64(hb) + "." + b64(cb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign id token: %v", err)
	}
	return signingInput + "." + b64(sig)
}

// testTokenServer answers an OAuth token request with the given ID token.
func testTokenServer(t *testing.T, idToken string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func testECKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal EC key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func newTestAuth(t *testing.T, providers ...oauthProvider) *Auth {
	t.Helper()
	users, err := NewUserStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	signer, err := newCookieSigner("test-secret")
	if err != nil {
		t.Fatalf("newCookieSigner: %v", err)
	}
	tmpl, err := template.ParseFS(loginPageFS, "static/login.html")
	if err != nil {
		t.Fatalf("parse login template: %v", err)
	}
	byName := make(map[string]oauthProvider, len(providers))
	for _, p := range providers {
		byName[p.name()] = p
	}
	return &Auth{signer: signer, users: users, providers: providers, byName: byName, loginTmpl: tmpl}
}

func configuredGoogle(t *testing.T) *googleProvider {
	t.Helper()
	return newGoogleProvider(oauthEnv{
		redirectBase:       "https://app.test",
		googleClientID:     "client-id",
		googleClientSecret: "client-secret",
	})
}

func configuredApple(t *testing.T) *appleProvider {
	t.Helper()
	p, err := newAppleProvider(oauthEnv{
		redirectBase:    "https://app.test",
		appleTeamID:     "TEAMID1234",
		appleServicesID: "client-id",
		appleKeyID:      testKid,
		applePrivateKey: testECKeyPEM(t),
	})
	if err != nil {
		t.Fatalf("newAppleProvider: %v", err)
	}
	if !p.configured() {
		t.Fatal("apple provider should be configured")
	}
	return p
}

// startOAuth drives handleOAuthStart and returns the state value, the parsed
// signed state cookie, and the state cookie itself.
func startOAuth(t *testing.T, a *Auth, p oauthProvider) (string, *oauthState, *http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/"+p.name(), nil)
	rr := httptest.NewRecorder()
	a.handleOAuthStart(p).ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("start %s: code = %d, want 302", p.name(), rr.Code)
	}

	var stateCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == oauthStateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatalf("start %s: no state cookie set", p.name())
	}
	payload, ok := a.signer.unsign(stateCookie.Value)
	if !ok {
		t.Fatalf("start %s: state cookie not verifiable", p.name())
	}
	var st oauthState
	if err := json.Unmarshal(payload, &st); err != nil {
		t.Fatalf("start %s: state cookie malformed: %v", p.name(), err)
	}
	return st.State, &st, stateCookie, rr.Header().Get("Location")
}
