package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
)

// Worldwide eBird taxonomic structure (sciName, speciesCode, order,
// familyCode, taxonOrder — see the note on Taxon below) plus GBIF's
// vernacular names for those species, refreshed by cmd/gensnapshot
// alongside the hotspot snapshot. Small and effectively static — eBird
// updates its taxonomy roughly once a year — so, like hotspots, it's
// embedded rather than fetched live.
//
//go:embed data/taxonomy_core.json.gz
var embeddedTaxonomyCore []byte

// embeddedSpeciesNames holds only the languages gensnapshot's taxonomy
// command found adequate GBIF vernacular-name coverage for (see its
// coverage report) — so its language keys are exactly validLang (see
// main.go, which sets validLang from TaxonomyStore.SupportedLangs at
// startup).
//
//go:embed data/species_names.json.gz
var embeddedSpeciesNames []byte

// validLang is the set of bird-name languages the frontend offers (see
// static/index.html's #lang select) — every language embeddedSpeciesNames
// carries data for. Computed eagerly (not inside loadTaxonomyStore, which
// only the real server calls) so it's ready for request validation even in
// tests that build a Server directly without going through main().
var validLang = mustComputeValidLang()

func mustComputeValidLang() map[string]bool {
	names, err := decodeGzippedJSON[map[string]map[string]string](embeddedSpeciesNames)
	if err != nil {
		log.Fatalf("embedded species names: %v", err)
	}
	out := make(map[string]bool, len(names))
	for lang := range names {
		out[lang] = true
	}
	return out
}

// Taxon is one species' taxonomy data. SciName, SpeciesCode, Order,
// FamilyCode and TaxonOrder come from eBird — pure taxonomic structure,
// not creative naming, and cheap to keep current with one eBird fetch (see
// cmd/gensnapshot's taxonomy command). ComName (and, separately, each
// family's display name — see families.go) comes from GBIF's vernacular
// names instead of eBird's own per-locale common names: eBird/Clements'
// translated checklist text carries licensing restrictions on
// redistribution that its bare taxonomic structure doesn't.
type Taxon struct {
	SciName     string  `json:"sciName"`
	ComName     string  `json:"comName"`
	SpeciesCode string  `json:"speciesCode"`
	Order       string  `json:"order"`
	FamilyCode  string  `json:"familyCode"`
	TaxonOrder  float64 `json:"taxonOrder"`
}

// coreTaxon is the locale-independent subset of Taxon: eBird's own
// taxonomic structure, decoded straight from the embedded core file.
type coreTaxon struct {
	SciName     string  `json:"sciName"`
	SpeciesCode string  `json:"speciesCode"`
	Order       string  `json:"order"`
	FamilyCode  string  `json:"familyCode"`
	TaxonOrder  float64 `json:"taxonOrder"`
}

// TaxonomyStore answers taxonomy lookups entirely from the embedded tables:
// eBird's structure (core) plus GBIF's per-language common names (names).
type TaxonomyStore struct {
	core          map[string]coreTaxon         // speciesCode -> eBird's taxonomic structure
	names         map[string]map[string]string // lang -> speciesCode -> GBIF common name
	codeBySciName map[string]string
}

// NewTaxonomyStore builds a store from a core taxonomy table and per-language
// common-name tables. Used directly by tests with a handful of fixture
// species, and by loadTaxonomyStore with the full worldwide tables.
func NewTaxonomyStore(core map[string]coreTaxon, names map[string]map[string]string) *TaxonomyStore {
	codeBySciName := make(map[string]string, len(core))
	for code, t := range core {
		if t.SciName != "" {
			codeBySciName[t.SciName] = code
		}
	}
	return &TaxonomyStore{core: core, names: names, codeBySciName: codeBySciName}
}

// loadTaxonomyStore decompresses and parses the embedded core taxonomy and
// GBIF common-name tables.
func loadTaxonomyStore() (*TaxonomyStore, error) {
	coreList, err := decodeGzippedJSON[[]coreTaxon](embeddedTaxonomyCore)
	if err != nil {
		return nil, fmt.Errorf("embedded taxonomy core: %w", err)
	}
	core := make(map[string]coreTaxon, len(coreList))
	for _, t := range coreList {
		core[t.SpeciesCode] = t
	}

	names, err := decodeGzippedJSON[map[string]map[string]string](embeddedSpeciesNames)
	if err != nil {
		return nil, fmt.Errorf("embedded species names: %w", err)
	}

	return NewTaxonomyStore(core, names), nil
}

// CodeForSciName resolves a GBIF scientific name to an eBird species code.
func (t *TaxonomyStore) CodeForSciName(sciName string) (string, bool) {
	code, ok := t.codeBySciName[sciName]
	return code, ok
}

// Lookup returns the Taxon for each of codes in lang, silently dropping any
// code the taxonomy doesn't recognize. ComName falls back from lang to
// English to the species' own scientific name — never to eBird's common
// name (see the note on Taxon) — covering both a language GBIF didn't have
// this species in and a species GBIF has no vernacular name for at all
// (roughly 12% of eBird's species, mostly taxonomic splits/lumps GBIF's
// backbone doesn't share with eBird/Clements).
func (t *TaxonomyStore) Lookup(codes []string, lang string) map[string]Taxon {
	byLang := t.names[lang]
	byEnglish := t.names["en"]
	out := make(map[string]Taxon, len(codes))
	for _, code := range codes {
		core, ok := t.core[code]
		if !ok {
			continue
		}
		comName := byLang[code]
		if comName == "" {
			comName = byEnglish[code]
		}
		if comName == "" {
			comName = core.SciName
		}
		out[code] = Taxon{
			SciName:     core.SciName,
			ComName:     comName,
			SpeciesCode: core.SpeciesCode,
			Order:       core.Order,
			FamilyCode:  core.FamilyCode,
			TaxonOrder:  core.TaxonOrder,
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
// a separately shipped ID-mapping file. ComName is left empty — callers that
// need it (species_store.go doesn't) should look the code up via Lookup.
func (t *TaxonomyStore) StableIDOrder() []Taxon {
	taxa := make([]Taxon, 0, len(t.core))
	for _, c := range t.core {
		taxa = append(taxa, Taxon{
			SciName:     c.SciName,
			SpeciesCode: c.SpeciesCode,
			Order:       c.Order,
			FamilyCode:  c.FamilyCode,
			TaxonOrder:  c.TaxonOrder,
		})
	}
	sort.Slice(taxa, func(i, j int) bool { return taxa[i].SpeciesCode < taxa[j].SpeciesCode })
	return taxa
}

// decodeGzippedJSON gunzips data and decodes it as JSON into T. Used for the
// embedded, git-committed snapshots (taxonomy, families) — the much larger
// hotspot data isn't embedded (see hotspots_data.go) and has its own
// file-based loader.
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
