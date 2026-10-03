package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/jmelis/pajaros/server/internal/placekey"
)

// Place kinds stored in places.bolt; server/places_data.go mirrors them.
const (
	kindCity    = 0
	kindCountry = 1
	kindRegion  = 2
	kindNatural = 3
)

// Minimum polygon areas (km²) for the features that exist in huge numbers.
const (
	minParkKm2      = 0.3
	minReserveKm2   = 1.0
	minDistrictKm2  = 1.0
	minIslandKm2    = 0.5
	maxAdminLevel   = 10
	shapeTolDeg     = 0.01
	shapeTolDivisor = 80
)

// candidate is one searchable place read from an OSM extract: a named point
// (a city, town or village) or a named polygon (an administrative boundary,
// park, reserve, island, desert). The files `gensnapshot osm` writes hold one
// JSON candidate per line; `gensnapshot places` merges them.
type candidate struct {
	ID    uint64         `json:"id"`
	Kind  int            `json:"kind"`
	Level int            `json:"level,omitempty"` // admin_level of a boundary
	CC    string         `json:"cc,omitempty"`    // ISO 3166-1 alpha-2, countries only
	Names []string       `json:"names"`           // Names[0] is displayed, the rest are search aliases
	Pop   int            `json:"pop,omitempty"`   // the OSM population tag, 0 when absent
	Lat   float64        `json:"lat,omitempty"`   // points only
	Lng   float64        `json:"lng,omitempty"`
	Rings [][][2]float64 `json:"rings,omitempty"` // polygons only, (lng, lat)
	Place string         `json:"place,omitempty"` // sub-type: city, town, village, island, desert, park, reserve...
	En    string         `json:"en,omitempty"`    // the name:en tag
}

// osmID turns osmium's unique id ("n123" node, "a456" area) into a number
// small enough for a JavaScript client to hold exactly (below 2^53).
func osmID(s string) (uint64, bool) {
	if len(s) < 2 {
		return 0, false
	}
	n, err := strconv.ParseUint(s[1:], 10, 64)
	if err != nil {
		return 0, false
	}
	switch s[0] {
	case 'a':
		return n, true
	case 'n':
		return 1<<35 | n, true
	}
	return 0, false
}

func propStr(p map[string]any, key string) string {
	s, _ := p[key].(string)
	return strings.TrimSpace(s)
}

func propPop(p map[string]any) int {
	s := strings.NewReplacer(",", "", " ", "", ".", "").Replace(propStr(p, "population"))
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// displayNames returns the display name followed by search aliases. A name
// written in a non-Latin script is displayed in English when OSM has one.
func displayNames(p map[string]any) []string {
	name, en, es := propStr(p, "name"), propStr(p, "name:en"), propStr(p, "name:es")
	first := name
	if en != "" {
		if _, ok := placekey.Fold(name); !ok {
			first = en
		}
	}
	return uniqueNames(first, name, en, es, propStr(p, "official_name"), propStr(p, "int_name"))
}

// polygonAreaKm2 approximates a polygon's area as the sum of its rings' areas;
// holes add instead of subtract, which is close enough for a size threshold.
func polygonAreaKm2(rings [][][2]float64) float64 {
	var total float64
	for _, r := range rings {
		if len(r) < 4 {
			continue
		}
		midLat := r[0][1]
		total += math.Abs(ringArea(r)) * 111.0 * 111.0 * math.Cos(midLat*math.Pi/180)
	}
	return total
}

// classify decides whether a feature becomes a place, and of what kind.
func classify(p map[string]any, isPoint bool) (kind, level int, ok bool) {
	if propStr(p, "name") == "" {
		return 0, 0, false
	}
	place := propStr(p, "place")
	if isPoint {
		switch place {
		case "city", "town", "village":
			return kindCity, 0, true
		case "island", "archipelago":
			return kindNatural, 0, true
		}
		return 0, 0, false
	}
	if propStr(p, "boundary") == "administrative" {
		lvl, err := strconv.Atoi(propStr(p, "admin_level"))
		if err != nil || lvl < 2 || lvl > maxAdminLevel {
			return 0, 0, false
		}
		switch {
		case lvl == 2:
			return 0, 0, false // country outlines come from Natural Earth
		case lvl <= 7:
			return kindRegion, lvl, true
		default:
			return kindCity, lvl, true
		}
	}
	if b := propStr(p, "boundary"); b == "national_park" || b == "protected_area" {
		return kindNatural, 0, true
	}
	switch propStr(p, "leisure") {
	case "park", "nature_reserve":
		return kindNatural, 0, true
	}
	if propStr(p, "natural") == "desert" || place == "island" || place == "archipelago" {
		return kindNatural, 0, true
	}
	return 0, 0, false
}

// minArea is the smallest polygon kept for a feature of this sort.
func minArea(p map[string]any, kind, level int) float64 {
	switch {
	case kind == kindCity && level >= 9:
		return minDistrictKm2
	case kind == kindNatural && propStr(p, "leisure") == "park" && propStr(p, "boundary") == "":
		return minParkKm2
	case kind == kindNatural && (propStr(p, "place") == "island" || propStr(p, "place") == "archipelago"):
		return minIslandKm2
	case kind == kindNatural && propStr(p, "natural") != "desert":
		return minReserveKm2
	}
	return 0
}

// runOSM converts an `osmium export -f geojsonseq` file (one feature per
// line) into candidate places. Untagged vertices, tiny parks and unnamed
// features are dropped here, so the output is small enough to merge across
// every extract of the planet.
func runOSM(in, out string) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	o, err := os.Create(out)
	if err != nil {
		return err
	}
	defer o.Close()
	w := bufio.NewWriterSize(o, 1<<20)
	enc := json.NewEncoder(w)

	rd := bufio.NewReaderSize(f, 1<<20)
	var kept, seen int
	for {
		line, err := rd.ReadBytes('\n')
		line = bytes.Trim(line, "\x1e \r\n")
		if len(line) > 0 {
			seen++
			c, ok, perr := featureCandidate(line)
			if perr != nil {
				return fmt.Errorf("feature %d: %w", seen, perr)
			}
			if ok {
				if err := enc.Encode(c); err != nil {
					return err
				}
				kept++
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "%d features read, %d candidate places kept\n", seen, kept)
	if err := w.Flush(); err != nil {
		return err
	}
	return o.Close()
}

func featureCandidate(line []byte) (candidate, bool, error) {
	var ft struct {
		ID         string         `json:"id"`
		Properties map[string]any `json:"properties"`
		Geometry   struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	}
	if err := json.Unmarshal(line, &ft); err != nil {
		return candidate{}, false, err
	}
	id, ok := osmID(ft.ID)
	if !ok {
		return candidate{}, false, nil
	}
	p := ft.Properties
	c := candidate{ID: id, Pop: propPop(p), Names: displayNames(p), En: propStr(p, "name:en")}
	switch ft.Geometry.Type {
	case "Point":
		kind, level, ok := classify(p, true)
		if !ok {
			return candidate{}, false, nil
		}
		var xy [2]float64
		if err := json.Unmarshal(ft.Geometry.Coordinates, &xy); err != nil {
			return candidate{}, false, err
		}
		c.Kind, c.Level, c.Lng, c.Lat, c.Place = kind, level, xy[0], xy[1], propStr(p, "place")
		return c, true, nil
	case "Polygon", "MultiPolygon":
		kind, level, ok := classify(p, false)
		if !ok {
			return candidate{}, false, nil
		}
		var rings [][][2]float64
		if ft.Geometry.Type == "Polygon" {
			if err := json.Unmarshal(ft.Geometry.Coordinates, &rings); err != nil {
				return candidate{}, false, err
			}
		} else {
			var mp [][][][2]float64
			if err := json.Unmarshal(ft.Geometry.Coordinates, &mp); err != nil {
				return candidate{}, false, err
			}
			for _, poly := range mp {
				rings = append(rings, poly...)
			}
		}
		area := polygonAreaKm2(rings)
		if area < minArea(p, kind, level) {
			return candidate{}, false, nil
		}
		span := bboxSpan(rings)
		tol := math.Min(shapeTolDeg, span/shapeTolDivisor)
		c.Kind, c.Level, c.Place = kind, level, polygonSub(p)
		c.Rings = simplifyRings(rings, tol, 3*tol)
		c.Lat, c.Lng, _ = centreRadius(c.Rings)
		return c, len(c.Rings) > 0, nil
	}
	return candidate{}, false, nil
}

func bboxSpan(rings [][][2]float64) float64 {
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, r := range rings {
		for _, p := range r {
			minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
			minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
		}
	}
	return math.Max(maxX-minX, maxY-minY)
}

// polygonSub names the sort of a polygon place for ranking.
func polygonSub(p map[string]any) string {
	switch {
	case propStr(p, "natural") == "desert":
		return "desert"
	case propStr(p, "place") == "island" || propStr(p, "place") == "archipelago":
		return propStr(p, "place")
	case propStr(p, "boundary") == "national_park" || propStr(p, "boundary") == "protected_area" || propStr(p, "leisure") == "nature_reserve":
		return "reserve"
	case propStr(p, "leisure") == "park":
		return "park"
	}
	return ""
}
