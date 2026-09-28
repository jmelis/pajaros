package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
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

// handleListFavorites returns the account's favorites, each with the display
// name and coordinates captured when it was bookmarked. It reads only local
// data — no upstream call per favorite.
func (s *Server) handleListFavorites(w http.ResponseWriter, r *http.Request) {
	id := userIDFromContext(r)
	favs, err := s.users.Favorites(id)
	if err != nil {
		log.Printf("list favorites %s: %v", id, err)
		http.Error(w, "failed to load favorites", http.StatusInternalServerError)
		return
	}
	writeJSON(w, favs)
}

// favoriteBody is what a client may send when bookmarking: the hotspot's name
// and coordinates, both optional. locId comes from the path and is not part
// of the body.
type favoriteBody struct {
	LocName string  `json:"locName"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

// handleAddFavorite adds locId to the account's favorites, recording the
// supplied name/coordinates so the home list can render it later. Re-adding
// an existing favorite is a success (a no-op unless it supplies missing
// details). locId must match the same pattern the hotspot endpoints accept.
// The per-account cap returns 409 Conflict rather than growing without limit.
func (s *Server) handleAddFavorite(w http.ResponseWriter, r *http.Request) {
	locID := r.PathValue("locId")
	if !validLocID(locID) {
		http.Error(w, "invalid locId", http.StatusBadRequest)
		return
	}

	// An absent body is fine: the client may be re-adding purely idempotently.
	var body favoriteBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	id := userIDFromContext(r)
	err := s.users.AddFavorite(id, Favorite{
		LocID:   locID,
		LocName: body.LocName,
		Lat:     body.Lat,
		Lng:     body.Lng,
	})
	if errors.Is(err, ErrFavoritesLimit) {
		http.Error(w, "favorites limit reached", http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("add favorite %s %s: %v", id, locID, err)
		http.Error(w, "failed to add favorite", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveFavorite removes locId from the account's favorites. Removing
// one that isn't favorited is a success (no-op).
func (s *Server) handleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	locID := r.PathValue("locId")
	if !validLocID(locID) {
		http.Error(w, "invalid locId", http.StatusBadRequest)
		return
	}
	id := userIDFromContext(r)
	if err := s.users.RemoveFavorite(id, locID); err != nil {
		log.Printf("remove favorite %s %s: %v", id, locID, err)
		http.Error(w, "failed to remove favorite", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
