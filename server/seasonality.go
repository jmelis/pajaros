package main

import (
	"sort"

	"github.com/jmelis/pajaros/server/internal/seasonal"
)

// Season kinds, in the order the Seasons view lists its groups.
const (
	seasonYearRound  = "yearround"
	seasonSeasonal   = "seasonal"
	seasonOccasional = "occasional"
)

const (
	// minEffort is the busiest species' record count in the hotspot's
	// busiest month; with fewer records than this there is too little data
	// to call anything seasonal, so no species is classified.
	minEffort = 5
	// occasionalTotal and occasionalRate: a species with at most this many
	// records overall, or whose best month reaches under this fraction of the
	// month's observer effort, is occasional.
	occasionalTotal = 2
	occasionalRate  = 0.05
	// presentFrac: a month counts as "present" when the species' rate in it
	// is at least this fraction of its own best month.
	presentFrac = 0.25
	// yearRoundMonths: present in at least this many months = year-round.
	yearRoundMonths = 9
)

// SpeciesSeason is one species' place in a hotspot's year.
type SpeciesSeason struct {
	SciName string
	Kind    string
	// Months is a bitmask, bit m-1 set for calendar month m: the months the
	// species is present in (for occasional ones, any month with a record).
	Months uint16
	Total  int
	// Peak is the calendar month (1..12) where the species is most frequent.
	Peak int
}

// classifySeasons sorts a hotspot's species into year-round, seasonal and
// occasional from their per-month record counts. Counts are checklist
// records, so months with more observers have more of everything; the
// busiest species' counts approximate each month's observer effort, and a
// species' rate is its count over that effort. Returns nil when the hotspot
// has too little data (see minEffort).
func classifySeasons(entries []seasonal.Entry) map[uint16]SpeciesSeason {
	var effort [12]float64
	for m := 0; m < 12; m++ {
		counts := make([]int, 0, len(entries))
		for _, e := range entries {
			if e.Months[m] > 0 {
				counts = append(counts, int(e.Months[m]))
			}
		}
		sort.Sort(sort.Reverse(sort.IntSlice(counts)))
		top := counts
		if len(top) > 3 {
			top = top[:3]
		}
		sum := 0
		for _, c := range top {
			sum += c
		}
		if len(top) > 0 {
			effort[m] = float64(sum) / float64(len(top))
		}
	}
	busiest := 0.0
	for _, f := range effort {
		if f > busiest {
			busiest = f
		}
	}
	if busiest < minEffort {
		return nil
	}

	out := make(map[uint16]SpeciesSeason, len(entries))
	for _, e := range entries {
		var rate [12]float64
		peakRate, peak := 0.0, 0
		var recorded uint16
		for m := 0; m < 12; m++ {
			if e.Months[m] == 0 {
				continue
			}
			recorded |= 1 << m
			if effort[m] > 0 {
				rate[m] = float64(e.Months[m]) / effort[m]
				if rate[m] > 1 {
					rate[m] = 1
				}
			}
			if rate[m] > peakRate {
				peakRate, peak = rate[m], m+1
			}
		}
		total := e.Months.Total()
		s := SpeciesSeason{Total: total, Peak: peak}
		if total <= occasionalTotal || peakRate < occasionalRate {
			s.Kind, s.Months = seasonOccasional, recorded
			if peak == 0 {
				s.Peak = firstMonth(recorded)
			}
		} else {
			present, n := uint16(0), 0
			for m := 0; m < 12; m++ {
				if rate[m] >= presentFrac*peakRate {
					present |= 1 << m
					n++
				}
			}
			s.Months = present
			if n >= yearRoundMonths {
				s.Kind = seasonYearRound
			} else {
				s.Kind = seasonSeasonal
			}
		}
		out[e.ID] = s
	}
	return out
}

func firstMonth(mask uint16) int {
	for m := 0; m < 12; m++ {
		if mask&(1<<m) != 0 {
			return m + 1
		}
	}
	return 0
}
