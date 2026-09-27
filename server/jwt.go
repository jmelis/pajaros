package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// idTokenClaims is the subset of OIDC ID-token claims this app reads. Aud is
// decoded flexibly because OIDC allows it to be either a string or an array
// of strings, and EmailVerified likewise arrives as a JSON bool from Google
// but (sometimes) as the string "true" from Apple.
type idTokenClaims struct {
	Issuer        string          `json:"iss"`
	Audience      stringList      `json:"aud"`
	Subject       string          `json:"sub"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
	Nonce         string          `json:"nonce"`
	Expiry        int64           `json:"exp"`
	IssuedAt      int64           `json:"iat"`
}

func (c idTokenClaims) emailVerified() bool {
	if len(c.EmailVerified) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(c.EmailVerified, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(c.EmailVerified, &s); err == nil {
		return strings.EqualFold(s, "true")
	}
	return false
}

// stringList accepts either a JSON string or an array of strings.
type stringList []string

func (l *stringList) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*l = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

func (l stringList) contains(v string) bool {
	for _, s := range l {
		if s == v {
			return true
		}
	}
	return false
}

// jwtVerifier validates an RS256 ID token against a provider's JWKS. It is
// the single verification path shared by Google and Apple, which is also why
// it's straightforward to exercise in tests with a local JWKS server.
type jwtVerifier struct {
	jwksURL  string
	issuers  []string
	audience string
	client   *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

const jwksCacheTTL = time.Hour

func newJWTVerifier(jwksURL string, issuers []string, audience string) *jwtVerifier {
	return &jwtVerifier{
		jwksURL:  jwksURL,
		issuers:  issuers,
		audience: audience,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// verify checks the token's signature against the provider's JWKS and then
// its issuer, audience, expiry, and nonce. A non-empty nonce is required:
// both providers are always sent one (see oauth.go).
func (v *jwtVerifier) verify(ctx context.Context, rawToken, nonce string) (*idTokenClaims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("id token is not a JWT")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode JWT header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("parse JWT header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported id token alg %q", header.Alg)
	}

	key, err := v.signingKey(ctx, header.Kid)
	if err != nil {
		return nil, err
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode JWT signature: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("id token signature: %w", err)
	}

	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode JWT claims: %w", err)
	}
	var claims idTokenClaims
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		return nil, fmt.Errorf("parse JWT claims: %w", err)
	}

	if !containsString(v.issuers, claims.Issuer) {
		return nil, fmt.Errorf("id token issuer %q not allowed", claims.Issuer)
	}
	if v.audience != "" && !claims.Audience.contains(v.audience) {
		return nil, fmt.Errorf("id token audience %v does not match client id", claims.Audience)
	}
	if claims.Expiry != 0 && time.Now().Unix() >= claims.Expiry {
		return nil, fmt.Errorf("id token expired")
	}
	if nonce == "" || !hmacEqualString(claims.Nonce, nonce) {
		return nil, fmt.Errorf("id token nonce mismatch")
	}
	if claims.Subject == "" {
		return nil, fmt.Errorf("id token has no subject")
	}
	return &claims, nil
}

func (v *jwtVerifier) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	now := time.Now()
	if v.keys == nil || now.Sub(v.fetchedAt) > jwksCacheTTL {
		if err := v.fetchLocked(ctx); err != nil {
			return nil, err
		}
	}
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	// A key we don't know about could be a rotation; refetch once and retry
	// before giving up.
	if err := v.fetchLocked(ctx); err != nil {
		return nil, err
	}
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("no JWKS key for kid %q", kid)
}

func (v *jwtVerifier) fetchLocked(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch JWKS: unexpected status %d", resp.StatusCode)
	}
	var doc jwksDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return fmt.Errorf("JWKS contained no usable RSA keys")
	}
	v.keys = keys
	v.fetchedAt = time.Now()
	return nil
}

func (k jwk) publicKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return nil, fmt.Errorf("invalid RSA exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// hmacEqualString is a constant-time string comparison for the nonce check.
func hmacEqualString(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
