package main

import (
	"net/http"
	"os"
	"strings"
)

// handleContact serves the public contact address shown on the Contact page.
// It comes from CONTACT_EMAIL so the address is an explicit deployment choice
// and never ships in the source; unset, the page simply omits the line.
func (s *Server) handleContact(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"email": strings.TrimSpace(os.Getenv("CONTACT_EMAIL"))})
}
