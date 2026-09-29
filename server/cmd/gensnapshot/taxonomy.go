package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// runTaxonomy rebuilds all three embedded taxonomy files server/taxonomy_data.go
// and server/families.go expect:
//
//   - data/taxonomy_core.json.gz: eBird's taxonomic structure (sciName,
//     speciesCode, order, familyCode, taxonOrder) — one eBird call, any
//     locale, since none of these fields vary by locale.
//   - data/species_names.json.gz: GBIF vernacular names per species, one
//     language per key, restricted to languages with at least
//     minSpeciesCoverage of eBird's species named — this is exactly
//     validLang at runtime (see TaxonomyStore.SupportedLangs).
//   - data/family_names.json.gz: GBIF vernacular names per eBird family,
//     every language GBIF had anything for (no threshold — FamilyName
//     falls back to English, then to the family's own scientific name, so
//     sparse coverage degrades gracefully rather than needing a cutoff).
//
// eBird's own common names (per-species or per-family) are deliberately not
// used here — see the note on Taxon in ../../taxonomy_data.go. Only eBird's bare
// taxonomic structure is fetched from eBird; every name comes from GBIF.
func runTaxonomy() error {
	apiKey := os.Getenv("EBIRD_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("EBIRD_API_KEY not set")
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "fetching eBird taxonomic structure...")
	ebirdSpecies, err := fetchEbirdStructure(apiKey)
	if err != nil {
		return fmt.Errorf("fetch eBird structure: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%d eBird species\n", len(ebirdSpecies))

	core := make([]coreTaxonOut, 0, len(ebirdSpecies))
	ebirdBySciName := make(map[string]ebirdTaxonEntry, len(ebirdSpecies))
	for _, e := range ebirdSpecies {
		core = append(core, coreTaxonOut{
			SciName:     e.SciName,
			SpeciesCode: e.SpeciesCode,
			Order:       e.Order,
			FamilyCode:  e.FamilyCode,
			TaxonOrder:  e.TaxonOrder,
		})
		ebirdBySciName[e.SciName] = e
	}
	if err := writeGzippedJSON("data/taxonomy_core.json.gz", core); err != nil {
		return fmt.Errorf("write taxonomy_core.json.gz: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote data/taxonomy_core.json.gz (%d species)\n", len(core))

	fmt.Fprintln(os.Stderr, "fetching GBIF bird families...")
	gbifFamilies, err := fetchGBIFFamilies()
	if err != nil {
		return fmt.Errorf("fetch GBIF families: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%d GBIF bird families\n", len(gbifFamilies))
	familyByName := make(map[string]gbifTaxon, len(gbifFamilies))
	for _, f := range gbifFamilies {
		familyByName[f.CanonicalName] = f
	}

	// speciesNameVotes[code][lang][name] = how many GBIF records offered
	// that exact string — see pickCanonical.
	speciesNameVotes := map[string]map[string]map[string]int{}
	// familyVotes[ebirdFamilyCode][gbifFamilyName] = how many member
	// species joined eBird's family to that GBIF family.
	familyVotes := map[string]map[string]int{}

	for i, fam := range gbifFamilies {
		members, err := fetchGBIFSpeciesForFamily(fam.Key)
		if err != nil {
			return fmt.Errorf("fetch species for family %s (%d): %w", fam.CanonicalName, fam.Key, err)
		}
		for _, sp := range members {
			e, ok := ebirdBySciName[sp.CanonicalName]
			if !ok {
				continue // taxonomic split/lump/spelling mismatch vs. eBird — see the note on Taxon
			}
			if familyVotes[e.FamilyCode] == nil {
				familyVotes[e.FamilyCode] = map[string]int{}
			}
			familyVotes[e.FamilyCode][fam.CanonicalName]++

			for _, vn := range sp.VernacularNames {
				lang, ok := iso6391(vn.Language)
				if !ok || vn.VernacularName == "" {
					continue
				}
				byLang, ok := speciesNameVotes[e.SpeciesCode]
				if !ok {
					byLang = map[string]map[string]int{}
					speciesNameVotes[e.SpeciesCode] = byLang
				}
				byName, ok := byLang[lang]
				if !ok {
					byName = map[string]int{}
					byLang[lang] = byName
				}
				byName[vn.VernacularName]++
			}
		}
		if (i+1)%50 == 0 || i == len(gbifFamilies)-1 {
			fmt.Fprintf(os.Stderr, "  %d/%d GBIF families processed\n", i+1, len(gbifFamilies))
		}
	}

	speciesNames, coverage := buildSpeciesNames(speciesNameVotes, len(core))
	fmt.Fprintln(os.Stderr, "\nspecies-name coverage (% of eBird's species with a GBIF common name):")
	printCoverage(coverage)

	included := map[string]map[string]string{}
	var includedLangs, excludedLangs []string
	for lang, byCode := range speciesNames {
		if coverage[lang] >= minSpeciesCoverage {
			included[lang] = byCode
			includedLangs = append(includedLangs, lang)
		} else {
			excludedLangs = append(excludedLangs, lang)
		}
	}
	sort.Strings(includedLangs)
	fmt.Fprintf(os.Stderr, "\n%d languages at/above %.0f%% coverage (these become validLang): %v\n",
		len(includedLangs), minSpeciesCoverage*100, includedLangs)
	fmt.Fprintf(os.Stderr, "%d languages below threshold, excluded\n", len(excludedLangs))

	if err := writeGzippedJSON("data/species_names.json.gz", included); err != nil {
		return fmt.Errorf("write species_names.json.gz: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote data/species_names.json.gz\n")

	familyNames, famCoverage := buildFamilyNames(familyVotes, familyByName)
	fmt.Fprintln(os.Stderr, "\nfamily-name coverage (% of eBird's families with a GBIF common name; no threshold applied):")
	printCoverage(famCoverage)

	if err := writeGzippedJSON("data/family_names.json.gz", familyNames); err != nil {
		return fmt.Errorf("write family_names.json.gz: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote data/family_names.json.gz (%d families)\n", len(familyNames))

	return nil
}

// minSpeciesCoverage is the fraction of eBird's species a language needs a
// GBIF common name for to be offered as a bird-name language at all — see
// the coverage study this was chosen from: a clean cliff separates ~20
// languages at 80%+ from a long tail below it (many well under 10%).
const minSpeciesCoverage = 0.80

// buildSpeciesNames picks one canonical name per (species code, language)
// out of every GBIF vernacular-name candidate seen, and returns each
// language's coverage (fraction of totalSpecies it has a name for).
func buildSpeciesNames(votes map[string]map[string]map[string]int, totalSpecies int) (map[string]map[string]string, map[string]float64) {
	byLang := map[string]map[string]string{}
	counts := map[string]int{}
	for code, langs := range votes {
		for lang, names := range langs {
			name := pickCanonical(names)
			if byLang[lang] == nil {
				byLang[lang] = map[string]string{}
			}
			byLang[lang][code] = name
			counts[lang]++
		}
	}
	coverage := map[string]float64{}
	for lang, n := range counts {
		coverage[lang] = float64(n) / float64(totalSpecies)
	}
	return byLang, coverage
}

// familyInfoOut mirrors FamilyInfo in ../../families.go (this tool lives in
// a separate `main` package so it can't import that type directly).
type familyInfoOut struct {
	SciName string            `json:"sciName"`
	Names   map[string]string `json:"names"`
}

// buildFamilyNames resolves each eBird family (identified by familyCode) to
// its best-matching GBIF family — by majority vote among member species,
// same technique as species names — and returns its scientific name plus
// whatever vernacular names that GBIF family record already carried
// (species/search?rank=FAMILY returns vernacularNames inline, so no extra
// per-family call is needed). Also returns each language's coverage
// (fraction of eBird's families it has a name for), purely informational.
func buildFamilyNames(votes map[string]map[string]int, familyByName map[string]gbifTaxon) (map[string]familyInfoOut, map[string]float64) {
	out := map[string]familyInfoOut{}
	counts := map[string]int{}
	for familyCode, gbifNames := range votes {
		gbifName := pickCanonical(gbifNames)
		fam, ok := familyByName[gbifName]
		if !ok {
			continue
		}
		nameVotes := map[string]map[string]int{}
		for _, vn := range fam.VernacularNames {
			lang, ok := iso6391(vn.Language)
			if !ok || vn.VernacularName == "" {
				continue
			}
			if nameVotes[lang] == nil {
				nameVotes[lang] = map[string]int{}
			}
			nameVotes[lang][vn.VernacularName]++
		}
		names := map[string]string{}
		for lang, candidates := range nameVotes {
			names[lang] = pickCanonical(candidates)
			counts[lang]++
		}
		out[familyCode] = familyInfoOut{SciName: gbifName, Names: names}
	}
	coverage := map[string]float64{}
	totalFamilies := len(votes)
	for lang, n := range counts {
		coverage[lang] = float64(n) / float64(totalFamilies)
	}
	return out, coverage
}

// iso6391ByGBIFCode maps GBIF's vernacular-name language codes (ISO 639-2/3,
// three letters — "eng", "spa", "fra") to the two-letter ISO 639-1 codes
// this app uses everywhere else (validLang, the lang query param, i18n.json
// — see main.go's validLang). Covers every language that showed any
// meaningful GBIF coverage in the coverage study this was built from, plus
// headroom for one to climb back above threshold later; a GBIF language
// with no entry here (a handful of very low-coverage ones, e.g. Navajo,
// Aragonese, which have no ISO 639-1 code at all) is simply dropped —
// harmless, since it was nowhere near minSpeciesCoverage anyway.
var iso6391ByGBIFCode = map[string]string{
	"eng": "en", "fra": "fr", "por": "pt", "spa": "es", "deu": "de",
	"pol": "pl", "zho": "zh", "epo": "eo", "slk": "sk", "ukr": "uk",
	"nld": "nl", "rus": "ru", "jpn": "ja", "tur": "tr", "swe": "sv",
	"ita": "it", "nob": "nb", "lit": "lt", "fin": "fi", "ces": "cs",
	"cat": "ca", "nor": "no", "hrv": "hr", "srp": "sr", "cym": "cy",
	"hun": "hu", "est": "et", "ara": "ar", "lav": "lv", "ind": "id",
	"bul": "bg", "heb": "he", "dan": "da", "fas": "fa", "gle": "ga",
	"slv": "sl", "bre": "br", "afr": "af", "isl": "is", "sme": "se",
	"kor": "ko", "vie": "vi", "ell": "el", "nno": "nn", "kaz": "kk",
	"ron": "ro", "glv": "gv", "eus": "eu", "glg": "gl", "hye": "hy",
	"tha": "th", "fao": "fo", "mkd": "mk", "msa": "ms", "bel": "be",
	"gla": "gd", "ben": "bn", "khm": "km", "aze": "az", "tam": "ta",
	"nep": "ne", "ltz": "lb", "mar": "mr", "mon": "mn", "chv": "cv",
	"sqi": "sq", "kat": "ka", "cor": "kw", "tsn": "tn", "roh": "rm",
	"mlg": "mg", "fry": "fy", "mlt": "mt", "grn": "gn", "mal": "ml",
	"hat": "ht", "bak": "ba", "swa": "sw", "mri": "mi", "mya": "my",
	"kan": "kn", "hin": "hi", "oji": "oj", "kur": "ku", "zul": "zu",
	"kal": "kl", "guj": "gu", "ton": "to", "que": "qu", "asm": "as",
	"sin": "si",
}

func iso6391(gbifCode string) (string, bool) {
	code, ok := iso6391ByGBIFCode[gbifCode]
	return code, ok
}

// pickCanonical picks one name out of several candidate strings and their
// vote counts. GBIF's contributing checklists don't agree on capitalization
// or accenting for the same name (e.g. "Ánade azulón", "ánade azulón", and
// "Anade Azulón" for Anas platyrhynchos), so counting raw strings splits one
// name's votes across several spellings and can let a less-supported but
// consistently-spelled rival win outright — that's exactly how "Pato de
// collar" (a Latin-American name, 4 identical votes) used to beat "Ánade
// azulón" (the standard name, 6 votes split three ways). Votes are first
// pooled by a case/diacritic-folded key to find the winning name; the
// most-voted exact spelling within that group is then returned, so the
// output stays a real observed string rather than a synthesized casing.
func pickCanonical(votes map[string]int) string {
	pooled := map[string]int{}
	for name, count := range votes {
		pooled[foldForVoting(name)] += count
	}
	bestKey := ""
	bestKeyCount := -1
	for key, count := range pooled {
		if bestKey == "" || count > bestKeyCount || (count == bestKeyCount && isBetterTiebreak(key, bestKey)) {
			bestKey, bestKeyCount = key, count
		}
	}

	best := ""
	bestCount := -1
	for name, count := range votes {
		if foldForVoting(name) != bestKey {
			continue
		}
		if best == "" || count > bestCount || (count == bestCount && isBetterTiebreak(name, best)) {
			best, bestCount = name, count
		}
	}
	return best
}

func isBetterTiebreak(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// foldDiacritics strips combining marks after Unicode NFD decomposition,
// e.g. "á" (a + combining acute) -> "a".
var foldDiacritics = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// foldForVoting normalizes a name for vote-pooling: case-folded and
// diacritic-stripped, so spelling variants of the same name collapse into
// one vote bucket. Not used for the returned name itself — see pickCanonical.
func foldForVoting(name string) string {
	folded, _, err := transform.String(foldDiacritics, name)
	if err != nil {
		folded = name
	}
	return strings.ToLower(folded)
}

func printCoverage(coverage map[string]float64) {
	type row struct {
		lang string
		pct  float64
	}
	rows := make([]row, 0, len(coverage))
	for lang, pct := range coverage {
		rows = append(rows, row{lang, pct})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].pct > rows[j].pct })
	for _, r := range rows {
		fmt.Fprintf(os.Stderr, "  %-6s %5.1f%%\n", r.lang, r.pct*100)
	}
}

// ---- eBird: taxonomic structure only (see the note on Taxon in ../../taxonomy_data.go) ----

// ebirdLocale is arbitrary — fetchEbirdStructure only keeps fields that
// don't vary by locale.
const ebirdLocale = "en"

// ebirdTaxonEntry is eBird's raw /ref/taxonomy/ebird row shape, trimmed to
// the fields this tool needs (eBird returns more: comName, familyComName,
// bandingCodes, comNameCodes, sciNameCodes, familySciName — dropped here,
// deliberately, for comName/familyComName; the rest just aren't used).
type ebirdTaxonEntry struct {
	SciName     string  `json:"sciName"`
	SpeciesCode string  `json:"speciesCode"`
	Category    string  `json:"category"`
	Order       string  `json:"order"`
	FamilyCode  string  `json:"familyCode"`
	TaxonOrder  float64 `json:"taxonOrder"`
}

// coreTaxonOut is the on-disk shape of data/taxonomy_core.json.gz. Field
// names and JSON tags must stay in lockstep with coreTaxon in
// ../../taxonomy_data.go, which is what decodes this at server startup.
type coreTaxonOut struct {
	SciName     string  `json:"sciName"`
	SpeciesCode string  `json:"speciesCode"`
	Order       string  `json:"order"`
	FamilyCode  string  `json:"familyCode"`
	TaxonOrder  float64 `json:"taxonOrder"`
}

// fetchEbirdStructure fetches eBird's complete world taxonomy (no
// pagination — one call returns every species) and keeps only true species
// — eBird's taxonomy also lists subspecies ("issf"), hybrids, slashes
// (ambiguous ID between two species), spuhs (genus-level "sp."), forms,
// intergrades and domestics, none of which GBIF's `canonicalName` (a clean
// binomial) will ever resolve to.
func fetchEbirdStructure(apiKey string) ([]ebirdTaxonEntry, error) {
	q := url.Values{"fmt": {"json"}, "locale": {ebirdLocale}}
	req, err := http.NewRequest(http.MethodGet, "https://api.ebird.org/v2/ref/taxonomy/ebird?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-eBirdApiToken", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("eBird taxonomy request failed: %d: %s", resp.StatusCode, body)
	}

	var entries []ebirdTaxonEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	out := entries[:0]
	for _, e := range entries {
		if e.Category == "species" {
			out = append(out, e)
		}
	}
	return out, nil
}

// ---- GBIF: vernacular (common) names, species- and family-rank ----

const (
	gbifSearchURL       = "https://api.gbif.org/v1/species/search"
	gbifBackboneDataset = "d7dddbf4-2cf0-4f39-9b2a-bb099caae36c" // GBIF Backbone Taxonomy
	// gbifAvesKey is Aves' key (nubKey) in the backbone — resolved via
	// https://api.gbif.org/v1/species/search?q=Aves&rank=CLASS.
	gbifAvesKey = 212
)

type gbifVernacularName struct {
	VernacularName string `json:"vernacularName"`
	Language       string `json:"language"`
}

// gbifTaxon is trimmed to the fields this tool needs from GBIF's species
// search response — both species- and family-rank results share this
// shape, and both come back with vernacularNames already embedded, so no
// separate per-taxon vernacular-names call is needed for either.
type gbifTaxon struct {
	Key             int                  `json:"key"`
	CanonicalName   string               `json:"canonicalName"`
	VernacularNames []gbifVernacularName `json:"vernacularNames"`
}

type gbifSearchResponse struct {
	EndOfRecords bool        `json:"endOfRecords"`
	Results      []gbifTaxon `json:"results"`
}

// fetchGBIFFamilies returns every accepted bird family in GBIF's backbone.
// Only ~488 families exist, so this paginates shallowly (well within the
// offset range that stays fast — see fetchGBIFSpeciesForFamily).
func fetchGBIFFamilies() ([]gbifTaxon, error) {
	return gbifSearchAll(url.Values{
		"datasetKey":     {gbifBackboneDataset},
		"highertaxonKey": {strconv.Itoa(gbifAvesKey)},
		"rank":           {"FAMILY"},
		"status":         {"ACCEPTED"},
	})
}

// fetchGBIFSpeciesForFamily returns every accepted species in familyKey,
// with their vernacular names. Paginating species this way — per family,
// starting each query's offset back at 0 — is what keeps this fast: a
// single global-offset walk across all ~14,600 bird species stalls badly
// past ~10K results (verified empirically: a plain curl took 20+ seconds
// for 80KB and still hadn't finished one page), because GBIF's search
// backend's offset pagination degrades hard at depth. A family rarely has
// more than a few hundred species, so its own offsets never get deep
// enough to hit that wall.
func fetchGBIFSpeciesForFamily(familyKey int) ([]gbifTaxon, error) {
	return gbifSearchAll(url.Values{
		"datasetKey":     {gbifBackboneDataset},
		"highertaxonKey": {strconv.Itoa(familyKey)},
		"rank":           {"SPECIES"},
		"status":         {"ACCEPTED"},
	})
}

func gbifSearchAll(baseParams url.Values) ([]gbifTaxon, error) {
	var out []gbifTaxon
	offset := 0
	for {
		params := url.Values{}
		maps.Copy(params, baseParams)
		params.Set("limit", "300")
		params.Set("offset", strconv.Itoa(offset))

		data, err := gbifGet(gbifSearchURL + "?" + params.Encode())
		if err != nil {
			return nil, err
		}
		out = append(out, data.Results...)
		offset += len(data.Results)
		if data.EndOfRecords || len(data.Results) == 0 {
			break
		}
	}
	return out, nil
}

// gbifGet retries transient failures with a short linear backoff. GBIF's
// public API needs no key and has no documented rate limit at the request
// volumes used here, so this is defensive rather than something observed
// to be necessary — unlike the deep-offset stall fetchGBIFSpeciesForFamily's
// doc explains, which no amount of retrying fixes.
func gbifGet(rawURL string) (*gbifSearchResponse, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	var lastErr error
	for attempt := range 5 {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		resp, err := client.Get(rawURL)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, body)
			continue
		}
		var out gbifSearchResponse
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return &out, nil
	}
	return nil, fmt.Errorf("GET %s: %w", rawURL, lastErr)
}
