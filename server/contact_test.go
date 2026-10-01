package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newContactTestApp wires the contact routes behind the real auth gate, with a
// fake Resend endpoint that records what the server posts to it.
func newContactTestApp(t *testing.T, resendStatus int) (http.Handler, *Auth, *UserStore, *[]resendEmail, *[]string) {
	t.Helper()
	_, srv, store, auth := newPrefsTestApp(t)

	var sent []resendEmail
	var authHeaders []string
	resend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		var msg resendEmail
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Errorf("fake resend: bad body: %v", err)
		}
		sent = append(sent, msg)
		w.WriteHeader(resendStatus)
		_, _ = io.WriteString(w, `{"message":"upstream detail"}`)
	}))
	t.Cleanup(resend.Close)

	srv.contact = &contactMailer{
		apiKey:   "re_test_key",
		from:     "birdsnearby <noreply@example.com>",
		to:       "owner@example.com",
		endpoint: resend.URL,
		client:   resend.Client(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/contact", srv.handleContactStatus)
	mux.HandleFunc("POST /api/contact", auth.requireFunc(srv.handleSendContact))
	return mux, auth, store, &sent, &authHeaders
}

func postContact(t *testing.T, app http.Handler, auth *Auth, userID, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := authedRequest(t, auth, userID, http.MethodPost, "/api/contact", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	return rr
}

func TestContactStatusReflectsConfiguration(t *testing.T) {
	status := func(srv *Server) bool {
		rr := httptest.NewRecorder()
		srv.handleContactStatus(rr, httptest.NewRequest(http.MethodGet, "/api/contact", nil))
		var out struct{ Enabled bool }
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Enabled
	}
	if status(&Server{}) {
		t.Error("enabled without a mailer")
	}
	if !status(&Server{contact: &contactMailer{}}) {
		t.Error("disabled with a mailer")
	}
}

func TestNewContactMailerFromEnv(t *testing.T) {
	set := func(key, to, from string) {
		t.Setenv("RESEND_API_KEY", key)
		t.Setenv("CONTACT_TO", to)
		t.Setenv("CONTACT_FROM", from)
	}
	set("", "", "")
	if newContactMailerFromEnv() != nil {
		t.Error("nothing set: want nil")
	}
	set("re_x", "owner@example.com", "")
	if newContactMailerFromEnv() != nil {
		t.Error("partial config: want nil")
	}
	set(" re_x ", " owner@example.com ", " noreply@example.com ")
	m := newContactMailerFromEnv()
	if m == nil {
		t.Fatal("full config: want a mailer")
	}
	if m.apiKey != "re_x" || m.to != "owner@example.com" || m.from != "noreply@example.com" {
		t.Errorf("values not trimmed: %+v", m)
	}
}

func TestSendContactRequiresSignIn(t *testing.T) {
	app, _, _, sent, _ := newContactTestApp(t, http.StatusOK)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/contact", strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("guest = %d, want 401", rr.Code)
	}
	if len(*sent) != 0 {
		t.Errorf("guest message was sent: %+v", *sent)
	}
}

func TestSendContactDeliversWithReplyTo(t *testing.T) {
	app, auth, store, sent, authHeaders := newContactTestApp(t, http.StatusOK)
	u, err := store.Upsert("google", "s1", "visitor@example.com", "Vera Visitor")
	if err != nil {
		t.Fatal(err)
	}

	rr := postContact(t, app, auth, u.ID, "application/json", `{"message":"  Lovely site!  "}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("code = %d (%s), want 204", rr.Code, rr.Body.String())
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(*sent))
	}
	got := (*sent)[0]
	if got.To[0] != "owner@example.com" || got.From != "birdsnearby <noreply@example.com>" {
		t.Errorf("addressing: from=%q to=%v", got.From, got.To)
	}
	if got.ReplyTo != "visitor@example.com" {
		t.Errorf("reply_to = %q, want the visitor's account email", got.ReplyTo)
	}
	if got.Subject != "birdsnearby: message from Vera Visitor" {
		t.Errorf("subject = %q", got.Subject)
	}
	if !strings.Contains(got.Text, "Lovely site!") || !strings.Contains(got.Text, u.ID) {
		t.Errorf("text missing message or account id: %q", got.Text)
	}
	if (*authHeaders)[0] != "Bearer re_test_key" {
		t.Errorf("Authorization = %q", (*authHeaders)[0])
	}
}

func TestSendContactSanitisesDisplayName(t *testing.T) {
	app, auth, store, sent, _ := newContactTestApp(t, http.StatusOK)
	u, err := store.Upsert("google", "s2", "v@example.com", "Eve\r\nBcc: x@evil.test")
	if err != nil {
		t.Fatal(err)
	}
	rr := postContact(t, app, auth, u.ID, "application/json", `{"message":"hi"}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rr.Code)
	}
	if subj := (*sent)[0].Subject; strings.ContainsAny(subj, "\r\n") {
		t.Errorf("subject contains a line break: %q", subj)
	}
}

func TestSendContactValidation(t *testing.T) {
	app, auth, store, sent, _ := newContactTestApp(t, http.StatusOK)
	u, err := store.Upsert("google", "s3", "v@example.com", "V")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, contentType, body string
		want                    int
	}{
		{"empty", "application/json", `{"message":"   "}`, http.StatusBadRequest},
		{"missing", "application/json", `{}`, http.StatusBadRequest},
		{"not json", "application/json", `nope`, http.StatusBadRequest},
		{"too long", "application/json", `{"message":"` + strings.Repeat("a", maxContactRunes+1) + `"}`, http.StatusBadRequest},
		{"form post", "application/x-www-form-urlencoded", `message=hi`, http.StatusUnsupportedMediaType},
		{"no content type", "", `{"message":"hi"}`, http.StatusUnsupportedMediaType},
	}
	for _, c := range cases {
		if rr := postContact(t, app, auth, u.ID, c.contentType, c.body); rr.Code != c.want {
			t.Errorf("%s: code = %d, want %d", c.name, rr.Code, c.want)
		}
	}
	if len(*sent) != 0 {
		t.Errorf("invalid requests reached Resend: %+v", *sent)
	}

	exact := `{"message":"` + strings.Repeat("é", maxContactRunes) + `"}`
	if rr := postContact(t, app, auth, u.ID, "application/json", exact); rr.Code != http.StatusNoContent {
		t.Errorf("message of exactly %d runes = %d, want 204", maxContactRunes, rr.Code)
	}
}

func TestSendContactUpstreamFailureIsBadGateway(t *testing.T) {
	app, auth, store, _, _ := newContactTestApp(t, http.StatusUnprocessableEntity)
	u, err := store.Upsert("google", "s4", "v@example.com", "V")
	if err != nil {
		t.Fatal(err)
	}
	rr := postContact(t, app, auth, u.ID, "application/json", `{"message":"hi"}`)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "upstream detail") {
		t.Errorf("upstream error detail leaked to the client: %q", rr.Body.String())
	}
}

func TestSendContactUnconfigured(t *testing.T) {
	_, srv, store, auth := newPrefsTestApp(t)
	u, err := store.Upsert("google", "s5", "v@example.com", "V")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/contact", auth.requireFunc(srv.handleSendContact))
	rr := postContact(t, mux, auth, u.ID, "application/json", `{"message":"hi"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", rr.Code)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("  a\r\nb\tc  ", 80); got != "a b c" {
		t.Errorf("got %q", got)
	}
	if got := oneLine("héllo", 3); got != "hél" {
		t.Errorf("got %q, want rune-aware truncation", got)
	}
}
