package main

import (
	"net/http"
	"os"
	"strings"
)

// handleAnalytics tells the frontend where the self-hosted Umami instance
// lives. Both values come from the environment so the deployment, not the
// source, decides whether usage is measured; unset, the frontend loads nothing.
func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	scriptURL := strings.TrimSpace(os.Getenv("UMAMI_SCRIPT_URL"))
	websiteID := strings.TrimSpace(os.Getenv("UMAMI_WEBSITE_ID"))
	if scriptURL == "" || websiteID == "" {
		scriptURL, websiteID = "", ""
	}
	writeJSON(w, map[string]string{"scriptUrl": scriptURL, "websiteId": websiteID})
}
