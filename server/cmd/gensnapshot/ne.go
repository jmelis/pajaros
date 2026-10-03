package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// naturalClasses are the Natural Earth "geography regions" feature classes
// kept as searchable places (everything but coasts, valleys, capes, etc.).
var naturalClasses = map[string]bool{
	"Desert": true, "Continent": true, "Plateau": true, "Plain": true, "Tundra": true,
	"Wetlands": true, "Basin": true, "Geoarea": true, "Range/mtn": true, "Peninsula": true,
	"Island group": true, "Island": true, "Delta": true, "Lake": true,
}

// neIDBase keeps Natural Earth place ids clear of OSM's (node ids add 1<<35).
const neIDBase = uint64(1) << 40

type geoFeature struct {
	props map[string]any
	rings [][][2]float64
}

func loadGeoJSON(path string) ([]geoFeature, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g struct {
		Features []struct {
			Properties map[string]any `json:"properties"`
			Geometry   struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make([]geoFeature, 0, len(g.Features))
	for _, f := range g.Features {
		var rings [][][2]float64
		switch f.Geometry.Type {
		case "Polygon":
			if err := json.Unmarshal(f.Geometry.Coordinates, &rings); err != nil {
				return nil, err
			}
		case "MultiPolygon":
			var mp [][][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &mp); err != nil {
				return nil, err
			}
			for _, p := range mp {
				rings = append(rings, p...)
			}
		default:
			continue
		}
		out = append(out, geoFeature{props: f.Properties, rings: rings})
	}
	return out, nil
}

func str(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := p[k].(string); ok && s != "" && s != "-99" {
			return s
		}
	}
	return ""
}

func num(p map[string]any, key string) float64 {
	f, _ := p[key].(float64)
	return f
}

// runNaturalEarth writes candidate places from Natural Earth: the 10m
// admin-0 countries (OSM extracts cut country outlines at their edges, so
// countries come from here) and the geography regions OSM only has as label
// points (the Sahara, the Alps, the Amazon basin).
func runNaturalEarth(admin0, regions, out string) error {
	o, err := os.Create(out)
	if err != nil {
		return err
	}
	defer o.Close()
	w := bufio.NewWriter(o)
	enc := json.NewEncoder(w)
	next := neIDBase
	emit := func(c candidate, rings [][][2]float64) error {
		span := bboxSpan(rings)
		tol := math.Min(shapeTolDeg, span/shapeTolDivisor)
		c.Rings = simplifyRings(rings, tol, 3*tol)
		if len(c.Rings) == 0 {
			return nil
		}
		c.Lat, c.Lng, _ = centreRadius(c.Rings)
		next++
		c.ID = next
		return enc.Encode(c)
	}

	fs, err := loadGeoJSON(admin0)
	if err != nil {
		return err
	}
	for _, f := range fs {
		en := str(f.props, "NAME_EN", "NAME")
		cc := str(f.props, "ISO_A2_EH", "ISO_A2")
		if err := emit(candidate{
			Kind: kindCountry, CC: cc, En: en, Pop: int(num(f.props, "POP_EST")),
			Names: uniqueNames(en, str(f.props, "NAME"), str(f.props, "NAME_ES"), str(f.props, "NAME_LOCAL")),
		}, f.rings); err != nil {
			return err
		}
	}
	fs, err = loadGeoJSON(regions)
	if err != nil {
		return err
	}
	for _, f := range fs {
		if !naturalClasses[str(f.props, "FEATURECLA")] {
			continue
		}
		en := str(f.props, "NAME_EN", "NAME")
		if en == "" {
			continue
		}
		if err := emit(candidate{
			Kind: kindNatural, Place: "region", En: en,
			Names: uniqueNames(en, str(f.props, "NAME_ES")),
		}, f.rings); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return o.Close()
}
