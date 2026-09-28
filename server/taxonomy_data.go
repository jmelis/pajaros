package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Worldwide eBird taxonomy, one table per supported bird-name language (see
// validLang), refreshed by cmd/gensnapshot alongside the hotspot snapshot.
// Small and effectively static — eBird updates its taxonomy roughly once a
// year — so, like hotspots, it's embedded rather than fetched live.
//
//go:embed data/taxonomy_en.json.gz
var embeddedTaxonomyEn []byte

//go:embed data/taxonomy_es.json.gz
var embeddedTaxonomyEs []byte

//go:embed data/taxonomy_fr.json.gz
var embeddedTaxonomyFr []byte

// TaxonomyStore answers taxonomy lookups entirely from the embedded tables.
// SciName, Order, FamilyCode and TaxonOrder don't vary by locale (only
// ComName does — see the note on Taxon), so CodeForSciName and
// SortByTaxonOrder are locale-independent and use core, one fixed locale's
// table, rather than needing a lang argument.
type TaxonomyStore struct {
	byLang        map[string]map[string]Taxon // lang -> species code -> Taxon
	core          map[string]Taxon            // one locale's table, for locale-independent fields
	codeBySciName map[string]string
}

// NewTaxonomyStore builds a store from per-language code->Taxon tables. Used
// directly by tests with a handful of fixture species, and by
// loadTaxonomyStore with the full worldwide tables.
func NewTaxonomyStore(byLang map[string]map[string]Taxon) *TaxonomyStore {
	var core map[string]Taxon
	for _, lang := range []string{"en", "es", "fr"} {
		if t, ok := byLang[lang]; ok {
			core = t
			break
		}
	}
	codeBySciName := make(map[string]string, len(core))
	for code, t := range core {
		if t.SciName != "" {
			codeBySciName[t.SciName] = code
		}
	}
	return &TaxonomyStore{byLang: byLang, core: core, codeBySciName: codeBySciName}
}

// loadTaxonomyStore decompresses and parses the three embedded worldwide
// taxonomy tables.
func loadTaxonomyStore() (*TaxonomyStore, error) {
	byLang := map[string]map[string]Taxon{}
	for lang, data := range map[string][]byte{
		"en": embeddedTaxonomyEn,
		"es": embeddedTaxonomyEs,
		"fr": embeddedTaxonomyFr,
	} {
		taxa, err := decodeGzippedJSON[[]Taxon](data)
		if err != nil {
			return nil, fmt.Errorf("embedded %s taxonomy: %w", lang, err)
		}
		m := make(map[string]Taxon, len(taxa))
		for _, t := range taxa {
			m[t.SpeciesCode] = t
		}
		byLang[lang] = m
	}
	return NewTaxonomyStore(byLang), nil
}

// CodeForSciName resolves a GBIF scientific name to an eBird species code.
func (t *TaxonomyStore) CodeForSciName(sciName string) (string, bool) {
	code, ok := t.codeBySciName[sciName]
	return code, ok
}

// Lookup returns the Taxon for each of codes in lang, silently dropping any
// code the taxonomy doesn't recognize.
func (t *TaxonomyStore) Lookup(codes []string, lang string) map[string]Taxon {
	table := t.byLang[lang]
	out := make(map[string]Taxon, len(codes))
	for _, code := range codes {
		if taxon, ok := table[code]; ok {
			out[code] = taxon
		}
	}
	return out
}

// SortByTaxonOrder sorts codes in place into eBird's taxonomic order — the
// same grouping the old /product/spplist response gave for free (verified:
// exactly ascending taxonOrder), which "category" mode relies on to group
// species by family.
func (t *TaxonomyStore) SortByTaxonOrder(codes []string) {
	sort.Slice(codes, func(i, j int) bool {
		return t.core[codes[i]].TaxonOrder < t.core[codes[j]].TaxonOrder
	})
}

// StableIDOrder returns every species sorted by eBird speciesCode — the same
// deterministic ordering cmd/gensnapshot uses to assign each species a
// uint16 ID when building hotspots.bolt (see its loadSpeciesIndex). Position
// i in the returned slice is species ID i; species_store.go uses this to
// decode bbolt's binary blobs back into species codes/names without needing
// a separately shipped ID-mapping file.
func (t *TaxonomyStore) StableIDOrder() []Taxon {
	taxa := make([]Taxon, 0, len(t.core))
	for _, tx := range t.core {
		taxa = append(taxa, tx)
	}
	sort.Slice(taxa, func(i, j int) bool { return taxa[i].SpeciesCode < taxa[j].SpeciesCode })
	return taxa
}

// decodeGzippedJSON gunzips data and decodes it as JSON into T. Used for the
// embedded, git-committed snapshots (taxonomy) — the much larger hotspot
// data isn't embedded (see hotspots_data.go) and has its own file-based
// loader.
func decodeGzippedJSON[T any](data []byte) (T, error) {
	var zero T
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return zero, err
	}
	defer gz.Close()
	raw, err := io.ReadAll(gz)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, err
	}
	return out, nil
}
