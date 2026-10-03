package areas

import (
	"path/filepath"
	"testing"

	"github.com/jmelis/pajaros/server/internal/seasonal"
)

func blob(id uint16, month int, n uint32) []byte {
	var m seasonal.Months
	m[month] = n
	return seasonal.Encode([]seasonal.Entry{{ID: id, Months: m}})
}

func sum(r Result) uint64 {
	var t uint64
	for _, e := range r.Entries {
		t += uint64(e.Months.Total())
	}
	return t
}

func TestBuildAndQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	b, err := NewBuilder(path)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by 0.01-degree cell (lat first). Includes negative coordinates.
	pts := []struct {
		lat, lng float64
		n        uint32
	}{
		{-33.411, -70.652, 1},
		{40.4150, -3.6850, 5},
		{40.4155, -3.6849, 7}, // same 0.01 cell as the previous point
		{40.4190, -3.6800, 11},
		{40.9, -3.2, 13},
		{41.3, -3.2, 17},
	}
	var want uint64
	for _, p := range pts {
		want += uint64(p.n)
		if err := b.Add(p.lat, p.lng, blob(3, 5, p.n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Finish(); err != nil {
		t.Fatal(err)
	}
	if b.Totals != [4]uint64{want, want, want, want} {
		t.Fatalf("totals %v want %d", b.Totals, want)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	r, err := s.Circle(40.4153, -3.6849, 0.3)
	if err != nil || sum(r) != 12 || r.Units != 2 {
		t.Fatalf("small circle: %v %+v %v", sum(r), r, err)
	}
	r, _ = s.Circle(40.4153, -3.6849, 1)
	if sum(r) != 23 {
		t.Fatalf("1 km circle: %d", sum(r))
	}
	// A big circle is answered from cells; the only difference from points is
	// centre-in-circle selection, so a generous radius must sum everything near.
	r, _ = s.Circle(40.6, -3.4, 100)
	if r.Level == "points" || sum(r) != 5+7+11+13+17 {
		t.Fatalf("big circle: level %s sum %d", r.Level, sum(r))
	}
	sh := NewShape([][][2]float64{{{-4, 40}, {-3, 40}, {-3, 41}, {-4, 41}}})
	r, _ = s.Shape(sh)
	if sum(r) != 5+7+11+13 {
		t.Fatalf("shape: %d", sum(r))
	}
	if n, _ := s.Points(); n != len(pts) {
		t.Fatalf("points %d", n)
	}
}
