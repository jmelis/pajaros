package main

import "fmt"

// fakeSpeciesSource is a test double for areaSpeciesSource — hands
// tests a fixed set of species/counts per (area, language) without any
// real area/bbolt/taxonomy data, matching areaSpeciesSource's own
// stated purpose (see species_resolver.go).
type fakeSpeciesSource struct {
	species map[string]fakeSpeciesEntry // key: key + "|" + lang
	counts  map[string]map[string]int   // key: key -> sciName -> count
	seasons map[string]map[string]SpeciesSeason
}

type fakeSpeciesEntry struct {
	codes []string
	taxa  map[string]Taxon
}

func newFakeSpeciesSource() *fakeSpeciesSource {
	return &fakeSpeciesSource{
		species: map[string]fakeSpeciesEntry{},
		counts:  map[string]map[string]int{},
	}
}

func (f *fakeSpeciesSource) set(key, lang string, codes []string, taxa map[string]Taxon) {
	f.species[key+"|"+lang] = fakeSpeciesEntry{codes: codes, taxa: taxa}
}

func (f *fakeSpeciesSource) setCounts(key string, counts map[string]int) {
	f.counts[key] = counts
}

func (f *fakeSpeciesSource) Species(key, lang string, month int) ([]string, map[string]Taxon, error) {
	e, ok := f.species[key+"|"+lang]
	if !ok {
		return nil, nil, fmt.Errorf("unknown area %q (lang %q)", key, lang)
	}
	return e.codes, e.taxa, nil
}

func (f *fakeSpeciesSource) PopularityCounts(key string, month int) (map[string]int, error) {
	return f.counts[key], nil
}

func (f *fakeSpeciesSource) Seasonality(key string) (map[string]SpeciesSeason, error) {
	return f.seasons[key], nil
}
