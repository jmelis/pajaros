package main

import (
	"encoding/json"
	"strings"
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
	if got[2].Peak != 7 {
		t.Errorf("summer species peak = %d, want 7 (July)", got[2].Peak)
	}
	// Bars: relative to the species' own best month, nonzero exactly where
	// the species has records, and the peak month at full height.
	if b := got[2].Bars; b[6] != 100 || b[5] == 0 || b[7] == 0 || b[0] != 0 || b[11] != 0 {
		t.Errorf("summer species bars = %v, want 100 in July, nonzero Jun/Aug, zero in winter", b)
	}
	if b := got[3].Bars; b[0] == 0 || b[1] == 0 || b[11] == 0 || b[6] != 0 || (b[0] != 100 && b[1] != 100 && b[11] != 100) {
		t.Errorf("winter species bars = %v, want a Dec-Feb run with its peak inside it", b)
	}
	if b := got[4].Bars; b[0] == 0 || b[4] == 0 || b[2] != 0 {
		t.Errorf("occasional species bars = %v, want only the recorded months set", b)
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

func TestSortBySeason(t *testing.T) {
	cards := []SpeciesCard{
		{SciName: "occ"}, {SciName: "none"}, {SciName: "wint"}, {SciName: "year"}, {SciName: "summ"},
	}
	seasons := map[string]SpeciesSeason{
		"occ":  {Kind: seasonOccasional, Total: 2},
		"wint": {Kind: seasonSeasonal, Peak: 1, Total: 50},
		"year": {Kind: seasonYearRound, Total: 900},
		"summ": {Kind: seasonSeasonal, Peak: 7, Total: 500},
	}
	labelSeasons(cards, seasons)
	sortBySeason(cards, seasons)
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
	got, err := s.Seasons("3.5,4.5")
	if err != nil {
		t.Fatal(err)
	}
	if got["Strix aluco"].Kind != seasonSeasonal || got["Erithacus rubecula"].Kind != seasonYearRound {
		t.Errorf("seasons not resolved by scientific name: %v", got)
	}
	if thin, err := s.Seasons("1.5,2.5"); err != nil || len(thin) != 0 {
		t.Errorf("hotspot with records in 3 months classified: %v, %v", thin, err)
	}
	if _, err := s.Seasons("0,0"); err != errUnknownHotspot {
		t.Errorf("unknown hotspot err = %v", err)
	}
}

func TestClassifySeasonsThinMonths(t *testing.T) {
	// January has no records and May only a couple: those months say nothing
	// about presence, so a bird seen in every other month is year-round and
	// both months are marked no-data rather than 0.
	robin := seasonal.Months{0, 20, 20, 20, 1, 20, 20, 20, 20, 20, 20, 20}
	crow := seasonal.Months{0, 15, 15, 15, 1, 15, 15, 15, 15, 15, 15, 15}
	got := classifySeasons([]seasonal.Entry{{ID: 0, Months: robin}, {ID: 1, Months: crow}})
	if got[1].Kind != seasonYearRound {
		t.Errorf("bird in every month with data = %q, want year-round", got[1].Kind)
	}
	if b := got[1].Bars; b[0] != noData || b[4] != noData || b[1] == noData {
		t.Errorf("bars = %v, want no-data in Jan and May only", b)
	}
}

func TestSeasonBarsMarshalAsArray(t *testing.T) {
	cards := []SpeciesCard{{SciName: "a"}}
	labelSeasons(cards, map[string]SpeciesSeason{"a": {Kind: seasonSeasonal, Bars: [12]int8{0, 5, 100, noData}}})
	b, err := json.Marshal(cards[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"seasonBars":[0,5,100,-1,0,0,0,0,0,0,0,0]`) {
		t.Errorf("seasonBars not a JSON number array: %s", b)
	}
}
