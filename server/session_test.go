package main

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	s, err := newCookieSigner("test-secret")
	if err != nil {
		t.Fatalf("newCookieSigner: %v", err)
	}
	token, err := s.issueSession("google:12345", time.Hour)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}
	got, ok := s.verifySession(token)
	if !ok || got != "google:12345" {
		t.Fatalf("verifySession = (%q, %v), want (google:12345, true)", got, ok)
	}
}

func TestSessionRejectsTamperedToken(t *testing.T) {
	s, _ := newCookieSigner("test-secret")
	token, _ := s.issueSession("google:1", time.Hour)

	// Flip a bit in the signature bytes (not the base64 text — its last
	// character's low bits are padding and can be changed without altering
	// the decoded signature).
	payloadB64, sigB64, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatalf("token %q has no signature separator", token)
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatal(err)
	}
	sig[0] ^= 0x01
	tampered := payloadB64 + "." + base64.RawURLEncoding.EncodeToString(sig)
	if _, ok := s.verifySession(tampered); ok {
		t.Fatal("verifySession accepted a tampered signature")
	}

	// A different key must not validate the same token.
	other, _ := newCookieSigner("different-secret")
	if _, ok := other.verifySession(token); ok {
		t.Fatal("verifySession accepted a token signed with a different key")
	}

	// Garbage must be rejected rather than panic.
	if _, ok := s.verifySession("not-a-token"); ok {
		t.Fatal("verifySession accepted malformed input")
	}
}

func TestSessionRejectsExpiredToken(t *testing.T) {
	s, _ := newCookieSigner("test-secret")
	token, _ := s.issueSession("google:1", -time.Minute)
	if _, ok := s.verifySession(token); ok {
		t.Fatal("verifySession accepted an expired token")
	}
}

func TestCookieSignerRoundTrip(t *testing.T) {
	s, _ := newCookieSigner("test-secret")
	payload := []byte(`{"hello":"world"}`)
	got, ok := s.unsign(s.sign(payload))
	if !ok || string(got) != string(payload) {
		t.Fatalf("unsign(sign(payload)) = (%q, %v), want (%q, true)", got, ok, payload)
	}
	if _, ok := s.unsign("aa.bb"); ok {
		t.Fatal("unsign accepted an unsigned/garbage value")
	}
}

func TestNewCookieSignerWithoutSecretGeneratesKey(t *testing.T) {
	s, err := newCookieSigner("")
	if err != nil {
		t.Fatalf("newCookieSigner(\"\"): %v", err)
	}
	if len(s.key) == 0 {
		t.Fatal("expected a generated key")
	}
}
