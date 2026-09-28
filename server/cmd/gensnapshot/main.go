// Command gensnapshot builds the embedded, worldwide data snapshots the
// server ships instead of calling eBird/GBIF live (see ARCHITECTURE.md
// for the full story on why). Run it from the server/ directory so its
// relative output paths (data/...) land where the go:embed directives in
// taxonomy_data.go and hotspots_data.go expect them.
//
// Subcommands:
//
//	EBIRD_API_KEY=... go run ./cmd/gensnapshot taxonomy
//	go run ./cmd/gensnapshot hotspots <gbif-download.zip | gbif.tsv>
//	go run ./cmd/gensnapshot places <geonames-cities500.zip | cities500.txt>
//
// eBird blocks requests from this project's cloud deployment, so taxonomy
// must be run from an unblocked connection (a home network works) and the
// result committed — not fetched at request time.
//
// hotspots consumes the aggregated file from GBIF's SQL Downloads API (see
// ARCHITECTURE.md) directly from its downloaded .zip — columns
// decimallatitude, decimallongitude, locality, species, n — no unzipping or
// pre-sorting needed; it decompresses the zip's single entry on the fly.
// It reads the data twice (see hotspots.go) instead of sorting it once,
// which keeps memory bounded without the cost of an external sort of a
// 20GB+ file, and never writes the ~20GB unzipped CSV to disk at all.
//
// places consumes a GeoNames gazetteer dump (see places.go/ARCHITECTURE.md)
// straight from its downloaded .zip, the same way hotspots does.
package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

// taxonLangs mirrors validLang in main.go — the set of bird-name languages
// the frontend offers.
var taxonLangs = []string{"en", "es", "fr"}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gensnapshot <taxonomy|hotspots|places>")
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "taxonomy":
		err = runTaxonomy()
	case "hotspots":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: gensnapshot hotspots <sorted-gbif-tsv>")
			os.Exit(2)
		}
		err = runHotspots(os.Args[2])
	case "places":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: gensnapshot places <geonames-dump>")
			os.Exit(2)
		}
		err = runPlaces(os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gensnapshot:", err)
		os.Exit(1)
	}
}

// ebirdTaxonEntry is eBird's raw /ref/taxonomy/ebird row shape, trimmed to
// the fields this tool needs (eBird returns more: bandingCodes,
// comNameCodes, sciNameCodes, familySciName — dropped here to keep the
// embedded snapshot lean).
type ebirdTaxonEntry struct {
	SciName       string  `json:"sciName"`
	ComName       string  `json:"comName"`
	SpeciesCode   string  `json:"speciesCode"`
	Category      string  `json:"category"`
	Order         string  `json:"order"`
	FamilyCode    string  `json:"familyCode"`
	FamilyComName string  `json:"familyComName"`
	TaxonOrder    float64 `json:"taxonOrder"`
}

// taxonOut is the on-disk shape for each embedded data/taxonomy_<lang>.json.gz
// file. Field names and JSON tags must stay in lockstep with the Taxon type
// in ebird.go, which is what taxonomy_data.go decodes this into at server
// startup — this tool lives in a separate `main` package (cmd/gensnapshot)
// so it can't just import that type directly.
type taxonOut struct {
	SciName       string  `json:"sciName"`
	ComName       string  `json:"comName"`
	SpeciesCode   string  `json:"speciesCode"`
	Order         string  `json:"order"`
	FamilyCode    string  `json:"familyCode"`
	FamilyComName string  `json:"familyComName"`
	TaxonOrder    float64 `json:"taxonOrder"`
}

// runTaxonomy fetches eBird's complete world taxonomy once per language (no
// pagination — one call returns every species) and writes each as a
// gzipped JSON file under data/.
func runTaxonomy() error {
	apiKey := os.Getenv("EBIRD_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("EBIRD_API_KEY not set")
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}

	for _, lang := range taxonLangs {
		fmt.Fprintf(os.Stderr, "fetching %s taxonomy...\n", lang)
		entries, err := fetchTaxonomy(apiKey, lang)
		if err != nil {
			return fmt.Errorf("fetch %s taxonomy: %w", lang, err)
		}

		out := make([]taxonOut, 0, len(entries))
		for _, e := range entries {
			// Only true species — eBird's taxonomy also lists subspecies
			// ("issf"), hybrids, slashes (ambiguous ID between two
			// species), spuhs (genus-level "sp."), forms, intergrades and
			// domestics, none of which GBIF's `species` column (a clean
			// binomial scientific name) will ever resolve to.
			if e.Category != "species" {
				continue
			}
			out = append(out, taxonOut{
				SciName:       e.SciName,
				ComName:       e.ComName,
				SpeciesCode:   e.SpeciesCode,
				Order:         e.Order,
				FamilyCode:    e.FamilyCode,
				FamilyComName: e.FamilyComName,
				TaxonOrder:    e.TaxonOrder,
			})
		}

		path := fmt.Sprintf("data/taxonomy_%s.json.gz", lang)
		if err := writeGzippedJSON(path, out); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d species)\n", path, len(out))
	}
	return nil
}

func fetchTaxonomy(apiKey, lang string) ([]ebirdTaxonEntry, error) {
	q := url.Values{"fmt": {"json"}, "locale": {lang}}
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
	return entries, nil
}

func writeGzippedJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	if err := json.NewEncoder(gz).Encode(v); err != nil {
		return err
	}
	return gz.Close()
}
