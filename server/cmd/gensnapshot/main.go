// Command gensnapshot builds the embedded, worldwide data snapshots the
// server ships instead of calling eBird/GBIF live (see ARCHITECTURE.md
// for the full story on why). Run it from the server/ directory so its
// relative output paths (data/...) land where the go:embed directives in
// taxonomy_data.go and geoip_data.go expect them.
//
// Subcommands:
//
//	EBIRD_API_KEY=... go run ./cmd/gensnapshot taxonomy <Multiling-IOC-*.xlsx>
//	go run ./cmd/gensnapshot hotspots <gbif-download.zip | gbif.tsv>
//	go run ./cmd/gensnapshot areas
//	go run ./cmd/gensnapshot osm <export.geojsonseq> <out.candidates.jsonl>
//	go run ./cmd/gensnapshot naturalearth <ne_admin_0.geojson> <ne_regions.geojson> <out.candidates.jsonl>
//	go run ./cmd/gensnapshot places <out places.bolt> <candidates.jsonl>...
//	go run ./cmd/gensnapshot geoip <dbip-country-lite.csv.gz | .csv>
//
// taxonomy makes a single eBird call for taxonomic structure (not blocked
// the way per-locale calls at request time would be — see ARCHITECTURE.md —
// but still best run from an unblocked connection) plus many small GBIF
// calls for common names (see taxonomy.go), merged with the downloaded
// Multilingual IOC World Bird List (ioc.go); the rest consume an already
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
// areas regroups data/hotspots_seasonal.bolt into data/seasonal_cells.bolt (areas.go).
//
// osm turns an `osmium export` of one OpenStreetMap extract into candidate
// places (osm.go); naturalearth does the same for country outlines and big
// natural regions (ne.go); places merges all the candidate files into
// places.bolt (places.go).
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
		fmt.Fprintln(os.Stderr, "usage: gensnapshot <taxonomy|hotspots|areas|osm|naturalearth|places|geoip>")
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "taxonomy":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: gensnapshot taxonomy <Multiling-IOC-xlsx>")
			os.Exit(2)
		}
		err = runTaxonomy(os.Args[2])
	case "hotspots":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: gensnapshot hotspots <sorted-gbif-tsv>")
			os.Exit(2)
		}
		err = runHotspots(os.Args[2])
	case "areas":
		err = runAreas()
	case "osm":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: gensnapshot osm <export.geojsonseq> <out.candidates.jsonl>")
			os.Exit(2)
		}
		err = runOSM(os.Args[2], os.Args[3])
	case "naturalearth":
		if len(os.Args) != 5 {
			fmt.Fprintln(os.Stderr, "usage: gensnapshot naturalearth <ne_admin_0.geojson> <ne_regions.geojson> <out.candidates.jsonl>")
			os.Exit(2)
		}
		err = runNaturalEarth(os.Args[2], os.Args[3], os.Args[4])
	case "places":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: gensnapshot places <out places.bolt> <candidates.jsonl>...")
			os.Exit(2)
		}
		err = runPlaces(os.Args[2], os.Args[3:])
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
