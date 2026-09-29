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
//	go run ./cmd/gensnapshot geoip <dbip-country-lite.csv.gz | .csv>
//
// taxonomy makes a single eBird call for taxonomic structure (not blocked
// the way per-locale calls at request time would be — see ARCHITECTURE.md —
// but still best run from an unblocked connection) plus many small GBIF
// calls for common names (see taxonomy.go); the rest consume an already
// downloaded bulk file and never call out at all.
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
//
// geoip consumes db-ip.com's free IP-to-country database (see
// geoip.go/server/geoip_data.go) straight from its downloaded .csv.gz.
package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gensnapshot <taxonomy|hotspots|places|geoip>")
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
	case "geoip":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: gensnapshot geoip <dbip-country-lite-dump>")
			os.Exit(2)
		}
		err = runGeoIP(os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gensnapshot:", err)
		os.Exit(1)
	}
}

// writeGzippedJSON is shared by every subcommand that writes a small,
// git-committed embedded snapshot (taxonomy, geoip) — hotspots/places are
// too big to embed and use their own bbolt-based writers instead.
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
