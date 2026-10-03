package areas

import (
	"math"
	"sort"

	"github.com/jmelis/pajaros/server/internal/seasonal"
)

func sortByID(es []seasonal.Entry) {
	sort.Slice(es, func(i, j int) bool { return es[i].ID < es[j].ID })
}

// Shape is a (multi)polygon: every ring, outer or hole, tested with the
// even-odd rule. Rings are lists of {lng, lat}.
type Shape struct {
	Rings                          [][][2]float64
	LatMin, LatMax, LngMin, LngMax float64
}

// NewShape builds a Shape and its bounding box from rings.
func NewShape(rings [][][2]float64) *Shape {
	s := &Shape{Rings: rings, LatMin: math.Inf(1), LatMax: math.Inf(-1), LngMin: math.Inf(1), LngMax: math.Inf(-1)}
	for _, r := range rings {
		for _, p := range r {
			s.LngMin, s.LngMax = math.Min(s.LngMin, p[0]), math.Max(s.LngMax, p[0])
			s.LatMin, s.LatMax = math.Min(s.LatMin, p[1]), math.Max(s.LatMax, p[1])
		}
	}
	return s
}

// Contains reports whether (lat, lng) is inside the shape.
func (s *Shape) Contains(lat, lng float64) bool {
	if lat < s.LatMin || lat > s.LatMax || lng < s.LngMin || lng > s.LngMax {
		return false
	}
	in := false
	for _, r := range s.Rings {
		for i, j := 0, len(r)-1; i < len(r); j, i = i, i+1 {
			xi, yi, xj, yj := r[i][0], r[i][1], r[j][0], r[j][1]
			if (yi > lat) != (yj > lat) && lng < (xj-xi)*(lat-yi)/(yj-yi)+xi {
				in = !in
			}
		}
	}
	return in
}
