package main

import (
	"math"
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
	// minEffort is the busiest species' record count (the month's observer
	// effort) a month needs to say anything about which species are present;
	// a thinner month is "no data", neither present nor absent.
	minEffort = 3
	// minMonths is how many months with data a hotspot needs before any
	// species is classified.
	minMonths = 6
	// occasionalTotal and occasionalRate: a species with at most this many
	// records overall, or whose best month reaches under this fraction of the
	// month's observer effort, is occasional.
	occasionalTotal = 2
	occasionalRate  = 0.05
	// presentFrac: a month counts as "present" when the species' rate in it
	// is at least this fraction of its own best month.
	presentFrac = 0.25
	// yearRoundFrac: present in at least this fraction of the months with
	// data = year-round.
	yearRoundFrac = 0.75
)

// SpeciesSeason is one species' place in a hotspot's year.
type SpeciesSeason struct {
	SciName string
	Kind    string
	// Bars is the species' reporting rate per calendar month (index 0 =
	// January), 0..100 relative to its own best month; any month with a
	// record is at least 1. noData marks a month too thin to tell.
	Bars  [12]int8
	Total int
	// Peak is the calendar month (1..12) where the species is most frequent.
	Peak int
}

// classifySeasons sorts a hotspot's species into year-round, seasonal and
// occasional from their per-month record counts. Counts are checklist
// records, so months with more observers have more of everything; the
// busiest species' counts approximate each month's observer effort, and a
// species' rate is its count over that effort. Months whose effort is below
// minEffort carry no information and are left out; nil is returned when fewer
// than minMonths months remain.
const noData = -1

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
	known := 0
	var thin [12]bool
	for m, f := range effort {
		if f < minEffort {
			thin[m] = true
		} else {
			known++
		}
	}
	if known < minMonths {
		return nil
	}

	out := make(map[uint16]SpeciesSeason, len(entries))
	for _, e := range entries {
		var rate, raw [12]float64 // rate is capped at 1; raw is not
		peakRate, peakRaw, peak := 0.0, 0.0, 0
		for m := 0; m < 12; m++ {
			if e.Months[m] == 0 || thin[m] {
				continue
			}
			raw[m] = float64(e.Months[m]) / effort[m]
			rate[m] = math.Min(raw[m], 1)
			if rate[m] > peakRate {
				peakRate, peak = rate[m], m+1
			}
			peakRaw = math.Max(peakRaw, raw[m])
		}
		total := e.Months.Total()
		s := SpeciesSeason{Total: total, Peak: peak}
		for m := 0; m < 12; m++ {
			switch {
			case thin[m]:
				s.Bars[m] = noData
			case e.Months[m] > 0 && peakRaw > 0:
				s.Bars[m] = int8(math.Max(1, math.Round(100*raw[m]/peakRaw)))
			}
		}
		if total <= occasionalTotal || peakRate < occasionalRate {
			s.Kind = seasonOccasional
			if peak == 0 {
				s.Peak = firstMonth(e.Months)
			}
		} else {
			n := 0
			for m := 0; m < 12; m++ {
				if !thin[m] && rate[m] >= presentFrac*peakRate {
					n++
				}
			}
			if float64(n) >= yearRoundFrac*float64(known) {
				s.Kind = seasonYearRound
			} else {
				s.Kind = seasonSeasonal
			}
		}
		out[e.ID] = s
	}
	return out
}

func firstMonth(months seasonal.Months) int {
	for m, n := range months {
		if n > 0 {
			return m + 1
		}
	}
	return 0
}
