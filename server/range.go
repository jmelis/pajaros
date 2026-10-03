package main

import (
	"container/list"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/jmelis/pajaros/server/internal/placekey"
	"github.com/jmelis/pajaros/server/internal/seasonal"
)

// rangeSmoothing is added to a cell's effort before dividing, so a cell with
// a handful of records can't show a species as dominant on a single sighting.
const rangeSmoothing = 5

// rangeTopN is how many of a cell's most-reported species define its effort.
const rangeTopN = 3

var speciesCodeRE = regexp.MustCompile(`^[a-z0-9]{3,12}$`)

var errUnknownSpecies = errors.New("unknown species")

// RangeCells is a species' world map: one [south-west latitude, south-west
// longitude, percent] triple per 1-degree cell where it was recorded.
type RangeCells struct {
	Cells [][3]int
	Max   int // the largest percent among Cells
}

// RangeMapper answers "where is this species, and how frequent". Frequency
// follows the effort adjustment of the seasonality classifier: a cell's effort
// is the mean record count of its three most-reported species, and a species'
// rate is its count over that effort (smoothed), so densely birded regions
// don't swamp the map the way raw counts would.
type RangeMapper struct {
	eachCell func(fn func(i, j int32, es []seasonal.Entry)) error
	taxa     []Taxon
	idByCode map[string]uint16
	taxonomy *TaxonomyStore
	sem      chan struct{}

	mu    sync.Mutex
	max   int
	order *list.List // front = most recent
	items map[string]*list.Element

	indexOnce sync.Once
	index     map[string][]searchEntry // lang -> entries
}

type rangeItem struct {
	key string
	val *RangeCells
}

type searchEntry struct {
	name  string // normalized
	words string // " " + normalized, for word-start matches
	code  string
}

func newRangeMapper(each func(fn func(i, j int32, es []seasonal.Entry)) error, taxonomy *TaxonomyStore) *RangeMapper {
	taxa := taxonomy.StableIDOrder()
	ids := make(map[string]uint16, len(taxa))
	for i, t := range taxa {
		ids[t.SpeciesCode] = uint16(i)
	}
	return &RangeMapper{
		eachCell: each,
		taxa:     taxa,
		idByCode: ids,
		taxonomy: taxonomy,
		sem:      make(chan struct{}, 2),
		max:      64,
		order:    list.New(),
		items:    map[string]*list.Element{},
	}
}

func rangeCacheKey(code string, month int) string { return code + "/" + string(rune('A'+month)) }

// Range returns the world map of species code for month (1-12; 0 for the
// whole year). It scans every 1-degree cell once per (species, month) and
// caches the result.
func (m *RangeMapper) Range(code string, month int) (*RangeCells, error) {
	id, ok := m.idByCode[code]
	if !ok {
		return nil, errUnknownSpecies
	}
	key := rangeCacheKey(code, month)
	if v := m.cacheGet(key); v != nil {
		return v, nil
	}
	m.sem <- struct{}{}
	defer func() { <-m.sem }()
	if v := m.cacheGet(key); v != nil {
		return v, nil
	}
	defer bboltTimer("range", "scan")()

	out := &RangeCells{Cells: [][3]int{}}
	err := m.eachCell(func(i, j int32, es []seasonal.Entry) {
		k := sort.Search(len(es), func(n int) bool { return es[n].ID >= id })
		if k == len(es) || es[k].ID != id {
			return
		}
		count := entryCount(es[k], month)
		if count == 0 {
			return
		}
		pct := int(math.Round(100 * math.Min(1, float64(count)/(cellEffort(es, month)+rangeSmoothing))))
		if pct < 1 {
			pct = 1
		}
		if pct > out.Max {
			out.Max = pct
		}
		out.Cells = append(out.Cells, [3]int{int(i), int(j), pct})
	})
	if err != nil {
		return nil, err
	}
	m.cachePut(key, out)
	return out, nil
}

func entryCount(e seasonal.Entry, month int) uint32 {
	if month == 0 {
		return uint32(e.Months.Total())
	}
	return e.Months[month-1]
}

// cellEffort is the mean count of the rangeTopN most-reported species.
func cellEffort(es []seasonal.Entry, month int) float64 {
	var top [rangeTopN]uint32
	for _, e := range es {
		c := entryCount(e, month)
		for n := 0; n < rangeTopN; n++ {
			if c > top[n] {
				c, top[n] = top[n], c
			}
		}
	}
	var sum float64
	var n int
	for _, c := range top {
		if c > 0 {
			sum += float64(c)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func (m *RangeMapper) cacheGet(key string) *RangeCells {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.items[key]
	if !ok {
		return nil
	}
	m.order.MoveToFront(el)
	return el.Value.(*rangeItem).val
}

func (m *RangeMapper) cachePut(key string, v *RangeCells) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[key]; ok {
		el.Value.(*rangeItem).val = v
		m.order.MoveToFront(el)
		return
	}
	m.items[key] = m.order.PushFront(&rangeItem{key: key, val: v})
	for m.order.Len() > m.max {
		last := m.order.Back()
		m.order.Remove(last)
		delete(m.items, last.Value.(*rangeItem).key)
	}
}

// Taxon returns the species with its names in lang.
func (m *RangeMapper) Taxon(code, lang string) (Taxon, bool) {
	t, ok := m.taxonomy.Lookup([]string{code}, lang)[code]
	return t, ok
}

// Search returns up to limit species whose common name in lang, English
// name, or scientific name starts with query (or has a word that does),
// shortest names first.
func (m *RangeMapper) Search(query, lang string, limit int) []Taxon {
	q := placekey.Normalize(query)
	if len(q) < 2 {
		return nil
	}
	m.indexOnce.Do(m.buildIndex)
	entries, ok := m.index[lang]
	if !ok {
		entries = m.index[defaultLang]
	}
	type hit struct {
		code   string
		name   string
		prefix bool
	}
	var hits []hit
	seen := map[string]bool{}
	for _, e := range entries {
		prefix := strings.HasPrefix(e.name, q)
		if !prefix && !strings.Contains(e.words, " "+q) {
			continue
		}
		if seen[e.code] {
			continue
		}
		seen[e.code] = true
		hits = append(hits, hit{e.code, e.name, prefix})
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].prefix != hits[b].prefix {
			return hits[a].prefix
		}
		if len(hits[a].name) != len(hits[b].name) {
			return len(hits[a].name) < len(hits[b].name)
		}
		return hits[a].name < hits[b].name
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	codes := make([]string, len(hits))
	for i, h := range hits {
		codes[i] = h.code
	}
	byCode := m.taxonomy.Lookup(codes, lang)
	out := make([]Taxon, 0, len(codes))
	for _, c := range codes {
		out = append(out, byCode[c])
	}
	return out
}

// buildIndex normalizes, per language, every species' names in that language,
// in English and in Latin, so a query is a linear scan of cheap strings.
func (m *RangeMapper) buildIndex() {
	m.index = map[string][]searchEntry{}
	for lang := range validLang {
		var list []searchEntry
		for _, t := range m.taxa {
			names := []string{m.taxonomy.names[lang][t.SpeciesCode], m.taxonomy.names["en"][t.SpeciesCode], t.SciName}
			for _, n := range names {
				if n == "" {
					continue
				}
				norm := placekey.Normalize(n)
				list = append(list, searchEntry{name: norm, words: " " + norm, code: t.SpeciesCode})
			}
		}
		m.index[lang] = list
	}
}

type speciesHit struct {
	SpeciesCode string `json:"speciesCode"`
	ComName     string `json:"comName"`
	SciName     string `json:"sciName"`
}

type rangeResponse struct {
	speciesHit
	Month int      `json:"month"`
	Max   int      `json:"max"`
	Cells [][3]int `json:"cells"`
}

func (s *Server) handleSpeciesSearch(w http.ResponseWriter, r *http.Request) {
	if s.ranges == nil {
		http.Error(w, "range maps unavailable", http.StatusNotFound)
		return
	}
	lang, ok := s.resolveLang(w, r)
	if !ok {
		return
	}
	found := s.ranges.Search(r.URL.Query().Get("q"), lang, 8)
	out := make([]speciesHit, len(found))
	for i, t := range found {
		out[i] = speciesHit{SpeciesCode: t.SpeciesCode, ComName: t.ComName, SciName: t.SciName}
	}
	writeJSON(w, out)
}

func (s *Server) handleSpeciesRange(w http.ResponseWriter, r *http.Request) {
	if s.ranges == nil {
		http.Error(w, "range maps unavailable", http.StatusNotFound)
		return
	}
	code := r.PathValue("code")
	if !speciesCodeRE.MatchString(code) {
		http.Error(w, "invalid species code", http.StatusBadRequest)
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
	taxon, ok := s.ranges.Taxon(code, lang)
	if !ok {
		http.Error(w, "unknown species", http.StatusNotFound)
		return
	}
	rc, err := s.ranges.Range(code, month)
	if err != nil {
		http.Error(w, "range lookup failed", http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("lang") != "" {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	writeJSON(w, rangeResponse{
		speciesHit: speciesHit{SpeciesCode: code, ComName: taxon.ComName, SciName: taxon.SciName},
		Month:      month, Max: rc.Max, Cells: rc.Cells,
	})
}
