package main

import (
	"math"
	"net/http"
)

// outlineTolDivisor sets how coarse a displayed outline is: vertices closer
// than 1/outlineTolDivisor of the area's longest side to the simplified line
// are dropped, which is below a pixel on a map a few hundred pixels wide.
const outlineTolDivisor = 400

// Outline returns the rings of the area's polygon as [lat, lng] pairs,
// simplified for drawing. It reports errUnknownArea for an area without a
// polygon (a custom circle, a town).
func (r *AreaResolver) Outline(key string) ([][][2]float64, error) {
	ref, ok := parseAreaKey(key)
	if !ok || ref.custom {
		return nil, errUnknownArea
	}
	rings, ok := r.places.Rings(ref.placeID)
	if !ok {
		return nil, errUnknownArea
	}
	minLat, maxLat, minLng, maxLng := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, ring := range rings {
		for _, p := range ring {
			minLng, maxLng = math.Min(minLng, p[0]), math.Max(maxLng, p[0])
			minLat, maxLat = math.Min(minLat, p[1]), math.Max(maxLat, p[1])
		}
	}
	tol := math.Max(maxLat-minLat, maxLng-minLng) / outlineTolDivisor
	out := make([][][2]float64, 0, len(rings))
	for _, ring := range rings {
		s := simplifyRing(ring, tol)
		if len(s) < 4 {
			continue
		}
		pts := make([][2]float64, len(s))
		for i, p := range s {
			pts[i] = [2]float64{round(p[1], 4), round(p[0], 4)}
		}
		out = append(out, pts)
	}
	if len(out) == 0 {
		return nil, errUnknownArea
	}
	return out, nil
}

// simplifyRing is Douglas-Peucker over a closed ring of (lng, lat) points; the
// first and last vertex are always kept.
func simplifyRing(ring [][2]float64, tol float64) [][2]float64 {
	n := len(ring)
	if n <= 4 {
		return ring
	}
	keep := make([]bool, n)
	keep[0], keep[n-1] = true, true
	type span struct{ lo, hi int }
	stack := []span{{0, n - 1}}
	for len(stack) > 0 {
		sp := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		far, farD := -1, tol
		for i := sp.lo + 1; i < sp.hi; i++ {
			if d := segmentDist(ring[i], ring[sp.lo], ring[sp.hi]); d > farD {
				far, farD = i, d
			}
		}
		if far >= 0 {
			keep[far] = true
			stack = append(stack, span{sp.lo, far}, span{far, sp.hi})
		}
	}
	out := make([][2]float64, 0, n/8)
	for i, p := range ring {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

func segmentDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	t := math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l2))
	return math.Hypot(p[0]-(a[0]+t*dx), p[1]-(a[1]+t*dy))
}

func (s *Server) handleAreaOutline(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !validAreaKey(key) {
		http.Error(w, "invalid area key", http.StatusBadRequest)
		return
	}
	rings, err := s.areas.Outline(key)
	if err != nil {
		http.Error(w, "no outline", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	writeJSON(w, map[string]any{"rings": rings})
}
