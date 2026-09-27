package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sessionCookieName is the cookie carrying the signed session token.
const sessionCookieName = "pajaros_session"

// sessionTTL is how long a login lasts before the user must sign in again.
const sessionTTL = 30 * 24 * time.Hour

// cookieSigner produces and verifies opaque, tamper-evident values: the
// session token and the short-lived OAuth state cookie. Both are just
// base64url(payload) + "." + base64url(HMAC-SHA256(key, payload)), so a
// tampered value fails verification rather than being silently trusted.
type cookieSigner struct {
	key []byte
}

// newCookieSigner uses secret when set. With no secret configured (the
// no-credentials startup case the plan calls out) it generates a random key,
// which means sessions simply don't survive a restart — fine for a dev
// instance, and better than refusing to start.
func newCookieSigner(secret string) (*cookieSigner, error) {
	if secret != "" {
		return &cookieSigner{key: []byte(secret)}, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}
	return &cookieSigner{key: key}, nil
}

func (s *cookieSigner) sign(payload []byte) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *cookieSigner) unsign(token string) ([]byte, bool) {
	payloadB64, sigB64, ok := strings.Cut(token, ".")
	if !ok {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, false
	}
	return payload, true
}

// sessionPayload is the signed cookie body. UserID is the persisted user
// record's stable id ("provider:subject").
type sessionPayload struct {
	UserID string `json:"uid"`
	Exp    int64  `json:"exp"`
}

func (s *cookieSigner) issueSession(userID string, ttl time.Duration) (string, error) {
	payload, err := json.Marshal(sessionPayload{
		UserID: userID,
		Exp:    time.Now().Add(ttl).Unix(),
	})
	if err != nil {
		return "", err
	}
	return s.sign(payload), nil
}

// verifySession returns the user id carried by a valid, unexpired session
// token.
func (s *cookieSigner) verifySession(token string) (string, bool) {
	payload, ok := s.unsign(token)
	if !ok {
		return "", false
	}
	var p sessionPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", false
	}
	if p.UserID == "" || time.Now().Unix() >= p.Exp {
		return "", false
	}
	return p.UserID, true
}
