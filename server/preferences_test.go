package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newPrefsTestApp wires the preference routes (and the species route) behind
// the same auth gate as main.go, using a fresh database and a fresh file
// cache, and returns everything a test needs to seed or assert.
func newPrefsTestApp(t *testing.T) (http.Handler, *Server, *UserStore, *Auth) {
	t.Helper()

	store, _ := newTestStore(t)
	cacheDir := t.TempDir()
	imgCache, err := NewImageCache(cacheDir)
	if err != nil {
		t.Fatalf("NewImageCache: %v", err)
	}
	srv := &Server{users: store, cache: imgCache, species: newFakeSpeciesSource()}

	signer, err := newCookieSigner("test-secret")
	if err != nil {
		t.Fatalf("newCookieSigner: %v", err)
	}
	auth := &Auth{signer: signer, users: store}

	app := http.NewServeMux()
	app.HandleFunc("GET /api/me", srv.handleGetProfile)
	app.HandleFunc("GET /api/me/language", srv.handleGetLanguage)
	app.HandleFunc("PUT /api/me/language", srv.handleSetLanguage)
	app.HandleFunc("GET /api/me/favorites", srv.handleListFavorites)
	app.HandleFunc("PUT /api/me/favorites/{locId}", srv.handleAddFavorite)
	app.HandleFunc("DELETE /api/me/favorites/{locId}", srv.handleRemoveFavorite)
	app.HandleFunc("GET /api/hotspots/{locId}/species", srv.handleHotspotSpecies)

	root := http.NewServeMux()
	root.Handle("/", auth.require(app))
	return root, srv, store, auth
}

// authedRequest builds a request carrying a valid session for userID.
func authedRequest(t *testing.T, auth *Auth, userID, method, target string, body io.Reader) *http.Request {
	t.Helper()
	token, err := auth.signer.issueSession(userID, time.Hour)
	if err != nil {
		t.Fatalf("issueSession: %v", err)
	}
	req := httptest.NewRequest(method, target, body)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	return req
}

func TestPreferencesRoutesRequireAuth(t *testing.T) {
	app, _, _, _ := newPrefsTestApp(t)
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/me"},
		{http.MethodGet, "/api/me/language"},
		{http.MethodPut, "/api/me/language"},
		{http.MethodGet, "/api/me/favorites"},
		{http.MethodPut, "/api/me/favorites/L1"},
		{http.MethodDelete, "/api/me/favorites/L1"},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, httptest.NewRequest(c.method, c.path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, rr.Code)
		}
	}
}

func TestProfileDefaultsForNewAccount(t *testing.T) {
	app, _, store, auth := newPrefsTestApp(t)
	if _, err := store.Upsert("google", "new", "new@example.com", "New"); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, "google:new", http.MethodGet, "/api/me", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/me = %d (%s), want 200", rr.Code, rr.Body.String())
	}

	var p Profile
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if p.ID != "google:new" || p.Email != "new@example.com" {
		t.Errorf("profile = %+v", p)
	}
	if p.Language != defaultLang {
		t.Errorf("language = %q, want default %q", p.Language, defaultLang)
	}
	if p.Favorites == nil || len(p.Favorites) != 0 {
		t.Errorf("favorites = %v, want empty", p.Favorites)
	}
}

func TestSetLanguageValidatesAndPersists(t *testing.T) {
	app, _, store, auth := newPrefsTestApp(t)
	if _, err := store.Upsert("google", "lang", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:lang"

	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/language",
		body(`{"language":"de"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid language = %d, want 400", rr.Code)
	}

	rr = httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/language",
		body(`{"language":"fr"}`)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("valid language = %d (%s), want 204", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodGet, "/api/me/language", nil))
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode language: %v", err)
	}
	if got["language"] != "fr" {
		t.Errorf("GET /api/me/language = %v, want fr", got)
	}
}

func TestFavoriteLifecycle(t *testing.T) {
	app, _, store, auth := newPrefsTestApp(t)
	if _, err := store.Upsert("google", "fav", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:fav"

	// Malformed locId is rejected with the same validation as the hotspot routes.
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/favorites/notaloc", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed locId = %d, want 400", rr.Code)
	}

	// Add twice: the second is a no-op, not an error or a duplicate.
	for i := 0; i < 2; i++ {
		rr = httptest.NewRecorder()
		app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/favorites/50.85,4.35", nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("add favorite (attempt %d) = %d, want 204", i+1, rr.Code)
		}
	}

	favs := getFavorites(t, app, auth, id)
	if len(favs) != 1 || favs[0].LocID != "50.85,4.35" {
		t.Fatalf("favorites after add = %v, want [50.85,4.35]", favs)
	}

	rr = httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodDelete, "/api/me/favorites/50.85,4.35", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("remove favorite = %d, want 204", rr.Code)
	}
	if favs = getFavorites(t, app, auth, id); len(favs) != 0 {
		t.Fatalf("favorites after remove = %v, want empty", favs)
	}
}

func TestFavoriteCarriesDisplayNameAndEnforcesCap(t *testing.T) {
	app, _, store, auth := newPrefsTestApp(t)
	if _, err := store.Upsert("google", "favmeta", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:favmeta"

	// Add with a resolvable display name and coordinates: they come back.
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/favorites/50.85,4.35",
		body(`{"locName":"Parc de Bruxelles","lat":50.85,"lng":4.35}`)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("add with name = %d (%s), want 204", rr.Code, rr.Body.String())
	}
	favs := getFavorites(t, app, auth, id)
	if len(favs) != 1 || favs[0].LocName != "Parc de Bruxelles" || favs[0].Lat != 50.85 || favs[0].Lng != 4.35 {
		t.Fatalf("favorites = %+v, want the supplied name/coords", favs)
	}

	// A hotspot whose name cannot be resolved (empty name) is still listed,
	// with an empty LocName rather than vanishing.
	rr = httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/favorites/40.0,-3.0", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("add without name = %d, want 204", rr.Code)
	}
	favs = getFavorites(t, app, auth, id)
	var unresolved *Favorite
	for i := range favs {
		if favs[i].LocID == "40.0,-3.0" {
			unresolved = &favs[i]
		}
	}
	if unresolved == nil || unresolved.LocName != "" {
		t.Fatalf("unresolved favorite = %+v, want present with empty name", favs)
	}

	// Fill the account to the cap, then one more is rejected with 409.
	for i := len(favs); i < maxFavorites; i++ {
		rr = httptest.NewRecorder()
		app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, fmt.Sprintf("/api/me/favorites/10.%d,20.%d", i, i), nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("fill favorite %d = %d, want 204", i, rr.Code)
		}
	}
	rr = httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/favorites/99.0,99.0", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("add past cap = %d, want 409", rr.Code)
	}

	// Re-adding one that is already favorited still succeeds at the cap.
	rr = httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/favorites/50.85,4.35", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("re-add at cap = %d, want 204", rr.Code)
	}
}

func TestSpeciesFallsBackToStoredLanguage(t *testing.T) {
	app, srv, store, auth := newPrefsTestApp(t)
	if _, err := store.Upsert("google", "sp", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:sp"

	// Seed the species cache for the default (es), preferred (fr), and an
	// explicit (en) language so the handler never reaches for eBird.
	seedSpeciesCache(t, srv, "41.5,-70.5", "es", "Nombre en español")
	seedSpeciesCache(t, srv, "41.5,-70.5", "fr", "Nom en français")
	seedSpeciesCache(t, srv, "41.5,-70.5", "en", "English name")

	// No preference yet: falls back to the hardcoded default.
	if got := firstComName(t, app, auth, id, "/api/hotspots/41.5,-70.5/species"); got != "English name" {
		t.Errorf("no preference: comName = %q, want the English default", got)
	}

	// Stored preference is used when no lang param is supplied.
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/language", body(`{"language":"fr"}`)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("set language = %d, want 204", rr.Code)
	}
	if got := firstComName(t, app, auth, id, "/api/hotspots/41.5,-70.5/species"); got != "Nom en français" {
		t.Errorf("preference fr: comName = %q, want the French name", got)
	}

	// An explicit lang param still wins over the stored preference.
	if got := firstComName(t, app, auth, id, "/api/hotspots/41.5,-70.5/species?lang=en"); got != "English name" {
		t.Errorf("explicit lang=en: comName = %q, want the English name", got)
	}
}

func body(s string) io.Reader { return strings.NewReader(s) }

func getFavorites(t *testing.T, app http.Handler, auth *Auth, id string) []Favorite {
	t.Helper()
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodGet, "/api/me/favorites", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/me/favorites = %d, want 200", rr.Code)
	}
	var favs []Favorite
	if err := json.Unmarshal(rr.Body.Bytes(), &favs); err != nil {
		t.Fatalf("decode favorites: %v", err)
	}
	return favs
}

func seedSpeciesCache(t *testing.T, srv *Server, locID, lang, comName string) {
	t.Helper()
	fake := srv.species.(*fakeSpeciesSource)
	// Empty SciName keeps the handler from kicking off an image fetch.
	fake.set(locID, lang, []string{"testsp1"}, map[string]Taxon{
		"testsp1": {ComName: comName},
	})
}

func firstComName(t *testing.T, app http.Handler, auth *Auth, id, target string) string {
	t.Helper()
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodGet, target, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s = %d (%s), want 200", target, rr.Code, rr.Body.String())
	}
	var cards []SpeciesCard
	if err := json.Unmarshal(rr.Body.Bytes(), &cards); err != nil {
		t.Fatalf("decode species: %v", err)
	}
	if len(cards) == 0 {
		t.Fatalf("GET %s returned no species cards", target)
	}
	return cards[0].ComName
}
