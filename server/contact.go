package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	resendEndpoint     = "https://api.resend.com/emails"
	maxContactRunes    = 4000
	maxContactBodySize = 32 << 10
)

// contactMailer delivers visitor messages to the site owner through Resend.
// The owner's address (CONTACT_TO) only ever lives in the environment, never
// in the page or the source.
type contactMailer struct {
	apiKey   string
	from     string
	to       string
	endpoint string
	client   *http.Client
}

// newContactMailerFromEnv returns nil, which disables the contact form, unless
// RESEND_API_KEY, CONTACT_TO and CONTACT_FROM are all set. Setting only some
// of them is logged, since it is almost certainly a deployment mistake.
func newContactMailerFromEnv() *contactMailer {
	m := &contactMailer{
		apiKey:   strings.TrimSpace(os.Getenv("RESEND_API_KEY")),
		from:     strings.TrimSpace(os.Getenv("CONTACT_FROM")),
		to:       strings.TrimSpace(os.Getenv("CONTACT_TO")),
		endpoint: resendEndpoint,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
	switch {
	case m.apiKey == "" && m.from == "" && m.to == "":
		return nil
	case m.apiKey == "" || m.from == "" || m.to == "":
		log.Printf("contact form disabled: RESEND_API_KEY, CONTACT_FROM and CONTACT_TO must all be set")
		return nil
	}
	return m
}

type resendEmail struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	ReplyTo string   `json:"reply_to,omitempty"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
}

func (m *contactMailer) send(ctx context.Context, msg resendEmail) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("resend: status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}

// handleContactStatus tells the About page whether the contact form exists,
// so it can omit the form when the deployment has no mail configuration.
func (s *Server) handleContactStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"enabled": s.contact != nil})
}

// handleSendContact emails a signed-in visitor's message to the site owner,
// with the visitor's account email as Reply-To. Only signed-in accounts can
// reach it (Auth.require), which is what keeps bots out without a captcha.
func (s *Server) handleSendContact(w http.ResponseWriter, r *http.Request) {
	if s.contact == nil {
		http.Error(w, "contact form not configured", http.StatusServiceUnavailable)
		return
	}
	// A cross-site <form> post can't send application/json, so this also
	// closes the CSRF gap that Lax cookies leave for top-level navigations.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var in struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxContactBodySize)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(in.Message)
	if message == "" {
		http.Error(w, "message is empty", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(message) > maxContactRunes {
		http.Error(w, "message too long", http.StatusBadRequest)
		return
	}

	id := userIDFromContext(r)
	p, ok, err := s.users.Profile(id)
	if err != nil {
		log.Printf("contact: profile %s: %v", id, err)
		http.Error(w, "failed to load profile", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	name := oneLine(p.DisplayName, 80)
	if name == "" {
		name = oneLine(p.Email, 80)
	}
	err = s.contact.send(r.Context(), resendEmail{
		From:    s.contact.from,
		To:      []string{s.contact.to},
		ReplyTo: oneLine(p.Email, 254),
		Subject: "birdsnearby: message from " + name,
		Text:    fmt.Sprintf("From: %s <%s>\nAccount: %s\n\n%s\n", name, p.Email, p.ID, message),
	})
	if err != nil {
		log.Printf("contact: send for %s: %v", id, err)
		http.Error(w, "failed to send message", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// oneLine collapses control characters (newlines included) to spaces and caps
// the length, so a display name can't break out of a subject or From line.
func oneLine(s string, maxRunes int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes])
	}
	return s
}
