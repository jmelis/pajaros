package main

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"time"
)

// Account progress + display preferences API. Like preferences.go, every
// route here is mounted behind Auth.require (see main.go), so handlers can
// rely on userIDFromContext and touch only the local SQLite store.

// defaultDisplayMode is the sober look every account starts in; "kid" is the
// warmer, reward-heavy skin.
const defaultDisplayMode = "standard"

// validDisplayModes is the allow-list of display modes a PUT accepts.
var validDisplayModes = map[string]bool{
	defaultDisplayMode: true,
	"kid":              true,
}

// validSpeciesCode gates the {speciesCode} path value on progress writes.
// eBird species codes are short lowercase alphanumeric strings (e.g. "norcar");
// this is intentionally a little permissive so a future taxonomy spelling
// isn't rejected, while still keeping junk out of the database.
var validSpeciesCode = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`).MatchString

// handleGetSecondaryLanguage returns the account's optional second subtitle
// language, or "" when none is set.
func (s *Server) handleGetSecondaryLanguage(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	lang, err := s.users.SecondaryLanguage(id)
	if err != nil {
		log.Printf("get secondary language %s: %v", id, err)
		http.Error(w, "failed to load secondary language", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"language": lang})
}

// handleSetSecondaryLanguage stores (or, for "", clears) the account's second
// subtitle language. Unlike the primary language, an empty value is valid and
// means "unset".
func (s *Server) handleSetSecondaryLanguage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Language string `json:"language"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Language != "" && !validLang[body.Language] {
		http.Error(w, "unsupported language", http.StatusBadRequest)
		return
	}

	id := userIDFromContext(r)
	if err := s.users.SetSecondaryLanguage(id, body.Language); err != nil {
		log.Printf("set secondary language %s: %v", id, err)
		http.Error(w, "failed to save secondary language", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetDisplayMode returns the account's display mode ("standard"/"kid").
func (s *Server) handleGetDisplayMode(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	mode, err := s.users.DisplayMode(id)
	if err != nil {
		log.Printf("get display mode %s: %v", id, err)
		http.Error(w, "failed to load display mode", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"displayMode": mode})
}

// handleSetDisplayMode stores the account's display mode, enforcing the
// standard/kid allow-list.
func (s *Server) handleSetDisplayMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayMode string `json:"displayMode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !validDisplayModes[body.DisplayMode] {
		http.Error(w, "unsupported display mode", http.StatusBadRequest)
		return
	}

	id := userIDFromContext(r)
	if err := s.users.SetDisplayMode(id, body.DisplayMode); err != nil {
		log.Printf("set display mode %s: %v", id, err)
		http.Error(w, "failed to save display mode", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetProgress returns the account's quiz summary for the settings view.
func (s *Server) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	stats, err := s.users.ProgressStats(id)
	if err != nil {
		log.Printf("get progress %s: %v", id, err)
		http.Error(w, "failed to load progress", http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

// handleSubmitAnswer records one quiz answer for speciesCode and returns the
// resulting Leitner state.
func (s *Server) handleSubmitAnswer(w http.ResponseWriter, r *http.Request) {
	speciesCode := r.PathValue("speciesCode")
	if !validSpeciesCode(speciesCode) {
		http.Error(w, "invalid speciesCode", http.StatusBadRequest)
		return
	}
	var body struct {
		Correct bool `json:"correct"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	id := userIDFromContext(r)
	answer, err := s.users.AnswerCard(id, speciesCode, body.Correct, time.Now())
	if err != nil {
		log.Printf("answer card %s %s: %v", id, speciesCode, err)
		http.Error(w, "failed to save progress", http.StatusInternalServerError)
		return
	}
	writeJSON(w, answer)
}

// handleResetProgress clears the account's quiz progress and stars.
func (s *Server) handleResetProgress(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	if err := s.users.ResetProgress(id); err != nil {
		log.Printf("reset progress %s: %v", id, err)
		http.Error(w, "failed to reset progress", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
