package main

import "fmt"

// fakeSpeciesSource is a test double for hotspotSpeciesSource — hands
// tests a fixed set of species/counts per (hotspot, language) without any
// real hotspot/bbolt/taxonomy data, matching hotspotSpeciesSource's own
// stated purpose (see species_resolver.go).
type fakeSpeciesSource struct {
	species map[string]fakeSpeciesEntry // key: locID + "|" + lang
	counts  map[string]map[string]int   // key: locID -> sciName -> count
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

func (f *fakeSpeciesSource) set(locID, lang string, codes []string, taxa map[string]Taxon) {
	f.species[locID+"|"+lang] = fakeSpeciesEntry{codes: codes, taxa: taxa}
}

func (f *fakeSpeciesSource) setCounts(locID string, counts map[string]int) {
	f.counts[locID] = counts
}

func (f *fakeSpeciesSource) Species(locID, lang string, month int) ([]string, map[string]Taxon, error) {
	e, ok := f.species[locID+"|"+lang]
	if !ok {
		return nil, nil, fmt.Errorf("unknown hotspot %q (lang %q)", locID, lang)
	}
	return e.codes, e.taxa, nil
}

func (f *fakeSpeciesSource) PopularityCounts(locID string, month int) (map[string]int, error) {
	return f.counts[locID], nil
}

func (f *fakeSpeciesSource) Seasonality(locID string) (map[string]SpeciesSeason, error) {
	return f.seasons[locID], nil
}
