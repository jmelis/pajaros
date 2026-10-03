package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Account preferences API. Every route here is mounted behind Auth.require
// (see main.go), so handlers can rely on userIDFromContext and need no
// auth logic of their own. They touch only the local SQLite store, never an
// upstream API.

// handleGetProfile returns the logged-in account's identity and current
// preferences in one call. A brand-new account comes back with the default
// language and an empty favorites list.
func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	p, ok, err := s.users.Profile(id)
	if err != nil {
		log.Printf("profile %s: %v", id, err)
		http.Error(w, "failed to load profile", http.StatusInternalServerError)
		return
	}
	if !ok {
		// The session is valid but the account row is gone (e.g. the database
		// was replaced). Treat it as missing rather than inventing a record.
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	writeJSON(w, p)
}

// handleGetLanguage returns the account's effective language: its stored
// preference, or the app default when unset.
func (s *Server) handleGetLanguage(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	lang, ok, err := s.users.PreferredLanguage(id)
	if err != nil {
		log.Printf("get language %s: %v", id, err)
		http.Error(w, "failed to load language", http.StatusInternalServerError)
		return
	}
	if !ok {
		lang = defaultLang
	}
	writeJSON(w, map[string]string{"language": lang})
}

// handleSetLanguage stores the account's preferred bird-name language,
// enforcing the same es/fr/en allow-list as the species endpoint's lang query
// parameter. The preference is what the species endpoint falls back to when no
// lang is supplied.
func (s *Server) handleSetLanguage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Language string `json:"language"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !validLang[body.Language] {
		http.Error(w, "unsupported language", http.StatusBadRequest)
		return
	}

	id := userIDFromContext(r)
	if err := s.users.SetLanguage(id, body.Language); err != nil {
		log.Printf("set language %s: %v", id, err)
		http.Error(w, "failed to save language", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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

// handleListFavorites returns the account's favorites, each with the name and
// details captured when it was bookmarked. It reads only local data.
func (s *Server) handleListFavorites(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	favs, err := s.users.Favorites(id)
	if err != nil {
		log.Printf("list favorites %s: %v", id, err)
		http.Error(w, "failed to load favorites", http.StatusInternalServerError)
		return
	}
	// A custom circle is described from the current places data, so favorites
	// saved before the labels existed get them too.
	for i, f := range favs {
		if !strings.HasPrefix(f.Key, "c") {
			continue
		}
		if info, err := s.areas.Info(f.Key); err == nil {
			favs[i].Name, favs[i].Region, favs[i].Country = info.Name, info.Region, info.Country
		}
	}
	writeJSON(w, favs)
}

// handleAddFavorite adds an area to the account's favorites. The key must name
// a real place or a valid custom circle; the stored details come from the
// server. Re-adding an existing favorite
// refreshes it. The per-account cap returns 409 Conflict rather than growing
// without limit.
func (s *Server) handleAddFavorite(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !validAreaKey(key) {
		http.Error(w, "invalid area key", http.StatusBadRequest)
		return
	}
	info, err := s.areas.Info(key)
	if err != nil {
		http.Error(w, "place not found", http.StatusNotFound)
		return
	}

	fav := Favorite{
		Key: info.Key, Name: info.Name, Kind: info.Kind, Country: info.Country,
		Lat: info.Lat, Lng: info.Lng, RadiusKm: info.RadiusKm,
	}

	id := userIDFromContext(r)
	err = s.users.AddFavorite(id, fav)
	if errors.Is(err, ErrFavoritesLimit) {
		http.Error(w, "favorites limit reached", http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("add favorite %s %s: %v", id, key, err)
		http.Error(w, "failed to add favorite", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxFavoriteNameRunes bounds the label a user can give a favorite.
const maxFavoriteNameRunes = 80

// handleRenameFavorite sets the label of one of the account's favorites; an
// empty name restores the place's own.
func (s *Server) handleRenameFavorite(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !validAreaKey(key) {
		http.Error(w, "invalid area key", http.StatusBadRequest)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	name := strings.Join(strings.Fields(body.Name), " ")
	if utf8.RuneCountInString(name) > maxFavoriteNameRunes || strings.ContainsFunc(name, unicode.IsControl) {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	id := userIDFromContext(r)
	ok, err := s.users.SetFavoriteName(id, key, name)
	if err != nil {
		log.Printf("rename favorite %s %s: %v", id, key, err)
		http.Error(w, "failed to rename favorite", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not a favorite", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveFavorite removes an area from the account's favorites. Removing
// one that isn't favorited is a success (no-op).
func (s *Server) handleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !validAreaKey(key) {
		http.Error(w, "invalid area key", http.StatusBadRequest)
		return
	}
	id := userIDFromContext(r)
	if err := s.users.RemoveFavorite(id, key); err != nil {
		log.Printf("remove favorite %s %s: %v", id, key, err)
		http.Error(w, "failed to remove favorite", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
