package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleContact(t *testing.T) {
	srv := &Server{}
	get := func() string {
		rr := httptest.NewRecorder()
		srv.handleContact(rr, httptest.NewRequest(http.MethodGet, "/api/contact", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", rr.Code)
		}
		var out struct{ Email string }
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Email
	}

	t.Setenv("CONTACT_EMAIL", "")
	if got := get(); got != "" {
		t.Errorf("unset email = %q, want empty", got)
	}
	t.Setenv("CONTACT_EMAIL", "  hello@example.com ")
	if got := get(); got != "hello@example.com" {
		t.Errorf("email = %q, want trimmed hello@example.com", got)
	}
}
