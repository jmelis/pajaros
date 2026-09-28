package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The frontend is split into markup, styles, application code and
// translations; all of static/ is embedded and served as a file tree.
//
//go:embed static
var embeddedStatic embed.FS

type Server struct {
	hotspots *HotspotStore
	species  hotspotSpeciesSource
	cache    *ImageCache
	users    *UserStore
}

// validLocID matches a hotspot ID — "lat,lng" (see ebird.go's Hotspot type),
// e.g. "41.486977,-71.0376". It gates every use of the {locId} path value
// before it's looked up in the hotspot store.
var validLocID = regexp.MustCompile(`^-?\d+(\.\d+)?,-?\d+(\.\d+)?$`).MatchString

// validLang is the fixed set of bird-name languages the frontend offers (see
// static/index.html's #lang select) and the embedded taxonomy ships tables
// for (see taxonomy_data.go).
var validLang = map[string]bool{"es": true, "fr": true, "en": true}

// defaultLang is the bird-name language used when neither the request nor the
// account's stored preference selects one. Kept next to validLang so the
// species endpoint and the store agree on what an unset preference means.
const defaultLang = "en"

// Environment variables
// =====================
//
// Hotspot, species and taxonomy data are entirely offline at request time —
// taxonomy is go:embed'd at build time (taxonomy_data.go); hotspot/species
// data is read lazily from a bbolt file at HOTSPOTS_DATA_DIR, built by
// cmd/gensnapshot (see ARCHITECTURE.md and hotspots_data.go/species_store.go).
// So there is no eBird or GBIF API key or rate limit to configure here —
// Wikimedia (card images) is the only upstream call left at request time.
//
// Optional (existing):
//   CACHE_DIR  on-disk upstream/image cache (default "./cache").
//   PORT, HOST  listen address (default "8080" / "0.0.0.0").
//   HOTSPOTS_DATA_DIR  directory holding hotspots.bolt (default "./data").
//   WIKIMEDIA_RPS  Wikimedia rate limit (default 8/s).
//
// Authentication (all optional; the server starts fine without any of them):
//   SESSION_SECRET  HMAC key for signing session cookies. If unset, a random
//                   key is generated at startup, so sessions don't survive a
//                   restart. Set a stable random value in production.
//   USER_DB_PATH  path to the SQLite database holding users and their
//                 preferences (default "./users.db"). Created automatically,
//                 including its parent directory, on first startup.
//
// Each sign-in provider is turned on explicitly with its own flag:
//   GOOGLE_AUTH_ENABLED  truthy to offer "Sign in with Google".
//   APPLE_AUTH_ENABLED   truthy to offer "Sign in with Apple".
// Truthy means "1", "true", "yes", or "on" (case-insensitive); unset, empty,
// or anything else leaves that provider off, even if its credentials below are
// present. Setting a flag without that provider's complete credentials is a
// fatal startup error naming what's missing.
//
// With neither flag set, authentication is disabled entirely: every route
// (frontend and API) is reachable with no session, and every request acts as a
// single fixed development account ("dev:local") so the account-preference
// routes still work. This is meant for local development only — set a flag (or
// both) before deploying anywhere real.
//
// Google credentials (needed when GOOGLE_AUTH_ENABLED is truthy):
//   GOOGLE_CLIENT_ID
//   GOOGLE_CLIENT_SECRET
//   OAUTH_REDIRECT_BASE_URL  externally reachable base URL of this server,
//                            e.g. "https://birdquiz.example.com". Callback URLs
//                            are <base>/auth/google/callback and
//                            <base>/auth/apple/callback.
//
// Apple credentials (needed when APPLE_AUTH_ENABLED is truthy):
//   APPLE_TEAM_ID      Apple Developer Team ID.
//   APPLE_SERVICES_ID  Services ID (the OAuth client id).
//   APPLE_KEY_ID       Key ID of the Sign in with Apple private key.
//   APPLE_PRIVATE_KEY  contents of the .p8 private key (PEM). Literal "\n"
//                      escapes are accepted for env-var friendliness.
//   OAUTH_REDIRECT_BASE_URL  as above.

type SpeciesCard struct {
	SpeciesCode string `json:"speciesCode"`
	SciName     string `json:"sciName"`
	ComName     string `json:"comName"`
	ImageURL    string `json:"imageUrl"`
	Order       string `json:"order"`
	Family      string `json:"family"`
	// NearbyCount is set only in "popularity" mode: this species' observation
	// count at the hotspot (see hotspotSpeciesSource.PopularityCounts). nil
	// means unmatched/no data, not zero.
	NearbyCount *int `json:"nearbyCount,omitempty"`
	// ImageMissing is true once a Wikimedia lookup has already confirmed no
	// freely-licensed photo exists for this species — distinct from "not
	// fetched yet", which is the common case and just leaves this false.
	ImageMissing bool `json:"imageMissing,omitempty"`
}

func main() {
	cacheDir := os.Getenv("CACHE_DIR")
	if cacheDir == "" {
		cacheDir = "./cache"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = "0.0.0.0"
	}

	cache, err := NewImageCache(cacheDir)
	if err != nil {
		log.Fatalf("cache dir %q: %v", cacheDir, err)
	}

	// GBIF-derived hotspot/species data — low single-digit GB, rebuilt
	// yearly by cmd/gensnapshot, scp'd to this host rather than go:embed'd
	// or committed to git. See ARCHITECTURE.md. bbolt is
	// mmap-backed, so opening it doesn't load the worldwide dataset into
	// memory — one handle is shared by both HotspotStore and SpeciesStore,
	// which read different buckets of the same file.
	dataDir := os.Getenv("HOTSPOTS_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	loadStart := time.Now()
	hotspotsDB, err := bolt.Open(filepath.Join(dataDir, "hotspots.bolt"), 0o444, &bolt.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("open hotspots.bolt: %v", err)
	}
	defer hotspotsDB.Close()
	hotspots, err := openHotspotStore(hotspotsDB)
	if err != nil {
		log.Fatalf("hotspot store: %v", err)
	}
	log.Printf("opened %d hotspots in %s", hotspots.Len(), time.Since(loadStart).Round(time.Millisecond))
	taxonomy, err := loadTaxonomyStore()
	if err != nil {
		log.Fatalf("taxonomy snapshot: %v", err)
	}
	speciesStore := openSpeciesStore(hotspotsDB, taxonomy)
	species := NewSpeciesResolver(taxonomy, speciesStore)

	// Persistence for user identity and preferences. The SQLite database is
	// created, schema included, on first startup; see USER_DB_PATH above.
	dbPath := os.Getenv("USER_DB_PATH")
	if dbPath == "" {
		dbPath = "./users.db"
	}
	users, err := NewUserStore(dbPath)
	if err != nil {
		log.Fatalf("user database %q: %v", dbPath, err)
	}
	defer users.Close()

	srv := &Server{
		hotspots: hotspots,
		species:  species,
		cache:    cache,
		users:    users,
	}

	staticFS, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		log.Fatal(err)
	}

	// Authentication. In open mode (no provider enabled) every route below is
	// reachable without a session; otherwise the built mux gates them. See the
	// environment-variable block above.
	signer, err := newCookieSigner(os.Getenv("SESSION_SECRET"))
	if err != nil {
		log.Fatal(err)
	}
	auth, err := newAuth(loadOAuthEnv(), users, signer)
	if err != nil {
		log.Fatal(err)
	}
	if auth.openMode() {
		log.Printf("WARNING: authentication is disabled — running in open/development mode; every route is reachable without signing in")
	} else {
		logConfiguredProviders(auth.providers)
	}
	if os.Getenv("SESSION_SECRET") == "" && !auth.openMode() {
		log.Printf("SESSION_SECRET not set — sessions will not survive a restart")
	}

	// Per-account limits, independent of the global upstream limiters in
	// ratelimit.go: those cap our total outbound rate across every client,
	// these cap how much of that shared budget one account can burn through.
	// Detail views (species) are the ones that can fan out to Wikimedia (one
	// image lookup per species) on a cache miss, so they get the tighter
	// limit; the hotspot search is a single local bbolt lookup regardless of
	// how many hotspots come back, so it gets a looser one.
	//
	// The key is the account id when the login gate is active. In open mode
	// there is no account, so it falls back to the client IP instead of the
	// single fixed development account, which would rate-limit everyone
	// collectively.
	hotspotDetailLimiter := newKeyedRateLimiter(20, 10) // burst 20, 10/min sustained
	hotspotSearchLimiter := newKeyedRateLimiter(20, 20) // burst 20, 20/min sustained
	hotspotKey := hotspotRateLimitKey(auth.openMode())

	// The login/callback endpoints run before an account exists, so they keep
	// an IP-keyed limit of their own.
	authLimiter := newIPRateLimiter(30, 30)

	appMux := http.NewServeMux()
	appMux.HandleFunc("GET /api/hotspots", hotspotSearchLimiter.middleware(hotspotKey, srv.handleNearbyHotspots))
	appMux.HandleFunc("GET /api/hotspots/{locId}", hotspotDetailLimiter.middleware(hotspotKey, srv.handleHotspotInfo))
	appMux.HandleFunc("GET /api/hotspots/{locId}/species", hotspotDetailLimiter.middleware(hotspotKey, srv.handleHotspotSpecies))
	// The quiz builder reuses the species/taxonomy cache but also reaches for
	// the same offline popularity data a detail view can, so it shares that
	// limiter.
	appMux.HandleFunc("GET /api/hotspots/{locId}/quiz", hotspotDetailLimiter.middleware(hotspotKey, srv.handleHotspotQuiz))

	// Per-account preferences. These touch only the local database, so unlike
	// the hotspot routes they need no upstream-keyed rate limit.
	appMux.HandleFunc("GET /api/me", srv.handleGetProfile)
	appMux.HandleFunc("GET /api/me/language", srv.handleGetLanguage)
	appMux.HandleFunc("PUT /api/me/language", srv.handleSetLanguage)
	appMux.HandleFunc("GET /api/me/secondary-language", srv.handleGetSecondaryLanguage)
	appMux.HandleFunc("PUT /api/me/secondary-language", srv.handleSetSecondaryLanguage)
	appMux.HandleFunc("GET /api/me/star-rewards", srv.handleGetStarRewards)
	appMux.HandleFunc("PUT /api/me/star-rewards", srv.handleSetStarRewards)
	appMux.HandleFunc("GET /api/me/progress", srv.handleGetProgress)
	appMux.HandleFunc("POST /api/me/progress/{speciesCode}", srv.handleSubmitAnswer)
	appMux.HandleFunc("DELETE /api/me/progress", srv.handleResetProgress)
	appMux.HandleFunc("GET /api/me/mastered", srv.handleGetMastered)
	appMux.HandleFunc("GET /api/me/favorites", srv.handleListFavorites)
	appMux.HandleFunc("PUT /api/me/favorites/{locId}", srv.handleAddFavorite)
	appMux.HandleFunc("DELETE /api/me/favorites/{locId}", srv.handleRemoveFavorite)

	appMux.Handle("GET /images/", http.StripPrefix("/images/", http.FileServer(http.Dir(cacheDir))))
	appMux.Handle("GET /", http.FileServerFS(staticFS))

	mux := buildRootMux(auth, appMux, authLimiter)

	addr := host + ":" + port
	log.Printf("listening on %s (cache dir: %s, user db: %s)", addr, cacheDir, dbPath)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// buildRootMux assembles the top-level handler: the public login page and
// OAuth routes (registered only for enabled providers) and the app mux behind
// the login gate. In open mode there is no gate and no login route is
// registered, so a request to /login falls through to the app's 404.
func buildRootMux(auth *Auth, appMux http.Handler, authLimiter *keyedRateLimiter) *http.ServeMux {
	mux := http.NewServeMux()
	// Unauthenticated on purpose: a container orchestrator's liveness/readiness
	// probe has no session to present, and gating it would make every restart
	// depend on eBird/OAuth being reachable rather than on this process itself.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", auth.gate(appMux))
	if auth.openMode() {
		return mux
	}

	mux.HandleFunc("GET /login", auth.handleLoginPage)
	mux.HandleFunc("POST /auth/logout", auth.handleLogout)
	if p, ok := auth.byName["google"]; ok {
		mux.HandleFunc("GET /auth/google", authLimiter.middleware(clientIP, auth.handleOAuthStart(p)))
		mux.HandleFunc("GET /auth/google/callback", authLimiter.middleware(clientIP, auth.handleOAuthCallback(p)))
	}
	if p, ok := auth.byName["apple"]; ok {
		mux.HandleFunc("GET /auth/apple", authLimiter.middleware(clientIP, auth.handleOAuthStart(p)))
		mux.HandleFunc("POST /auth/apple/callback", authLimiter.middleware(clientIP, auth.handleOAuthCallback(p)))
	}
	return mux
}

func (s *Server) handleNearbyHotspots(w http.ResponseWriter, r *http.Request) {
	lat, err1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lng, err2 := strconv.ParseFloat(r.URL.Query().Get("lng"), 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "lat and lng query params are required floats", http.StatusBadRequest)
		return
	}
	distKm := 25.0
	if v := r.URL.Query().Get("dist"); v != "" {
		if d, err := strconv.ParseFloat(v, 64); err == nil {
			distKm = d
		}
	}

	hotspots := s.hotspots.Nearby(lat, lng, distKm)
	writeJSON(w, hotspots)
}

func (s *Server) handleHotspotInfo(w http.ResponseWriter, r *http.Request) {
	locID := r.PathValue("locId")
	if !validLocID(locID) {
		http.Error(w, "invalid locId", http.StatusBadRequest)
		return
	}
	hotspot, ok := s.hotspots.Info(locID)
	if !ok {
		http.Error(w, "hotspot not found", http.StatusNotFound)
		return
	}
	writeJSON(w, hotspot)
}

func (s *Server) handleHotspotSpecies(w http.ResponseWriter, r *http.Request) {
	locID := r.PathValue("locId")
	if !validLocID(locID) {
		http.Error(w, "invalid locId", http.StatusBadRequest)
		return
	}
	lang, ok := s.resolveLang(w, r)
	if !ok {
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "category"
	}

	codes, taxa, err := s.species.Species(locID, lang)
	if err != nil {
		log.Printf("Species(%s, %s): %v", locID, lang, err)
		http.Error(w, "failed to look up hotspot species", http.StatusNotFound)
		return
	}
	s.recordTaxa(taxa, lang)

	// codes is already in eBird's taxonomic order (verified: ascending
	// taxonOrder), which conveniently groups species by family/order —
	// exactly the grouping we want for "category" mode. No extra API calls.
	cards := make([]SpeciesCard, len(codes))
	for i, code := range codes {
		taxon := taxa[code]
		cards[i] = SpeciesCard{
			SpeciesCode: code,
			SciName:     taxon.SciName,
			ComName:     taxon.ComName,
			Order:       taxon.Order,
			Family:      FamilyName(taxon.FamilyCode, taxon.FamilyComName, lang),
		}
		if taxon.SciName != "" {
			cards[i].ImageURL = s.cache.ImageURLFor(taxon.SciName)
			if s.cache.IsKnownMissing(taxon.SciName) {
				cards[i].ImageMissing = true
			} else {
				s.cache.EnsureFetchedAsync(taxon.SciName, lang, taxon.ComName)
			}
		}
	}

	switch mode {
	case "popularity":
		nearbyCounts, err := s.species.PopularityCounts(locID)
		if err != nil {
			// Popularity data is a nice-to-have — fall back to category
			// order rather than fail the whole request.
			log.Printf("PopularityCounts(%s): %v — falling back to category order", locID, err)
		} else {
			for i := range cards {
				if count, ok := nearbyCounts[cards[i].SciName]; ok {
					c := count
					cards[i].NearbyCount = &c
				}
			}
			sortByPopularity(cards,
				func(c SpeciesCard) (int, bool) {
					if c.NearbyCount == nil {
						return 0, false
					}
					return *c.NearbyCount, true
				},
				func(c SpeciesCard) string { return c.ComName },
			)
		}
	case "alphabetical":
		sort.SliceStable(cards, func(i, j int) bool { return cards[i].ComName < cards[j].ComName })
	}

	writeJSON(w, cards)
}

// resolveLang returns the effective, already-validated bird-name language for
// a request: an explicit lang query value, else the account's stored
// preference, else the app default. An explicit (even invalid) query value is
// validated strictly. On an unsupported value it writes a 400 and returns
// ok=false, so callers just return.
func (s *Server) resolveLang(w http.ResponseWriter, r *http.Request) (string, bool) {
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		if pref, ok, err := s.users.PreferredLanguage(userIDFromContext(r)); err != nil {
			log.Printf("PreferredLanguage(%s): %v", userIDFromContext(r), err)
		} else if ok && validLang[pref] {
			lang = pref
		}
	}
	if lang == "" {
		lang = defaultLang
	}
	if !validLang[lang] {
		http.Error(w, "unsupported lang", http.StatusBadRequest)
		return "", false
	}
	return lang, true
}

// recordTaxa caches resolved taxonomy for later local use (the mastered
// view). Failure is non-fatal: the request that triggered it already has the
// data it needs.
func (s *Server) recordTaxa(taxa map[string]Taxon, lang string) {
	if s.users == nil {
		return
	}
	if err := s.users.RecordTaxa(taxa, lang); err != nil {
		log.Printf("record taxonomy (%s): %v", lang, err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}
