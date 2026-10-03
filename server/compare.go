package main

import (
	"log"
	"math"
	"net/http"
	"sort"
)

// compareMinReliable is the combined observation count (both areas
// together) below which a shared species' share ratio is too noisy to read
// anything into; such species still appear in the comparison, just flagged
// not Reliable so the page can fade them.
const compareMinReliable = 30

type compareArea struct {
	AreaInfo
	Species      int `json:"species"`
	Observations int `json:"observations"`
}

// compareSpecies is one species of the union of both areas. Counts are 0
// (and ranks 0) on the side where the species was never recorded. Shares are
// the fraction of that area's total observations, which makes areas
// with very different amounts of data comparable.
type compareSpecies struct {
	Code     string   `json:"code"`
	SciName  string   `json:"sciName"`
	ComName  string   `json:"comName"`
	Family   string   `json:"family"`
	CountA   int      `json:"countA"`
	CountB   int      `json:"countB"`
	RankA    int      `json:"rankA"`
	RankB    int      `json:"rankB"`
	ShareA   float64  `json:"shareA"`
	ShareB   float64  `json:"shareB"`
	LogRatio *float64 `json:"logRatio,omitempty"` // log2(shareA/shareB); only for species at both
	Reliable bool     `json:"reliable"`           // at both and combined count >= compareMinReliable
}

type compareFamily struct {
	Family      string  `json:"family"`
	SpeciesA    int     `json:"speciesA"`
	SpeciesB    int     `json:"speciesB"`
	SpeciesBoth int     `json:"speciesBoth"`
	ShareA      float64 `json:"shareA"`
	ShareB      float64 `json:"shareB"`
}

type compareResult struct {
	A          compareArea      `json:"a"`
	B          compareArea      `json:"b"`
	DistanceKm float64          `json:"distanceKm"`
	Shared     int              `json:"shared"`
	OnlyA      int              `json:"onlyA"`
	OnlyB      int              `json:"onlyB"`
	Species    []compareSpecies `json:"species"`  // by combined count, most observed first
	Families   []compareFamily  `json:"families"` // by combined share, largest first
}

// compareSide is one area's species data, as returned by
// areaSpeciesSource (taxa keyed by species code, counts by scientific name).
type compareSide struct {
	codes  []string
	taxa   map[string]Taxon
	counts map[string]int
}

func (s compareSide) count(code string) int { return s.counts[s.taxa[code].SciName] }

// ranks returns each species code's 1-based popularity rank (count desc,
// ties by common name — the app-wide ordering, see sortByPopularity) and the
// area's total observation count.
func (s compareSide) ranks() (map[string]int, int) {
	type item struct {
		code, name string
		count      int
	}
	items := make([]item, 0, len(s.codes))
	total := 0
	for _, c := range s.codes {
		n := s.count(c)
		total += n
		items = append(items, item{c, s.taxa[c].ComName, n})
	}
	sortByPopularity(items,
		func(i item) (int, bool) { return i.count, true },
		func(i item) string { return i.name },
	)
	ranks := make(map[string]int, len(items))
	for i, it := range items {
		ranks[it.code] = i + 1
	}
	return ranks, total
}

// buildComparison merges two areas' species data into the compare view
// model. Area metadata (names, coordinates) is filled in by the caller.
func buildComparison(a, b compareSide, lang string) compareResult {
	ranksA, totalA := a.ranks()
	ranksB, totalB := b.ranks()

	var res compareResult
	res.A = compareArea{Species: len(a.codes), Observations: totalA}
	res.B = compareArea{Species: len(b.codes), Observations: totalB}

	share := func(n, total int) float64 {
		if total == 0 {
			return 0
		}
		return float64(n) / float64(total)
	}

	union := map[string]Taxon{}
	for _, c := range a.codes {
		union[c] = a.taxa[c]
	}
	for _, c := range b.codes {
		if _, ok := union[c]; !ok {
			union[c] = b.taxa[c]
		}
	}

	type famAgg struct {
		a, b, both int
		cntA, cntB int
	}
	fams := map[string]*famAgg{}

	res.Species = make([]compareSpecies, 0, len(union))
	for code, taxon := range union {
		sp := compareSpecies{
			Code:    code,
			SciName: taxon.SciName,
			ComName: taxon.ComName,
			Family:  FamilyName(taxon.FamilyCode, lang),
			RankA:   ranksA[code],
			RankB:   ranksB[code],
		}
		inA, inB := sp.RankA > 0, sp.RankB > 0
		if inA {
			sp.CountA = a.count(code)
		}
		if inB {
			sp.CountB = b.count(code)
		}
		sp.ShareA = share(sp.CountA, totalA)
		sp.ShareB = share(sp.CountB, totalB)
		switch {
		case inA && inB:
			res.Shared++
			if sp.ShareA > 0 && sp.ShareB > 0 {
				lr := math.Log2(sp.ShareA / sp.ShareB)
				sp.LogRatio = &lr
				sp.Reliable = sp.CountA+sp.CountB >= compareMinReliable
			}
		case inA:
			res.OnlyA++
		default:
			res.OnlyB++
		}

		f := fams[sp.Family]
		if f == nil {
			f = &famAgg{}
			fams[sp.Family] = f
		}
		if inA {
			f.a++
			f.cntA += sp.CountA
		}
		if inB {
			f.b++
			f.cntB += sp.CountB
		}
		if inA && inB {
			f.both++
		}
		res.Species = append(res.Species, sp)
	}

	sort.Slice(res.Species, func(i, j int) bool {
		x, y := res.Species[i], res.Species[j]
		if cx, cy := x.CountA+x.CountB, y.CountA+y.CountB; cx != cy {
			return cx > cy
		}
		return x.ComName < y.ComName
	})

	res.Families = make([]compareFamily, 0, len(fams))
	for name, f := range fams {
		res.Families = append(res.Families, compareFamily{
			Family:      name,
			SpeciesA:    f.a,
			SpeciesB:    f.b,
			SpeciesBoth: f.both,
			ShareA:      share(f.cntA, totalA),
			ShareB:      share(f.cntB, totalB),
		})
	}
	sort.Slice(res.Families, func(i, j int) bool {
		x, y := res.Families[i], res.Families[j]
		if sx, sy := x.ShareA+x.ShareB, y.ShareA+y.ShareB; sx != sy {
			return sx > sy
		}
		return x.Family < y.Family
	})
	return res
}

// handleCompare serves GET /api/compare?a=<key>&b=<key>[&lang=xx]. It reads
// species, counts and taxonomy straight from the local stores and never
// touches the image cache, so comparing two areas can't fan out to Wikimedia
// the way opening a species list can.
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	keyA, keyB := r.URL.Query().Get("a"), r.URL.Query().Get("b")
	if !validAreaKey(keyA) || !validAreaKey(keyB) {
		http.Error(w, "a and b must be valid area keys", http.StatusBadRequest)
		return
	}
	if keyA == keyB {
		http.Error(w, "a and b must be different areas", http.StatusBadRequest)
		return
	}
	lang, ok := s.resolveLang(w, r)
	if !ok {
		return
	}
	infoA, errA := s.areas.Info(keyA)
	infoB, errB := s.areas.Info(keyB)
	if errA != nil || errB != nil {
		http.Error(w, "place not found", http.StatusNotFound)
		return
	}

	load := func(key string) (compareSide, bool) {
		codes, taxa, err := s.species.Species(key, lang, 0)
		if err != nil {
			log.Printf("compare: Species(%s, %s): %v", key, lang, err)
			return compareSide{}, false
		}
		counts, err := s.species.PopularityCounts(key, 0)
		if err != nil {
			log.Printf("compare: PopularityCounts(%s): %v", key, err)
			return compareSide{}, false
		}
		return compareSide{codes: codes, taxa: taxa, counts: counts}, true
	}
	sideA, okA := load(keyA)
	sideB, okB := load(keyB)
	if !okA || !okB {
		http.Error(w, "failed to look up species", http.StatusNotFound)
		return
	}

	res := buildComparison(sideA, sideB, lang)
	res.A.AreaInfo, res.B.AreaInfo = infoA, infoB
	res.DistanceKm = haversineKm(infoA.Lat, infoA.Lng, infoB.Lat, infoB.Lng)
	writeJSON(w, res)
}
