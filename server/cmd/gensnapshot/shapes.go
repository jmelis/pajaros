package main

import (
	"math"
	"strings"

	"github.com/jmelis/pajaros/server/internal/areas"
)

type countryInfo struct {
	cc, name string
	shape    *areas.Shape
}

// uniqueNames drops empty and repeated (case-insensitive) names, keeping order.
func uniqueNames(names ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		k := strings.ToLower(strings.TrimSpace(n))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	return out
}

// containingCountry returns the country when every sampled vertex of the
// shape lies in that one country; a shape spanning several (the Sahara) or
// none (an ocean basin) belongs to no country.
func containingCountry(rings [][][2]float64, countries []countryInfo) (string, string) {
	var pts [][2]float64
	total := 0
	for _, r := range rings {
		total += len(r)
	}
	step := total/100 + 1
	n := 0
	for _, r := range rings {
		for _, p := range r {
			if n%step == 0 {
				pts = append(pts, p)
			}
			n++
		}
	}
	// Coastlines differ slightly between the datasets, so a few vertices of
	// an island can fall in the sea: the place is inside a country when that
	// country holds nearly all the sampled vertices.
	// A vertex also counts when a point ~5 km away on any side is inside.
	const near = 0.05
	votes := map[int]int{}
	for _, p := range pts {
		for _, d := range [][2]float64{{0, 0}, {near, 0}, {-near, 0}, {0, near}, {0, -near}} {
			hit := -1
			for i, c := range countries {
				if c.shape.Contains(p[1]+d[1], p[0]+d[0]) {
					hit = i
					break
				}
			}
			if hit >= 0 {
				votes[hit]++
				break
			}
		}
	}
	for i, v := range votes {
		if float64(v) >= 0.85*float64(len(pts)) {
			return countries[i].cc, countries[i].name
		}
	}
	return "", ""
}

// centreRadius returns a representative centre (the centroid of the largest
// ring) and the distance in km to the farthest vertex.
func centreRadius(rings [][][2]float64) (lat, lng, rKm float64) {
	best, bestArea := -1, -1.0
	for i, r := range rings {
		if a := math.Abs(ringArea(r)); a > bestArea {
			best, bestArea = i, a
		}
	}
	if best < 0 {
		return 0, 0, 0
	}
	r := rings[best]
	var cx, cy, a2 float64
	for i, j := 0, len(r)-1; i < len(r); j, i = i, i+1 {
		cross := r[j][0]*r[i][1] - r[i][0]*r[j][1]
		a2 += cross
		cx += (r[j][0] + r[i][0]) * cross
		cy += (r[j][1] + r[i][1]) * cross
	}
	if a2 == 0 {
		lng, lat = r[0][0], r[0][1]
	} else {
		lng, lat = cx/(3*a2), cy/(3*a2)
	}
	for _, rg := range rings {
		for _, p := range rg {
			rKm = math.Max(rKm, haversineKmGS(lat, lng, p[1], p[0]))
		}
	}
	return lat, lng, rKm
}

func ringArea(r [][2]float64) float64 {
	var a float64
	for i, j := 0, len(r)-1; i < len(r); j, i = i, i+1 {
		a += r[j][0]*r[i][1] - r[i][0]*r[j][1]
	}
	return a / 2
}

func haversineKmGS(lat1, lng1, lat2, lng2 float64) float64 {
	const p = math.Pi / 180
	dla, dlo := (lat2-lat1)*p, (lng2-lng1)*p
	a := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(lat1*p)*math.Cos(lat2*p)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * 6371 * math.Asin(math.Sqrt(a))
}

// simplifyRings thins every ring with Douglas-Peucker at tol degrees and
// drops specks smaller than minSpan degrees, keeping the largest ring
// whatever its size. Cell selection works at 0.05 degrees or coarser, so
// ~1 km of shape error is invisible.
func simplifyRings(rings [][][2]float64, tol, minSpan float64) [][][2]float64 {
	var out [][][2]float64
	var biggest [][2]float64
	bigSpan := -1.0
	for _, r := range rings {
		minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for _, p := range r {
			minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
			minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
		}
		span := math.Max(maxX-minX, maxY-minY)
		s := douglasPeucker(r, tol)
		if len(s) < 4 {
			continue
		}
		if span > bigSpan {
			bigSpan, biggest = span, s
		}
		if span >= minSpan {
			out = append(out, s)
		}
	}
	if len(out) == 0 && biggest != nil {
		out = append(out, biggest)
	}
	return out
}

func douglasPeucker(pts [][2]float64, tol float64) [][2]float64 {
	if len(pts) < 5 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	type seg struct{ a, b int }
	stack := []seg{{0, len(pts) - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		maxD, idx := 0.0, -1
		for i := s.a + 1; i < s.b; i++ {
			if d := segDist(pts[i], pts[s.a], pts[s.b]); d > maxD {
				maxD, idx = d, i
			}
		}
		if idx >= 0 && maxD > tol {
			keep[idx] = true
			stack = append(stack, seg{s.a, idx}, seg{idx, s.b})
		}
	}
	out := make([][2]float64, 0, len(pts)/4)
	for i, p := range pts {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

func segDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	t := math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/(dx*dx+dy*dy)))
	return math.Hypot(p[0]-(a[0]+t*dx), p[1]-(a[1]+t*dy))
}

// encodeShape is place_shapes' value format: uvarint ring count, then per
// ring a uvarint vertex count and float32 (lng, lat) pairs. Must match
// server/places_data.go's decodeShape.
func encodeShape(rings [][][2]float64) []byte {
	buf := appendUvarint(nil, uint64(len(rings)))
	for _, r := range rings {
		buf = appendUvarint(buf, uint64(len(r)))
		for _, p := range r {
			buf = appendFloat32(buf, float32(p[0]))
			buf = appendFloat32(buf, float32(p[1]))
		}
	}
	return buf
}

func appendFloat32(buf []byte, f float32) []byte {
	b := math.Float32bits(f)
	return append(buf, byte(b), byte(b>>8), byte(b>>16), byte(b>>24))
}
