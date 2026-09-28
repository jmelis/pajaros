package main

import "sort"

// sortByPopularity orders items in place using the app's shared popularity
// rule: highest observation count first, ties broken by common name
// ascending, and items with no count data (count returns false) last. The
// species endpoint's "popularity" mode and the quiz session pool both call
// this, so the ordering is defined in exactly one place.
func sortByPopularity[T any](items []T, count func(T) (int, bool), comName func(T) string) {
	sort.SliceStable(items, func(i, j int) bool {
		ci, iok := count(items[i])
		cj, jok := count(items[j])
		vi, vj := -1, -1
		if iok {
			vi = ci
		}
		if jok {
			vj = cj
		}
		if vi != vj {
			return vi > vj
		}
		return comName(items[i]) < comName(items[j])
	})
}
