package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleAnalytics(t *testing.T) {
	srv := &Server{}
	get := func() (string, string) {
		rr := httptest.NewRecorder()
		srv.handleAnalytics(rr, httptest.NewRequest(http.MethodGet, "/api/analytics", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", rr.Code)
		}
		var out struct{ ScriptURL, WebsiteID string }
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.ScriptURL, out.WebsiteID
	}

	t.Setenv("UMAMI_SCRIPT_URL", "")
	t.Setenv("UMAMI_WEBSITE_ID", "")
	if u, id := get(); u != "" || id != "" {
		t.Errorf("unset = %q, %q; want empty", u, id)
	}
	t.Setenv("UMAMI_SCRIPT_URL", "https://stats.example.com/script.js")
	if u, id := get(); u != "" || id != "" {
		t.Errorf("script without id = %q, %q; want both empty", u, id)
	}
	t.Setenv("UMAMI_WEBSITE_ID", " abc-123 ")
	if u, id := get(); u != "https://stats.example.com/script.js" || id != "abc-123" {
		t.Errorf("configured = %q, %q", u, id)
	}
}
