package main

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"time"
)

// Account progress API. Like preferences.go, every route here is mounted
// behind Auth.require (see main.go), so handlers can rely on
// userIDFromContext and touch only the local SQLite store.

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

// handleGetProgress returns the account's quiz summary for the home and
// settings views.
func (s *Server) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	stats, err := s.users.ProgressStats(id, time.Now())
	if err != nil {
		log.Printf("get progress %s: %v", id, err)
		http.Error(w, "failed to load progress", http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

// handleGetMastered returns the species the account has mastered, with the
// names and photo the mastered view renders. It resolves names from the
// durable taxonomy store and the photo URL from the local image cache, so it
// never calls upstream.
func (s *Server) handleGetMastered(w http.ResponseWriter, r *http.Request) {
	lang, ok := s.resolveLang(w, r)
	if !ok {
		return
	}
	id := userIDFromContext(r)
	secondary, err := s.users.SecondaryLanguage(id)
	if err != nil {
		log.Printf("mastered secondary language %s: %v", id, err)
		http.Error(w, "failed to load mastered birds", http.StatusInternalServerError)
		return
	}
	birds, err := s.users.MasteredSpecies(id, lang, secondary)
	if err != nil {
		log.Printf("get mastered %s: %v", id, err)
		http.Error(w, "failed to load mastered birds", http.StatusInternalServerError)
		return
	}
	if s.cache != nil {
		for i := range birds {
			if birds[i].SciName == "" {
				continue
			}
			birds[i].ImageURL = s.cache.ImageURLFor(birds[i].SciName)
			birds[i].ImageMissing = s.cache.IsKnownMissing(birds[i].SciName)
		}
	}
	writeJSON(w, birds)
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

// handleResetProgress clears the account's quiz progress.
func (s *Server) handleResetProgress(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	if err := s.users.ResetProgress(id); err != nil {
		log.Printf("reset progress %s: %v", id, err)
		http.Error(w, "failed to reset progress", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
