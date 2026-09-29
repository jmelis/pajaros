package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// spanishCountries and frenchCountries are the ISO 3166-1 alpha-2 codes of
// every country/territory where Spanish/French is an official (or, for a
// handful of Francophonie members where it's the primary language of
// administration and education without formal "official" status, e.g.
// Mauritius/Mauritania, a clearly primary) language — what a new account's
// default UI language is based on (see geoip_data.go on the server side).
// Deliberately inclusive of countries where it's one of several official
// languages (Belgium, Switzerland, Canada, Haiti, Rwanda...): geolocation
// only gives a country, not a region, so there's no way to tell which
// language-group a given visitor from one of these is actually in, and
// defaulting to the country's other official language(s) would be no less
// of a guess than this is.
var spanishCountries = map[string]bool{
	"AR": true, "BO": true, "CL": true, "CO": true, "CR": true, "CU": true,
	"DO": true, "EC": true, "SV": true, "GQ": true, "GT": true, "HN": true,
	"MX": true, "NI": true, "PA": true, "PY": true, "PE": true, "ES": true,
	"UY": true, "VE": true, "PR": true,
}

var frenchCountries = map[string]bool{
	"FR": true, "BE": true, "CH": true, "MC": true, "LU": true,
	"SN": true, "ML": true, "BF": true, "NE": true, "TG": true, "BJ": true,
	"GN": true, "CI": true, "CD": true, "CG": true, "CM": true, "GA": true,
	"TD": true, "CF": true, "MG": true, "DJ": true, "KM": true, "HT": true,
	"RW": true, "BI": true, "VU": true, "SC": true, "MU": true, "MR": true,
	"GP": true, "MQ": true, "GF": true, "RE": true, "YT": true, "PF": true,
	"NC": true, "WF": true, "BL": true, "MF": true, "PM": true,
}

// geoIPRangeOut is the on-disk shape of data/geoip_country.json.gz — an IP
// range (dotted or colon form, whichever net.ParseIP on the server side
// accepts) and which of the app's non-English UI languages its country
// speaks. See server/geoip_data.go, which embeds and decodes this.
type geoIPRangeOut struct {
	Start string `json:"s"`
	End   string `json:"e"`
	Lang  string `json:"l"`
}

// runGeoIP builds data/geoip_country.json.gz from db-ip.com's free "IP to
// Country Lite" database (CC-BY 4.0, monthly releases at
// db-ip.com/db/download/ip-to-country-lite): start_ip,end_ip,country_code
// rows, already sorted and covering the entire IPv4 and IPv6 address space
// with no gaps or overlaps. Only rows whose country is in
// spanishCountries/frenchCountries are kept — every other country (which is
// most of them) needs no entry, since the server's lookup treats "not
// found" as English.
func runGeoIP(path string) error {
	f, err := openMaybeGzip(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var out []geoIPRangeOut
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			return fmt.Errorf("geoip: malformed row %q", line)
		}
		start, end, cc := parts[0], parts[1], parts[2]

		var lang string
		switch {
		case spanishCountries[cc]:
			lang = "es"
		case frenchCountries[cc]:
			lang = "fr"
		default:
			continue
		}
		out = append(out, geoIPRangeOut{Start: start, End: end, Lang: lang})
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("geoip: read %s: %w", path, err)
	}

	// Input is already sorted by start; this just guards against a future
	// db-ip format change breaking that assumption silently.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })

	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}
	const outPath = "data/geoip_country.json.gz"
	if err := writeGzippedJSON(outPath, out); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d ranges)\n", outPath, len(out))
	return nil
}

// openMaybeGzip opens path, transparently decompressing it if it ends in
// .gz — db-ip ships its download gzipped, but a caller who already
// gunzipped it should work too.
func openMaybeGzip(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &gzipFileReader{gz: gz, f: f}, nil
}

// gzipFileReader closes both the decompressing reader and the underlying
// file together, so a single Close() fully releases path.
type gzipFileReader struct {
	gz *gzip.Reader
	f  *os.File
}

func (g *gzipFileReader) Read(p []byte) (int, error) { return g.gz.Read(p) }

func (g *gzipFileReader) Close() error {
	err := g.gz.Close()
	if ferr := g.f.Close(); err == nil {
		err = ferr
	}
	return err
}
