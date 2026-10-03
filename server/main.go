package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/jmelis/pajaros/server/internal/areas"
	"github.com/jmelis/pajaros/server/internal/seasonal"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// The frontend is split into markup, styles, application code and
// translations; all of static/ is embedded and served as a file tree.
//
//go:embed static
var embeddedStatic embed.FS

type Server struct {
	areas   *AreaResolver
	places  *PlaceStore
	species areaSpeciesSource
	cache   *ImageCache
	users   *UserStore
	contact *contactMailer // nil when the contact form isn't configured
	ranges  *RangeMapper   // nil when species range maps aren't wired (tests)
}

// defaultLang is the bird-name language used when neither the request nor the
// account's stored preference selects one. Kept next to validLang so the
// species endpoint and the store agree on what an unset preference means.
const defaultLang = "en"

// Environment variables
// =====================
//
// Place, species and taxonomy data are entirely offline at request time —
// taxonomy is go:embed'd at build time (taxonomy_data.go); the species counts
// are read lazily from seasonal_cells.bolt at DATA_DIR, built by
// cmd/gensnapshot (see ARCHITECTURE.md and area.go/species_store.go).
// Place-name search reads a second, much smaller bbolt file in the same
// directory, places.bolt, built from OpenStreetMap and Natural Earth
// (places_data.go). So
// there is no eBird or GBIF API key or rate limit to configure here —
// Wikimedia (card images) is the only upstream call left at request time.
//
// Optional (existing):
//   CACHE_DIR  on-disk upstream/image cache (default "./cache").
//   RESEND_API_KEY, CONTACT_FROM, CONTACT_TO  enable the About page's contact
//                  form for signed-in users: messages are sent through Resend
//                  from CONTACT_FROM (an address on a Resend-verified domain)
//                  to CONTACT_TO, which is never shown to visitors. The form
//                  is hidden unless all three are set.
//   UMAMI_SCRIPT_URL, UMAMI_WEBSITE_ID  self-hosted Umami tracker script and
//                  site id; analytics load only when both are set.
//   PORT, HOST  listen address (default "8080" / "0.0.0.0").
//   DATA_DIR  directory holding seasonal_cells.bolt and places.bolt
//             (default "./data").
//   WIKIMEDIA_RPS  Wikimedia rate limit (default 8/s). Per process: with N
//                  replicas the combined rate is N times this.
//   SHUTDOWN_DELAY    on SIGTERM, how long to keep serving before closing the
//                     listener, so the ingress stops routing here (default 5s).
//   SHUTDOWN_TIMEOUT  then how long in-flight requests get to finish (default
//                     20s). Delay + timeout must fit in the pod's
//                     terminationGracePeriodSeconds (default 30s).
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
//                            e.g. "https://birdsnearby.example.com". Callback URLs
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
	// ImageURLs is up to maxImagesPerSpecies candidate image URLs for this
	// species (ImageURL is always ImageURLs[0]) — the Learn card's photo
	// gallery; Browse only ever renders ImageURL.
	ImageURLs []string `json:"imageUrls,omitempty"`
	Order     string   `json:"order"`
	Family    string   `json:"family"`
	// NearbyCount is set only in "popularity" mode: this species' observation
	// count in the area (see areaSpeciesSource.PopularityCounts). nil
	// means unmatched/no data, not zero.
	NearbyCount *int `json:"nearbyCount,omitempty"`
	// Season and SeasonBars are set only in "seasonality" mode: one of
	// yearround/seasonal/occasional, and 12 bar heights (0..100, January
	// first) for the species' reporting rate by month. Absent when the
	// area has too little data.
	Season     string `json:"season,omitempty"`
	SeasonBars []int  `json:"seasonBars,omitempty"` // []uint8 would marshal as base64
	// ImageMissing is true once a Wikimedia lookup has already confirmed no
	// freely-licensed photo exists for this species — distinct from "not
	// fetched yet", which is the common case and just leaves this false.
	ImageMissing bool `json:"imageMissing,omitempty"`
}

func main() {
	// `go run . warmcache` pre-populates the image cache and exits, instead
	// of serving — see warmcache.go for what it actually does and why it
	// lives here rather than in cmd/gensnapshot.
	if len(os.Args) > 1 && os.Args[1] == "warmcache" {
		if err := runWarmCache(); err != nil {
			log.Fatalf("warmcache: %v", err)
		}
		return
	}
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
	// Re-trim the cache once under the current image sourcing policy (keep
	// each species' title photo, drop the rest, reopen "no image" species) so
	// the normal background fetching refills it — see imagepolicy.go. Local
	// disk I/O only; non-fatal because live traffic keeps working with whatever
	// is on disk.
	if err := migrateImagePolicy(cache, cacheDir); err != nil {
		log.Printf("image policy migration: %v (continuing — old photos stay until the next restart)", err)
	}

	// GBIF-derived seasonal species counts on a grid — about a gigabyte,
	// rebuilt yearly by cmd/gensnapshot, scp'd to this host rather than
	// go:embed'd or committed to git. See ARCHITECTURE.md. bbolt is
	// mmap-backed, so opening it doesn't load the dataset into memory.
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	cellsStore, err := areas.Open(filepath.Join(dataDir, areas.FileName))
	if err != nil {
		log.Fatalf("open %s: %v", areas.FileName, err)
	}
	defer cellsStore.Close()

	placesDB, err := bolt.Open(filepath.Join(dataDir, "places.bolt"), 0o444, &bolt.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("open places.bolt: %v", err)
	}
	defer placesDB.Close()
	places, err := openPlaceStore(placesDB)
	if err != nil {
		log.Fatalf("place store: %v", err)
	}
	log.Printf("opened %d place names", places.Len())
	areaResolver := NewAreaResolver(cellsStore, places)

	taxonomy, err := loadTaxonomyStore()
	if err != nil {
		log.Fatalf("taxonomy snapshot: %v", err)
	}
	geoip, err := loadGeoIPStore()
	if err != nil {
		log.Fatalf("geoip snapshot: %v", err)
	}
	species := NewSpeciesResolver(taxonomy, newSpeciesStore(areaResolver, taxonomy))

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
		areas:   areaResolver,
		places:  places,
		species: species,
		cache:   cache,
		users:   users,
		contact: newContactMailerFromEnv(),
		ranges:  newRangeMapper(func(fn func(i, j int32, es []seasonal.Entry)) error { return cellsStore.ForEachCell(2, fn) }, taxonomy),
	}

	prometheus.MustRegister(&dbGaugeCollector{users: users, places: places})

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
	auth, err := newAuth(loadOAuthEnv(), users, signer, geoip)
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
	// limit; the place search is a single local bbolt lookup, so it gets a
	// looser one.
	//
	// The key is the account id for a signed-in visitor. Guests (and everyone
	// in open mode) are keyed by client IP instead; the fixed development
	// account would rate-limit everyone collectively.
	areaDetailLimiter := newKeyedRateLimiter("area_detail", 20, 10)   // burst 20, 10/min sustained
	placeSearchLimiter := newKeyedRateLimiter("place_search", 20, 20) // burst 20, 20/min sustained
	limitKey := areaRateLimitKey(auth.openMode())

	// The login/callback endpoints run before an account exists, so they keep
	// an IP-keyed limit of their own.
	authLimiter := newIPRateLimiter("auth", 30, 30)

	appMux := http.NewServeMux()
	// Place-name search: a single local bbolt lookup, no upstream fan-out.
	appMux.HandleFunc("GET /api/places", instrumentHTTP("/api/places", placeSearchLimiter.middleware(limitKey, srv.handlePlaceSearch)))
	appMux.HandleFunc("GET /api/species", instrumentHTTP("/api/species", placeSearchLimiter.middleware(limitKey, srv.handleSpeciesSearch)))
	// A range map scans every 1-degree cell once, then is cached.
	appMux.HandleFunc("GET /api/species/{code}/range", instrumentHTTP("/api/species/{code}/range", areaDetailLimiter.middleware(limitKey, srv.handleSpeciesRange)))
	appMux.HandleFunc("GET /api/places/{key}", instrumentHTTP("/api/places/{key}", areaDetailLimiter.middleware(limitKey, srv.handleAreaInfo)))
	appMux.HandleFunc("GET /api/places/{key}/outline", instrumentHTTP("/api/places/{key}/outline", areaDetailLimiter.middleware(limitKey, srv.handleAreaOutline)))
	appMux.HandleFunc("GET /api/places/{key}/species", instrumentHTTP("/api/places/{key}/species", areaDetailLimiter.middleware(limitKey, srv.handleAreaSpecies)))
	appMux.HandleFunc("GET /api/places/{key}/credits", instrumentHTTP("/api/places/{key}/credits", areaDetailLimiter.middleware(limitKey, srv.handleAreaCredits)))
	// Compare reads only local stores (no image fetching), but pools two
	// areas per call, so it shares the detail limiter.
	appMux.HandleFunc("GET /api/compare", instrumentHTTP("/api/compare", areaDetailLimiter.middleware(limitKey, srv.handleCompare)))

	// Contact: the form is signed-in only, and capped per account so one
	// account can't flood the owner's inbox or burn the Resend quota.
	contactLimiter := newKeyedRateLimiter("contact", 3, 3.0/60) // burst 3, 3/hour sustained
	appMux.HandleFunc("GET /api/contact", instrumentHTTP("/api/contact", srv.handleContactStatus))
	appMux.HandleFunc("POST /api/contact", instrumentHTTP("/api/contact", auth.requireFunc(contactLimiter.middleware(accountKey, srv.handleSendContact))))
	appMux.HandleFunc("GET /api/analytics", instrumentHTTP("/api/analytics", srv.handleAnalytics))

	// Per-account preferences and saved areas: the only routes that need a
	// sign-in (guests get a 401). They touch only the local database, so unlike
	// the area routes they need no upstream-keyed rate limit.
	appMux.HandleFunc("GET /api/me", instrumentHTTP("/api/me", auth.requireFunc(srv.handleGetProfile)))
	appMux.HandleFunc("GET /api/me/language", instrumentHTTP("/api/me/language", auth.requireFunc(srv.handleGetLanguage)))
	appMux.HandleFunc("PUT /api/me/language", instrumentHTTP("/api/me/language", auth.requireFunc(srv.handleSetLanguage)))
	appMux.HandleFunc("GET /api/me/secondary-language", instrumentHTTP("/api/me/secondary-language", auth.requireFunc(srv.handleGetSecondaryLanguage)))
	appMux.HandleFunc("PUT /api/me/secondary-language", instrumentHTTP("/api/me/secondary-language", auth.requireFunc(srv.handleSetSecondaryLanguage)))
	appMux.HandleFunc("GET /api/me/favorites", instrumentHTTP("/api/me/favorites", auth.requireFunc(srv.handleListFavorites)))
	appMux.HandleFunc("PUT /api/me/favorites/{key}", instrumentHTTP("/api/me/favorites/{key}", auth.requireFunc(srv.handleAddFavorite)))
	appMux.HandleFunc("PUT /api/me/favorites/{key}/name", instrumentHTTP("/api/me/favorites/{key}/name", auth.requireFunc(srv.handleRenameFavorite)))
	appMux.HandleFunc("DELETE /api/me/favorites/{key}", instrumentHTTP("/api/me/favorites/{key}", auth.requireFunc(srv.handleRemoveFavorite)))

	appMux.Handle("GET /images/", instrumentHTTP("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir(cacheDir))).ServeHTTP))
	appMux.Handle("GET /", instrumentHTTP("/", http.FileServerFS(staticFS).ServeHTTP))

	mux := buildRootMux(auth, appMux, authLimiter)

	addr := host + ":" + port
	log.Printf("listening on %s (cache dir: %s, user db: %s)", addr, cacheDir, dbPath)

	httpSrv := &http.Server{Addr: addr, Handler: mux}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.ListenAndServe() }()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, os.Interrupt)
	select {
	case err := <-serveErr:
		log.Fatal(err)
	case sig := <-sigs:
		signal.Stop(sigs) // a second signal now kills the process outright
		// On SIGTERM (a rolling update or scale-down) keep serving while the
		// ingress notices this pod is leaving the Service's endpoints;
		// otherwise it keeps routing new requests to a closed listener.
		if sig == syscall.SIGTERM {
			delay := durationFromEnv("SHUTDOWN_DELAY", 5*time.Second)
			log.Printf("SIGTERM: still serving for %s while the pod is removed from the Service", delay)
			time.Sleep(delay)
		}
		timeout := durationFromEnv("SHUTDOWN_TIMEOUT", 20*time.Second)
		log.Printf("shutting down: draining in-flight requests (up to %s)", timeout)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
}

// durationFromEnv parses envVar as a Go duration ("5s"), falling back to def
// when unset or invalid.
func durationFromEnv(envVar string, def time.Duration) time.Duration {
	if v := os.Getenv(envVar); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return def
}

// buildRootMux assembles the top-level handler: the public login page and
// OAuth routes (registered only for enabled providers) and the app mux behind
// the session gate, which identifies signed-in visitors but admits everyone.
// In open mode no login route is registered, so a request to /login falls
// through to the app's 404.
func buildRootMux(auth *Auth, appMux http.Handler, authLimiter *keyedRateLimiter) *http.ServeMux {
	mux := http.NewServeMux()
	// Unauthenticated on purpose: a container orchestrator's liveness/readiness
	// probe has no session to present, and gating it would make every restart
	// depend on eBird/OAuth being reachable rather than on this process itself.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Unauthenticated for the same reason as /healthz above: a scrape has no
	// session to present, and this is what it scrapes.
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.Handle("/", auth.gate(appMux))
	if auth.openMode() {
		return mux
	}

	mux.HandleFunc("GET /login", instrumentHTTP("/login", auth.handleLoginPage))
	mux.HandleFunc("POST /auth/logout", instrumentHTTP("/auth/logout", auth.handleLogout))
	if p, ok := auth.byName["google"]; ok {
		mux.HandleFunc("GET /auth/google", instrumentHTTP("/auth/google", authLimiter.middleware(clientIP, auth.handleOAuthStart(p))))
		mux.HandleFunc("GET /auth/google/callback", instrumentHTTP("/auth/google/callback", authLimiter.middleware(clientIP, auth.handleOAuthCallback(p))))
	}
	if p, ok := auth.byName["apple"]; ok {
		mux.HandleFunc("GET /auth/apple", instrumentHTTP("/auth/apple", authLimiter.middleware(clientIP, auth.handleOAuthStart(p))))
		mux.HandleFunc("POST /auth/apple/callback", instrumentHTTP("/auth/apple/callback", authLimiter.middleware(clientIP, auth.handleOAuthCallback(p))))
	}
	return mux
}

// handlePlaceSearch answers "type a place name" search — a prefix match
// against places.bolt, most-populous match first. Each result's id names the
// area (key "p<id>"); a custom circle needs no search.
func (s *Server) handlePlaceSearch(w http.ResponseWriter, r *http.Request) {
	places := s.places.Search(r.URL.Query().Get("q"), 8)
	writeJSON(w, places)
}

func (s *Server) handleAreaInfo(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, info)
}

func (s *Server) handleAreaSpecies(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !validAreaKey(key) {
		http.Error(w, "invalid area key", http.StatusBadRequest)
		return
	}
	lang, ok := s.resolveLang(w, r)
	if !ok {
		return
	}
	month, ok := parseMonth(w, r)
	if !ok {
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "category"
	}

	if mode == "seasonality" {
		month = 0 // the seasonal view always looks at the whole year
	}
	codes, taxa, err := s.species.Species(key, lang, month)
	if err != nil {
		log.Printf("Species(%s, %s, month %d): %v", key, lang, month, err)
		http.Error(w, "failed to look up species", http.StatusNotFound)
		return
	}

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
			Family:      FamilyName(taxon.FamilyCode, lang),
		}
		if taxon.SciName != "" {
			cards[i].ImageURLs = s.cache.ImageURLsFor(taxon.SciName)
			cards[i].ImageURL = cards[i].ImageURLs[0]
			if s.cache.IsKnownMissing(taxon.SciName) {
				cards[i].ImageMissing = true
			} else {
				s.cache.EnsureFetchedAsync(taxon.SciName)
			}
		}
	}

	switch mode {
	case "popularity":
		nearbyCounts, err := s.species.PopularityCounts(key, month)
		if err != nil {
			// Popularity data is a nice-to-have — fall back to category
			// order rather than fail the whole request.
			log.Printf("PopularityCounts(%s, month %d): %v — falling back to category order", key, month, err)
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

	// Every card carries its monthly bar chart, whatever the order; the
	// seasonality order also groups and sorts by season.
	if seasons, err := s.species.Seasonality(key); err != nil {
		log.Printf("Seasonality(%s): %v — no season data", key, err)
	} else {
		labelSeasons(cards, seasons)
		if mode == "seasonality" {
			sortBySeason(cards, seasons)
		}
	}

	areaSpeciesCount.WithLabelValues(mode).Observe(float64(len(cards)))
	writeJSON(w, cards)
}

// parseMonth reads the optional ?month= query value: 1..12 selects one
// calendar month (all years pooled), absent or 0 means the whole year. Any
// other value writes a 400 and returns ok=false, so callers just return.
func parseMonth(w http.ResponseWriter, r *http.Request) (month int, ok bool) {
	v := r.URL.Query().Get("month")
	if v == "" {
		return 0, true
	}
	m, err := strconv.Atoi(v)
	if err != nil || m < 0 || m > 12 {
		http.Error(w, "month must be 0 (whole year) or 1-12", http.StatusBadRequest)
		return 0, false
	}
	return m, true
}

// resolveLang returns the effective, already-validated bird-name language for
// a request: an explicit lang query value, else the signed-in account's stored
// preference (guests have none), else the app default. An explicit (even invalid) query value is
// validated strictly. On an unsupported value it writes a 400 and returns
// ok=false, so callers just return.
func (s *Server) resolveLang(w http.ResponseWriter, r *http.Request) (string, bool) {
	lang := r.URL.Query().Get("lang")
	if id := userIDFromContext(r); lang == "" && id != "" {
		if pref, ok, err := s.users.PreferredLanguage(id); err != nil {
			log.Printf("PreferredLanguage(%s): %v", id, err)
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}

// labelSeasons gives each card its season kind and monthly bars.
func labelSeasons(cards []SpeciesCard, seasons map[string]SpeciesSeason) {
	for i := range cards {
		if sp, ok := seasons[cards[i].SciName]; ok {
			cards[i].Season = sp.Kind
			bars := make([]int, len(sp.Bars))
			for m, v := range sp.Bars {
				bars[m] = int(v)
			}
			cards[i].SeasonBars = bars
		}
	}
}

// sortBySeason orders the cards by
// group (year-round, seasonal, occasional): year-round and occasional ones
// by how often they are reported, seasonal ones by the month they peak in so
// the group reads like a calendar. Cards with no season data stay last, in
// their incoming order.
func sortBySeason(cards []SpeciesCard, seasons map[string]SpeciesSeason) {
	rank := map[string]int{seasonYearRound: 0, seasonSeasonal: 1, seasonOccasional: 2}
	key := func(c SpeciesCard) (group, peak, total int) {
		sp, ok := seasons[c.SciName]
		if !ok {
			return len(rank), 0, 0
		}
		if sp.Kind == seasonSeasonal {
			return rank[sp.Kind], sp.Peak, -sp.Total
		}
		return rank[sp.Kind], 0, -sp.Total
	}
	sort.SliceStable(cards, func(i, j int) bool {
		gi, pi, ti := key(cards[i])
		gj, pj, tj := key(cards[j])
		if gi != gj {
			return gi < gj
		}
		if pi != pj {
			return pi < pj
		}
		return ti < tj
	})
}
