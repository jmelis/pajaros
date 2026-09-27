package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
)

//go:embed static/index.html
var embeddedStatic embed.FS

type Server struct {
	ebird      *EbirdClient
	ebirdCache *EbirdCache
	cache      *ImageCache
	gbif       *GBIFCache
}

// validLocID matches eBird's hotspot location ID format (e.g. "L99381"). It
// gates every use of the {locId} path value, both to reject junk before
// spending an upstream call on it and because that value is also used
// verbatim in cache file paths (see ebird_cache.go) — without this, a
// crafted locId could walk out of the cache directory.
var validLocID = regexp.MustCompile(`^L\d+$`).MatchString

// validLang is the fixed set of bird-name languages the frontend offers
// (see static/index.html's #lang select). Enforcing an allow-list, rather
// than accepting any query value, closes a cache-bypass path: lang is part
// of the species cache key, so an attacker sending a new lang value on every
// request would otherwise force a fresh eBird fetch every time.
var validLang = map[string]bool{"es": true, "fr": true, "en": true}

// Environment variables
// =====================
//
// Required:
//   EBIRD_API_KEY  eBird API key (existing requirement).
//
// Optional (existing):
//   CACHE_DIR  on-disk upstream/image cache (default "./cache").
//   PORT, HOST  listen address (default "8080" / "0.0.0.0").
//   WIKIMEDIA_RPS, EBIRD_RPS, GBIF_RPS  global upstream rate limits.
//
// Authentication (all optional; the server starts fine without any of them,
// but nothing can be reached until at least one provider is configured):
//   SESSION_SECRET  HMAC key for signing session cookies. If unset, a random
//                   key is generated at startup, so sessions don't survive a
//                   restart. Set a stable random value in production.
//   USER_STORE_DIR  directory for the one-JSON-file-per-user store
//                   (default "./users").
//
// Sign in with Google (needs all three):
//   GOOGLE_CLIENT_ID
//   GOOGLE_CLIENT_SECRET
//   OAUTH_REDIRECT_BASE_URL  externally reachable base URL of this server,
//                            e.g. "https://pajaros.example.com". Callback URLs
//                            are <base>/auth/google/callback and
//                            <base>/auth/apple/callback.
//
// Sign in with Apple (needs all six):
//   APPLE_TEAM_ID      Apple Developer Team ID.
//   APPLE_SERVICES_ID  Services ID (the OAuth client id).
//   APPLE_KEY_ID       Key ID of the Sign in with Apple private key.
//   APPLE_PRIVATE_KEY  contents of the .p8 private key (PEM). Literal "\n"
//                      escapes are accepted for env-var friendliness.
//   OAUTH_REDIRECT_BASE_URL  as above.
//
// A provider with any required value missing is simply not offered (its login
// button is rendered disabled); it never prevents startup or panics.

type SpeciesCard struct {
	SpeciesCode string `json:"speciesCode"`
	SciName     string `json:"sciName"`
	ComName     string `json:"comName"`
	ImageURL    string `json:"imageUrl"`
	Order       string `json:"order"`
	Family      string `json:"family"`
	// NearbyCount is set only in "popularity" mode: GBIF occurrence count
	// for this species in the surrounding area (see gbif.go). nil means
	// unmatched/no data, not zero — see PopularityCounts.
	NearbyCount *int `json:"nearbyCount,omitempty"`
	// ImageMissing is true once a Wikimedia lookup has already confirmed no
	// freely-licensed photo exists for this species — distinct from "not
	// fetched yet", which is the common case and just leaves this false.
	ImageMissing bool `json:"imageMissing,omitempty"`
}

func main() {
	apiKey := os.Getenv("EBIRD_API_KEY")
	if apiKey == "" {
		log.Fatal("EBIRD_API_KEY is required")
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
	gbifCache, err := NewGBIFCache(cacheDir)
	if err != nil {
		log.Fatalf("cache dir %q: %v", cacheDir, err)
	}
	ebirdClient := NewEbirdClient(apiKey)
	ebirdCache, err := NewEbirdCache(ebirdClient, cacheDir)
	if err != nil {
		log.Fatalf("cache dir %q: %v", cacheDir, err)
	}

	srv := &Server{
		ebird:      ebirdClient,
		ebirdCache: ebirdCache,
		cache:      cache,
		gbif:       gbifCache,
	}

	staticFS, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		log.Fatal(err)
	}

	// Authentication. Everything below the top-level login/callback routes is
	// gated by auth.require; see the environment-variable block above.
	userDir := os.Getenv("USER_STORE_DIR")
	if userDir == "" {
		userDir = "./users"
	}
	users, err := NewUserStore(userDir)
	if err != nil {
		log.Fatalf("user store dir %q: %v", userDir, err)
	}
	signer, err := newCookieSigner(os.Getenv("SESSION_SECRET"))
	if err != nil {
		log.Fatal(err)
	}
	auth, err := newAuth(loadOAuthEnv(), users, signer)
	if err != nil {
		log.Fatal(err)
	}
	logConfiguredProviders(auth.providers)
	if os.Getenv("SESSION_SECRET") == "" {
		log.Printf("SESSION_SECRET not set — sessions will not survive a restart")
	}

	// Per-account limits, independent of the global upstream limiters in
	// ratelimit.go: those cap our total outbound rate across every client,
	// these cap how much of that shared budget one account can burn through.
	// Detail views (info + species) are the ones that can fan out to eBird
	// (and, via popularity mode, GBIF) on a cache miss, so they get the
	// tighter limit; the hotspot search is a single eBird call per request
	// regardless of how many hotspots come back, so it gets a looser one.
	hotspotDetailLimiter := newKeyedRateLimiter(20, 10) // burst 20, 10/min sustained
	hotspotSearchLimiter := newKeyedRateLimiter(20, 20) // burst 20, 20/min sustained

	// The login/callback endpoints run before an account exists, so they keep
	// an IP-keyed limit of their own.
	authLimiter := newIPRateLimiter(30, 30)

	appMux := http.NewServeMux()
	appMux.HandleFunc("GET /api/hotspots", hotspotSearchLimiter.middleware(accountKey, srv.handleNearbyHotspots))
	appMux.HandleFunc("GET /api/hotspots/{locId}", hotspotDetailLimiter.middleware(accountKey, srv.handleHotspotInfo))
	appMux.HandleFunc("GET /api/hotspots/{locId}/species", hotspotDetailLimiter.middleware(accountKey, srv.handleHotspotSpecies))
	appMux.Handle("GET /images/", http.StripPrefix("/images/", http.FileServer(http.Dir(cacheDir))))
	appMux.Handle("GET /", http.FileServerFS(staticFS))

	// Public routes: the login page and the OAuth endpoints themselves.
	// Everything else falls through "/" to the auth-gated appMux.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", auth.handleLoginPage)
	mux.HandleFunc("GET /auth/google", authLimiter.middleware(clientIP, auth.handleOAuthStart(auth.byName["google"])))
	mux.HandleFunc("GET /auth/google/callback", authLimiter.middleware(clientIP, auth.handleOAuthCallback(auth.byName["google"])))
	mux.HandleFunc("GET /auth/apple", authLimiter.middleware(clientIP, auth.handleOAuthStart(auth.byName["apple"])))
	mux.HandleFunc("POST /auth/apple/callback", authLimiter.middleware(clientIP, auth.handleOAuthCallback(auth.byName["apple"])))
	mux.HandleFunc("POST /auth/logout", auth.handleLogout)
	mux.Handle("/", auth.require(appMux))

	addr := host + ":" + port
	log.Printf("listening on %s (cache dir: %s, user dir: %s)", addr, cacheDir, userDir)
	log.Fatal(http.ListenAndServe(addr, mux))
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

	hotspots, err := s.ebird.NearbyHotspots(lat, lng, distKm)
	if err != nil {
		log.Printf("NearbyHotspots(%g, %g, %g): %v", lat, lng, distKm, err)
		http.Error(w, "failed to fetch hotspots from eBird", http.StatusBadGateway)
		return
	}
	writeJSON(w, hotspots)
}

func (s *Server) handleHotspotInfo(w http.ResponseWriter, r *http.Request) {
	locID := r.PathValue("locId")
	if !validLocID(locID) {
		http.Error(w, "invalid locId", http.StatusBadRequest)
		return
	}
	hotspot, err := s.ebirdCache.HotspotInfo(locID)
	if err != nil {
		log.Printf("HotspotInfo(%s): %v", locID, err)
		http.Error(w, "failed to fetch hotspot from eBird", http.StatusBadGateway)
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
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = "es"
	}
	if !validLang[lang] {
		http.Error(w, "unsupported lang", http.StatusBadRequest)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "category"
	}

	codes, taxa, err := s.ebirdCache.Species(locID, lang)
	if err != nil {
		log.Printf("Species(%s, %s): %v", locID, lang, err)
		http.Error(w, "failed to fetch species from eBird", http.StatusBadGateway)
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
			Family:      FamilyName(taxon.FamilyCode, taxon.FamilyComName, lang),
		}
		if taxon.SciName != "" {
			cards[i].ImageURL = s.cache.ImageURLFor(taxon.SciName)
			if s.cache.IsKnownMissing(taxon.SciName) {
				cards[i].ImageMissing = true
			} else {
				s.cache.EnsureFetchedAsync(taxon.SciName)
			}
		}
	}

	switch mode {
	case "popularity":
		hotspot, err := s.ebirdCache.HotspotInfo(locID)
		if err != nil {
			log.Printf("HotspotInfo(%s): %v", locID, err)
			http.Error(w, "failed to fetch hotspot location from eBird", http.StatusBadGateway)
			return
		}
		nearbyCounts, err := s.gbif.PopularityCounts(hotspot.Lat, hotspot.Lng)
		if err != nil {
			// Popularity data is a nice-to-have — fall back to category
			// order rather than fail the whole request.
			log.Printf("PopularityCounts(%g, %g): %v — falling back to category order", hotspot.Lat, hotspot.Lng, err)
		} else {
			for i := range cards {
				if count, ok := nearbyCounts[cards[i].SciName]; ok {
					c := count
					cards[i].NearbyCount = &c
				}
			}
			sort.SliceStable(cards, func(i, j int) bool {
				a, b := cards[i].NearbyCount, cards[j].NearbyCount
				av, bv := -1, -1
				if a != nil {
					av = *a
				}
				if b != nil {
					bv = *b
				}
				if av != bv {
					return av > bv
				}
				return cards[i].ComName < cards[j].ComName
			})
		}
	case "alphabetical":
		sort.SliceStable(cards, func(i, j int) bool { return cards[i].ComName < cards[j].ComName })
	}

	writeJSON(w, cards)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}
