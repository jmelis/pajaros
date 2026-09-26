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

	// Per-IP limits, independent of the global upstream limiters in
	// ratelimit.go: those cap our total outbound rate across every client,
	// these cap how much of that shared budget one IP can burn through.
	// Detail views (info + species) are the ones that can fan out to eBird
	// (and, via popularity mode, GBIF) on a cache miss, so they get the
	// tighter limit; the hotspot search is a single eBird call per request
	// regardless of how many hotspots come back, so it gets a looser one.
	hotspotDetailLimiter := newIPRateLimiter(20, 10) // burst 20, 10/min sustained
	hotspotSearchLimiter := newIPRateLimiter(20, 20) // burst 20, 20/min sustained

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hotspots", hotspotSearchLimiter.middleware(srv.handleNearbyHotspots))
	mux.HandleFunc("GET /api/hotspots/{locId}", hotspotDetailLimiter.middleware(srv.handleHotspotInfo))
	mux.HandleFunc("GET /api/hotspots/{locId}/species", hotspotDetailLimiter.middleware(srv.handleHotspotSpecies))
	mux.Handle("GET /images/", http.StripPrefix("/images/", http.FileServer(http.Dir(cacheDir))))
	mux.Handle("GET /", http.FileServerFS(staticFS))

	addr := host + ":" + port
	log.Printf("listening on %s (cache dir: %s)", addr, cacheDir)
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
			s.cache.EnsureFetchedAsync(taxon.SciName)
		}
	}

	if mode == "popularity" {
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
	}

	writeJSON(w, cards)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}
