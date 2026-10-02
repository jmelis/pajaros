package main

import (
	"testing"

	"github.com/jmelis/pajaros/server/internal/seasonal"
)

func flat(n uint32) seasonal.Months {
	var m seasonal.Months
	for i := range m {
		m[i] = n
	}
	return m
}

func TestClassifySeasons(t *testing.T) {
	entries := []seasonal.Entry{
		{ID: 0, Months: flat(100)},                                                       // every month, busiest
		{ID: 1, Months: flat(60)},                                                        // year-round
		{ID: 2, Months: seasonal.Months{5: 80, 6: 90, 7: 70}},                            // Jun-Aug only
		{ID: 3, Months: seasonal.Months{0: 50, 1: 60, 11: 40}},                           // Dec-Feb: wraps the year
		{ID: 4, Months: seasonal.Months{0: 1, 4: 1}},                                     // two records overall
		{ID: 5, Months: seasonal.Months{0: 2, 1: 2, 2: 2, 3: 2, 4: 2, 5: 2, 6: 2, 7: 2}}, // never above ~2% of effort
		{ID: 6, Months: seasonal.Months{2: 100, 3: 100, 4: 100, 5: 100, 6: 100, 7: 100, 8: 100, 9: 100, 10: 100, 1: 100}},
	}
	got := classifySeasons(entries)
	kind := func(id uint16) string { return got[id].Kind }

	for id, want := range map[uint16]string{
		0: seasonYearRound,
		1: seasonYearRound,
		2: seasonSeasonal,
		3: seasonSeasonal,
		4: seasonOccasional,
		5: seasonOccasional,
		6: seasonYearRound,
	} {
		if kind(id) != want {
			t.Errorf("species %d = %q, want %q", id, kind(id), want)
		}
	}
	if m := got[2].Months; m != 1<<5|1<<6|1<<7 {
		t.Errorf("summer species months = %012b, want Jun-Aug", m)
	}
	if got[2].Peak != 7 {
		t.Errorf("summer species peak = %d, want 7 (July)", got[2].Peak)
	}
	if m := got[3].Months; m != 1<<0|1<<1|1<<11 {
		t.Errorf("winter species months = %012b, want Dec, Jan, Feb", m)
	}
	if got[4].Months != 1<<0|1<<4 {
		t.Errorf("occasional species months = %012b, want the recorded ones", got[4].Months)
	}
}

func TestClassifySeasonsAdjustsForEffort(t *testing.T) {
	// Observers pile in during July: a bird reported at a steady share of
	// checklists has far more records then, but is still year-round.
	busy := seasonal.Months{10, 10, 10, 10, 10, 10, 100, 10, 10, 10, 10, 10}
	steady := seasonal.Months{5, 5, 5, 5, 5, 5, 50, 5, 5, 5, 5, 5}
	got := classifySeasons([]seasonal.Entry{{ID: 0, Months: busy}, {ID: 1, Months: steady}})
	if got[1].Kind != seasonYearRound {
		t.Errorf("steady bird = %q, want year-round despite the July surge", got[1].Kind)
	}
}

func TestClassifySeasonsTooLittleData(t *testing.T) {
	if got := classifySeasons([]seasonal.Entry{{ID: 0, Months: seasonal.Months{0: 2}}, {ID: 1, Months: seasonal.Months{3: 1}}}); got != nil {
		t.Errorf("tiny hotspot classified: %v", got)
	}
	if got := classifySeasons(nil); got != nil {
		t.Errorf("empty hotspot classified: %v", got)
	}
}

func TestApplySeasonsOrder(t *testing.T) {
	cards := []SpeciesCard{
		{SciName: "occ"}, {SciName: "none"}, {SciName: "wint"}, {SciName: "year"}, {SciName: "summ"},
	}
	applySeasons(cards, map[string]SpeciesSeason{
		"occ":  {Kind: seasonOccasional, Total: 2},
		"wint": {Kind: seasonSeasonal, Peak: 1, Total: 50},
		"year": {Kind: seasonYearRound, Total: 900},
		"summ": {Kind: seasonSeasonal, Peak: 7, Total: 500},
	})
	var order []string
	for _, c := range cards {
		order = append(order, c.SciName)
	}
	want := []string{"year", "wint", "summ", "occ", "none"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if cards[3].Season != seasonOccasional || cards[4].Season != "" {
		t.Errorf("seasons not set as expected: %+v", cards)
	}
}

func TestSpeciesSeasonsFromStore(t *testing.T) {
	s := newTestSpeciesStore(t)
	// Too little data in the shared fixture (busiest species has 40 in
	// October only, so effort is fine) — just check names resolve and errors pass through.
	got, err := s.Seasons("1.5,2.5")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["Strix aluco"]; !ok {
		t.Errorf("owl missing from %v", got)
	}
	if _, err := s.Seasons("0,0"); err != errUnknownHotspot {
		t.Errorf("unknown hotspot err = %v", err)
	}
}
