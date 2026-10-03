package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jmelis/pajaros/server/internal/seasonal"
)

func rangeFixture() *RangeMapper {
	tax := NewTaxonomyStore(
		map[string]coreTaxon{
			"aaa1": {SciName: "Aus bus", SpeciesCode: "aaa1"},
			"bbb1": {SciName: "Cus dus", SpeciesCode: "bbb1"},
			"ccc1": {SciName: "Eus fus", SpeciesCode: "ccc1"},
			"ddd1": {SciName: "Gus hus", SpeciesCode: "ddd1"},
		},
		map[string]map[string]string{"en": {"aaa1": "Red Robin", "bbb1": "Blue Robin", "ccc1": "Wren", "ddd1": "Tit"}},
	)
	// IDs follow species code order: aaa1=0, bbb1=1, ccc1=2, ddd1=3.
	cells := map[[2]int32][]seasonal.Entry{
		{40, -4}: {
			{ID: 0, Months: seasonal.Months{0, 0, 0, 0, 50}},
			{ID: 1, Months: seasonal.Months{0, 0, 0, 0, 50}},
			{ID: 2, Months: seasonal.Months{0, 0, 0, 0, 50}},
			{ID: 3, Months: seasonal.Months{0, 0, 0, 0, 5}},
		},
		{41, -4}: {{ID: 0, Months: seasonal.Months{1}}, {ID: 1, Months: seasonal.Months{0, 3}}},
		{10, 10}: {{ID: 1, Months: seasonal.Months{4}}},
	}
	each := func(fn func(i, j int32, es []seasonal.Entry)) error {
		for k, es := range cells {
			fn(k[0], k[1], es)
		}
		return nil
	}
	return newRangeMapper(each, tax)
}

func TestRangeFrequency(t *testing.T) {
	m := rangeFixture()
	rc, err := m.Range("aaa1", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[[2]int]int{}
	for _, c := range rc.Cells {
		got[[2]int{c[0], c[1]}] = c[2]
	}
	// Cell (40,-4): count 50, effort 50 -> 50/55 = 91%. Cell (41,-4): the
	// single record in a thin cell is smoothed down (1/(2+5) = 14%).
	if len(got) != 2 || got[[2]int{40, -4}] != 91 || got[[2]int{41, -4}] != 14 {
		t.Fatalf("cells = %v", got)
	}
	if rc.Max != 91 {
		t.Errorf("max = %d", rc.Max)
	}

	if rc, _ := m.Range("aaa1", 5); len(rc.Cells) != 1 {
		t.Errorf("May cells = %v, want only the cell recorded in May", rc.Cells)
	}
	if rc, _ := m.Range("aaa1", 2); len(rc.Cells) != 0 {
		t.Errorf("February cells = %v, want none", rc.Cells)
	}
	if _, err := m.Range("nope", 0); err != errUnknownSpecies {
		t.Errorf("unknown species err = %v", err)
	}
	if again, _ := m.Range("aaa1", 0); again != m.cacheGet(rangeCacheKey("aaa1", 0)) {
		t.Error("second call did not come from the cache")
	}
}

func TestSpeciesSearch(t *testing.T) {
	m := rangeFixture()
	got := m.Search("robin", "en", 8)
	if len(got) != 2 {
		t.Fatalf("robin: %+v", got)
	}
	for _, g := range got {
		if g.ComName != "Red Robin" && g.ComName != "Blue Robin" {
			t.Errorf("unexpected hit %+v", g)
		}
	}
	if got := m.Search("wre", "en", 8); len(got) != 1 || got[0].SpeciesCode != "ccc1" {
		t.Errorf("wre: %+v", got)
	}
	if got := m.Search("gus", "en", 8); len(got) != 1 || got[0].SpeciesCode != "ddd1" {
		t.Errorf("latin: %+v", got)
	}
	if m.Search("w", "en", 8) != nil {
		t.Error("one-letter query should match nothing")
	}
}

func TestRangeHandlers(t *testing.T) {
	srv := &Server{ranges: rangeFixture()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/species", srv.handleSpeciesSearch)
	mux.HandleFunc("GET /api/species/{code}/range", srv.handleSpeciesRange)

	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr
	}
	rr := get("/api/species/aaa1/range?lang=en")
	if rr.Code != 200 {
		t.Fatalf("range = %d", rr.Code)
	}
	var body struct {
		SpeciesCode string   `json:"speciesCode"`
		ComName     string   `json:"comName"`
		Max         int      `json:"max"`
		Cells       [][3]int `json:"cells"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SpeciesCode != "aaa1" || body.ComName != "Red Robin" || body.Max != 91 || len(body.Cells) != 2 {
		t.Errorf("body = %+v", body)
	}
	if cc := rr.Header().Get("Cache-Control"); cc == "" {
		t.Error("explicit-language range should be cacheable")
	}
	for path, want := range map[string]int{
		"/api/species/zzzz/range?lang=en":          http.StatusNotFound,
		"/api/species/AA!/range?lang=en":           http.StatusBadRequest,
		"/api/species/aaa1/range?lang=en&month=13": http.StatusBadRequest,
		"/api/species?q=robin&lang=en":             http.StatusOK,
	} {
		if code := get(path).Code; code != want {
			t.Errorf("%s = %d, want %d", path, code, want)
		}
	}
}
