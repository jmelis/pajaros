package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jmelis/pajaros/server/internal/seasonal"
	bolt "go.etcd.io/bbolt"
)

func newTestSpeciesStore(t *testing.T) *SpeciesStore {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "h.bolt"), 0o644, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	blob := seasonal.Encode([]seasonal.Entry{
		{ID: 0, Months: seasonal.Months{5, 0, 0, 0, 0, 0, 0, 0, 0, 10, 0, 0}}, // robin: 5 Jan, 10 Oct
		{ID: 1, Months: seasonal.Months{9: 40}},                               // owl: Oct only
		{ID: 2, Months: seasonal.Months{6: 7}},                                // swift: Jul only
	})
	err = db.Update(func(tx *bolt.Tx) error {
		b, _ := tx.CreateBucket([]byte(speciesBucketName))
		if err := b.Put([]byte("1.5,2.5"), blob); err != nil {
			return err
		}
		year := seasonal.Encode([]seasonal.Entry{
			{ID: 0, Months: seasonal.Months{20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20}},
			{ID: 1, Months: seasonal.Months{0: 10, 1: 10, 11: 10}},
		})
		if err := b.Put([]byte("3.5,4.5"), year); err != nil {
			return err
		}
		return b.Put([]byte("9,9"), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	return &SpeciesStore{db: db, taxonByID: []Taxon{
		{SpeciesCode: "eurrob1", SciName: "Erithacus rubecula"},
		{SpeciesCode: "tawowl1", SciName: "Strix aluco"},
		{SpeciesCode: "comswi", SciName: "Apus apus"},
	}}
}

func TestSpeciesLookupByMonth(t *testing.T) {
	s := newTestSpeciesStore(t)

	all, err := s.Lookup("1.5,2.5", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []SpeciesCount{{"tawowl1", "Strix aluco", 40}, {"eurrob1", "Erithacus rubecula", 15}, {"comswi", "Apus apus", 7}}
	if len(all) != 3 || all[0] != want[0] || all[1] != want[1] || all[2] != want[2] {
		t.Errorf("whole year = %v, want %v", all, want)
	}

	oct, _ := s.Lookup("1.5,2.5", 10)
	if len(oct) != 2 || oct[0].Code != "tawowl1" || oct[0].Count != 40 || oct[1].Code != "eurrob1" || oct[1].Count != 10 {
		t.Errorf("october = %v, want owl 40 then robin 10", oct)
	}

	jan, _ := s.Lookup("1.5,2.5", 1)
	if len(jan) != 1 || jan[0].Code != "eurrob1" || jan[0].Count != 5 {
		t.Errorf("january = %v, want robin 5 only", jan)
	}

	mar, err := s.Lookup("1.5,2.5", 3)
	if err != nil || len(mar) != 0 {
		t.Errorf("march = %v, %v; want an empty list and no error for a known hotspot", mar, err)
	}
}

func TestSpeciesLookupUnknownHotspot(t *testing.T) {
	s := newTestSpeciesStore(t)
	for _, id := range []string{"0,0", "9,9"} {
		if _, err := s.Lookup(id, 0); err != errUnknownHotspot {
			t.Errorf("Lookup(%q) err = %v, want errUnknownHotspot", id, err)
		}
	}
}

func TestParseMonth(t *testing.T) {
	cases := map[string]struct {
		month int
		ok    bool
	}{"": {0, true}, "0": {0, true}, "1": {1, true}, "12": {12, true}, "13": {0, false}, "-1": {0, false}, "oct": {0, false}}
	for q, want := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/x?month="+q, nil)
		m, ok := parseMonth(w, r)
		if m != want.month || ok != want.ok {
			t.Errorf("month=%q: got (%d,%v), want (%d,%v)", q, m, ok, want.month, want.ok)
		}
		if !ok && w.Code != http.StatusBadRequest {
			t.Errorf("month=%q: status %d, want 400", q, w.Code)
		}
	}
}
