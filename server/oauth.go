package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// oauthProvider is the provider-neutral surface the login handlers use. Two
// implementations exist (Google, Apple); adding a third would only need to
// satisfy this interface and be appended in newAuth.
type oauthProvider interface {
	name() string
	displayName() string
	// configured reports whether enough env vars are present to offer this
	// provider. Only enabled providers reach the login page or routes, so an
	// unconfigured provider here is a startup error, not a disabled button;
	// see newAuth.
	configured() bool
	// authCodeURL builds the provider's authorization redirect. verifier is
	// the PKCE code verifier ("" if the provider doesn't support PKCE).
	authCodeURL(state, verifier, nonce string) string
	// exchange trades the callback code for tokens and returns the ID token.
	exchange(ctx context.Context, code, verifier string) (string, error)
	// verifyIDToken validates a raw ID token against the provider's JWKS.
	verifyIDToken(ctx context.Context, rawIDToken, nonce string) (*idTokenClaims, error)
}

// oauthEnv is the OAuth configuration read from the environment. Credentials
// are only ever used for a provider whose enable flag is truthy; with neither
// flag set the server starts with no login gate at all (see newAuth). The
// server must start with none of it set.
type oauthEnv struct {
	// googleEnabled / appleEnabled are the explicit GOOGLE_AUTH_ENABLED /
	// APPLE_AUTH_ENABLED flags. Credentials being present no longer turns a
	// provider on by itself; the flag must say so.
	googleEnabled bool
	appleEnabled  bool

	// RedirectBase is the externally reachable base URL of this server, e.g.
	// "https://birdquiz.example.com". Callback URLs are derived from it.
	redirectBase string

	googleClientID     string
	googleClientSecret string

	appleTeamID     string
	appleServicesID string
	appleKeyID      string
	applePrivateKey string
}

func loadOAuthEnv() oauthEnv {
	return oauthEnv{
		googleEnabled:      envTruthy(os.Getenv("GOOGLE_AUTH_ENABLED")),
		appleEnabled:       envTruthy(os.Getenv("APPLE_AUTH_ENABLED")),
		redirectBase:       os.Getenv("OAUTH_REDIRECT_BASE_URL"),
		googleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		googleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		appleTeamID:        os.Getenv("APPLE_TEAM_ID"),
		appleServicesID:    os.Getenv("APPLE_SERVICES_ID"),
		appleKeyID:         os.Getenv("APPLE_KEY_ID"),
		applePrivateKey:    os.Getenv("APPLE_PRIVATE_KEY"),
	}
}

// envTruthy reports whether an environment variable counts as "on". Only a
// recognized truthy value enables a feature; unset, empty, or anything else
// (including "false" and "0") leaves it off.
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// missingGoogleVars lists the env var names a Google-enabled server still
// needs. Used to name exactly what's missing in the fatal startup error.
func (env oauthEnv) missingGoogleVars() []string {
	var missing []string
	if env.googleClientID == "" {
		missing = append(missing, "GOOGLE_CLIENT_ID")
	}
	if env.googleClientSecret == "" {
		missing = append(missing, "GOOGLE_CLIENT_SECRET")
	}
	if env.redirectBase == "" {
		missing = append(missing, "OAUTH_REDIRECT_BASE_URL")
	}
	return missing
}

// missingAppleVars lists the env var names an Apple-enabled server still
// needs. A malformed APPLE_PRIVATE_KEY is reported separately by
// newAppleProvider.
func (env oauthEnv) missingAppleVars() []string {
	var missing []string
	if env.appleTeamID == "" {
		missing = append(missing, "APPLE_TEAM_ID")
	}
	if env.appleServicesID == "" {
		missing = append(missing, "APPLE_SERVICES_ID")
	}
	if env.appleKeyID == "" {
		missing = append(missing, "APPLE_KEY_ID")
	}
	if env.applePrivateKey == "" {
		missing = append(missing, "APPLE_PRIVATE_KEY")
	}
	if env.redirectBase == "" {
		missing = append(missing, "OAUTH_REDIRECT_BASE_URL")
	}
	return missing
}

// -- Google ------------------------------------------------------------------

type googleProvider struct {
	conf     *oauth2.Config
	verifier *jwtVerifier
}

func newGoogleProvider(env oauthEnv) *googleProvider {
	p := &googleProvider{
		verifier: newJWTVerifier(
			"https://www.googleapis.com/oauth2/v3/certs",
			[]string{"https://accounts.google.com", "accounts.google.com"},
			env.googleClientID,
		),
	}
	if env.googleClientID == "" || env.googleClientSecret == "" || env.redirectBase == "" {
		return p
	}
	p.conf = &oauth2.Config{
		ClientID:     env.googleClientID,
		ClientSecret: env.googleClientSecret,
		RedirectURL:  strings.TrimRight(env.redirectBase, "/") + "/auth/google/callback",
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
	}
	return p
}

func (p *googleProvider) name() string        { return "google" }
func (p *googleProvider) displayName() string { return "Google" }
func (p *googleProvider) configured() bool    { return p.conf != nil }

func (p *googleProvider) authCodeURL(state, verifier, nonce string) string {
	return p.conf.AuthCodeURL(state,
		oauth2.AccessTypeOnline,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("nonce", nonce),
	)
}

func (p *googleProvider) exchange(ctx context.Context, code, verifier string) (string, error) {
	tok, err := p.conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", err
	}
	idToken, _ := tok.Extra("id_token").(string)
	if idToken == "" {
		return "", fmt.Errorf("google token response contained no id_token")
	}
	return idToken, nil
}

func (p *googleProvider) verifyIDToken(ctx context.Context, rawIDToken, nonce string) (*idTokenClaims, error) {
	return p.verifier.verify(ctx, rawIDToken, nonce)
}

// -- Apple -------------------------------------------------------------------

const appleTokenAudience = "https://appleid.apple.com"

type appleProvider struct {
	teamID      string
	servicesID  string
	keyID       string
	privateKey  *ecdsa.PrivateKey
	redirectURL string

	conf     *oauth2.Config
	verifier *jwtVerifier
}

// newAppleProvider returns a provider that is unconfigured (not an error the
// caller must handle) when any required env var is missing or the private key
// can't be parsed. A parse failure is still reported via the returned error
// so the operator gets a log line explaining why Sign in with Apple is off.
func newAppleProvider(env oauthEnv) (*appleProvider, error) {
	p := &appleProvider{
		teamID:     env.appleTeamID,
		servicesID: env.appleServicesID,
		keyID:      env.appleKeyID,
	}
	p.verifier = newJWTVerifier(
		"https://appleid.apple.com/auth/keys",
		[]string{appleTokenAudience},
		env.appleServicesID,
	)

	if env.appleTeamID == "" || env.appleServicesID == "" || env.appleKeyID == "" || env.applePrivateKey == "" || env.redirectBase == "" {
		return p, nil
	}
	key, err := parseApplePrivateKey(env.applePrivateKey)
	if err != nil {
		return p, fmt.Errorf("parse APPLE_PRIVATE_KEY: %w", err)
	}
	p.privateKey = key
	p.redirectURL = strings.TrimRight(env.redirectBase, "/") + "/auth/apple/callback"
	p.conf = &oauth2.Config{
		ClientID:    env.appleServicesID,
		RedirectURL: p.redirectURL,
		Scopes:      []string{"name", "email"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://appleid.apple.com/auth/authorize",
			TokenURL: "https://appleid.apple.com/auth/token",
			// Apple wants client_id/client_secret in the POST body, not
			// HTTP Basic, so AuthStyleInParams is required.
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	return p, nil
}

func (p *appleProvider) name() string        { return "apple" }
func (p *appleProvider) displayName() string { return "Apple" }
func (p *appleProvider) configured() bool    { return p.conf != nil }

// authCodeURL requests response_mode=form_post (required by Apple when
// asking for name/email) and carries a nonce that the returned ID token must
// echo. Apple's web flow doesn't support PKCE, so verifier is ignored.
func (p *appleProvider) authCodeURL(state, _, nonce string) string {
	return p.conf.AuthCodeURL(state,
		oauth2.AccessTypeOnline,
		oauth2.SetAuthURLParam("response_mode", "form_post"),
		oauth2.SetAuthURLParam("nonce", nonce),
	)
}

func (p *appleProvider) exchange(ctx context.Context, code, _ string) (string, error) {
	secret, err := p.clientSecret(time.Now())
	if err != nil {
		return "", fmt.Errorf("generate apple client secret: %w", err)
	}
	// Copy the config so a concurrent request can't observe the rotating
	// client secret mid-flight.
	conf := *p.conf
	conf.ClientSecret = secret
	tok, err := conf.Exchange(ctx, code)
	if err != nil {
		return "", err
	}
	idToken, _ := tok.Extra("id_token").(string)
	if idToken == "" {
		return "", fmt.Errorf("apple token response contained no id_token")
	}
	return idToken, nil
}

func (p *appleProvider) verifyIDToken(ctx context.Context, rawIDToken, nonce string) (*idTokenClaims, error) {
	return p.verifier.verify(ctx, rawIDToken, nonce)
}

// clientSecret builds the short-lived ES256 JWT Apple uses as a client
// secret: signed with the .p8 private key, valid for at most six months.
func (p *appleProvider) clientSecret(now time.Time) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": p.keyID, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": p.teamID,
		"iat": now.Unix(),
		"exp": now.Add(24 * time.Hour).Unix(),
		"aud": appleTokenAudience,
		"sub": p.servicesID,
	})
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, p.privateKey, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseApplePrivateKey accepts the raw .p8 contents (PKCS#8 PEM). Newlines
// are often flattened to "\n" when the key is passed through an env var, so
// restore them before decoding.
func parseApplePrivateKey(raw string) (*ecdsa.PrivateKey, error) {
	raw = strings.ReplaceAll(raw, `\n`, "\n")
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, fmt.Errorf("not a PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("expected an ECDSA (P-256) key, got %T", parsed)
	}
	return key, nil
}

// logConfiguredProviders writes a one-line summary at startup so an operator
// can see at a glance which login options are live.
func logConfiguredProviders(providers []oauthProvider) {
	var enabled []string
	for _, p := range providers {
		if p.configured() {
			enabled = append(enabled, p.displayName())
		}
	}
	if len(enabled) == 0 {
		log.Printf("no OAuth providers enabled")
		return
	}
	log.Printf("OAuth providers enabled: %s", strings.Join(enabled, ", "))
}
