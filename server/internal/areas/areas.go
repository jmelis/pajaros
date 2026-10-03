// Package areas stores per-place seasonal species counts at several spatial
// resolutions and answers "sum everything inside this circle / polygon".
//
// Counts are additive, so one file holds the raw points (for small places) and
// pre-summed grid cells at 0.05, 0.25 and 1 degree (for larger ones); a query
// picks the level that keeps the number of lookups bounded.
//
// All indices derive from one integer, the 0.01-degree index of a coordinate
// (floor(deg/0.01)); coarser cells are floor-divisions of it, so a point can
// never land in different cells at different levels because of float rounding.
package areas

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/jmelis/pajaros/server/internal/seasonal"

	bolt "go.etcd.io/bbolt"
)

// FileName is the bolt file this package reads and writes.
const FileName = "seasonal_cells.bolt"

const (
	bucketPoints = "points_by_cell"
	bucketMeta   = "areas_meta"
	metaFormat   = "species_format"
	metaPoints   = "points"
	// FormatValue labels the species blob encoding stored in the file.
	FormatValue = "seasonal-bits-1"
	pointDeg    = 0.01
	keyOffset   = 1 << 20
	// MaxPointRadiusKm is the largest circle answered from raw points.
	MaxPointRadiusKm = 5.0
	// maxCells bounds the cell lookups one query may make.
	maxCells = 3000
	// maxPointCells bounds the 0.01-degree cells a shape may span and still
	// be answered from raw points.
	maxPointCells = 2500
)

// levels: step in units of 0.01 degrees.
var levelSteps = [3]int32{5, 25, 100}

var levelBucket = [3]string{"cells_0", "cells_1", "cells_2"}

// LevelDeg returns the cell size in degrees of level 0..2.
func LevelDeg(level int) float64 { return float64(levelSteps[level]) * pointDeg }

func floorDiv(a, b int32) int32 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// idx is the 0.01-degree index of a coordinate.
func idx(deg float64) int32 { return int32(math.Floor(deg / pointDeg)) }

func cellKey(i, j int32) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint32(k[0:4], uint32(i+keyOffset))
	binary.BigEndian.PutUint32(k[4:8], uint32(j+keyOffset))
	return k
}

// Result is the pooled species counts of one query.
type Result struct {
	Entries []seasonal.Entry
	Units   int    // points or cells read
	Level   string // "points", "0.05", "0.25" or "1"
}

// Store reads an seasonal_cells.bolt file.
type Store struct{ db *bolt.DB }

// Open opens path read-only.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	var got string
	db.View(func(tx *bolt.Tx) error {
		if m := tx.Bucket([]byte(bucketMeta)); m != nil {
			got = string(m.Get([]byte(metaFormat)))
		}
		return nil
	})
	if got != FormatValue {
		db.Close()
		return nil, fmt.Errorf("areas: %s species format is %q, this server reads %q: rebuild the file", path, got, FormatValue)
	}
	return &Store{db: db}, nil
}

// Close releases the file.
func (s *Store) Close() error { return s.db.Close() }

// Points returns the number of raw points stored.
func (s *Store) Points() (int, error) {
	var n int
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(bucketMeta)).Get([]byte(metaPoints))
		if len(v) != 8 {
			return errors.New("areas: missing point count")
		}
		n = int(binary.BigEndian.Uint64(v))
		return nil
	})
	return n, err
}

// ForEachCell visits every stored cell of level 0..2 with its pooled entries.
// i and j are the cell indices (the cell spans i*LevelDeg to (i+1)*LevelDeg
// degrees of latitude, j likewise of longitude); entries are only valid
// during the call.
func (s *Store) ForEachCell(level int, fn func(i, j int32, es []seasonal.Entry)) error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(levelBucket[level]))
		if b == nil {
			return fmt.Errorf("areas: bucket %s missing", levelBucket[level])
		}
		return b.ForEach(func(k, v []byte) error {
			if len(k) != 8 {
				return nil
			}
			es, err := seasonal.Decode(v)
			if err != nil {
				return err
			}
			i := int32(binary.BigEndian.Uint32(k[0:4])) - keyOffset
			j := int32(binary.BigEndian.Uint32(k[4:8])) - keyOffset
			fn(i, j, es)
			return nil
		})
	})
}

type accumulator map[uint16]*seasonal.Months

func (a accumulator) add(es []seasonal.Entry) {
	for _, e := range es {
		m := a[e.ID]
		if m == nil {
			m = &seasonal.Months{}
			a[e.ID] = m
		}
		for k := range e.Months {
			m[k] += e.Months[k]
		}
	}
}

func (a accumulator) entries() []seasonal.Entry {
	out := make([]seasonal.Entry, 0, len(a))
	for id, m := range a {
		out = append(out, seasonal.Entry{ID: id, Months: *m})
	}
	sortByID(out)
	return out
}

// Circle sums everything within rKm of (lat, lng): raw points up to
// MaxPointRadiusKm, otherwise cells of the coarsest level that keeps the
// lookup count bounded.
func (s *Store) Circle(lat, lng, rKm float64) (Result, error) {
	dLat := rKm / 111.0
	dLng := rKm / (111.0 * math.Max(0.01, math.Cos(lat*math.Pi/180)))
	inside := func(cLat, cLng float64) bool { return haversineKm(lat, lng, cLat, cLng) <= rKm }
	if rKm <= MaxPointRadiusKm {
		return s.points(lat-dLat, lat+dLat, lng-dLng, lng+dLng, inside)
	}
	level := 2
	for l := range 3 {
		if cellsInBox(lat-dLat, lat+dLat, lng-dLng, lng+dLng, l) <= maxCells {
			level = l
			break
		}
	}
	return s.cells(level, lat-dLat, lat+dLat, lng-dLng, lng+dLng, inside)
}

// Shape sums everything inside sh. A small shape is answered from raw points
// (each tested against the polygon); a larger one from the cells of the
// finest level that keeps the lookup count bounded, taking those whose
// centre lies inside.
func (s *Store) Shape(sh *Shape) (Result, error) {
	inside := func(la, lo float64) bool { return sh.Contains(la, lo) }
	nPoints := (idx(sh.LatMax) - idx(sh.LatMin) + 1) * (idx(sh.LngMax) - idx(sh.LngMin) + 1)
	if nPoints <= maxPointCells {
		return s.points(sh.LatMin, sh.LatMax, sh.LngMin, sh.LngMax, inside)
	}
	level := 2
	for l := range 3 {
		if cellsInBox(sh.LatMin, sh.LatMax, sh.LngMin, sh.LngMax, l) <= maxCells {
			level = l
			break
		}
	}
	return s.cells(level, sh.LatMin, sh.LatMax, sh.LngMin, sh.LngMax, inside)
}

func cellsInBox(latMin, latMax, lngMin, lngMax float64, level int) int {
	st := levelSteps[level]
	ni := floorDiv(idx(latMax), st) - floorDiv(idx(latMin), st) + 1
	nj := floorDiv(idx(lngMax), st) - floorDiv(idx(lngMin), st) + 1
	return int(ni) * int(nj)
}

func (s *Store) cells(level int, latMin, latMax, lngMin, lngMax float64, inside func(lat, lng float64) bool) (Result, error) {
	st := levelSteps[level]
	res := Result{Level: fmt.Sprint(LevelDeg(level))}
	agg := accumulator{}
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(levelBucket[level]))
		if b == nil {
			return fmt.Errorf("areas: bucket %s missing", levelBucket[level])
		}
		for i := floorDiv(idx(latMin), st); i <= floorDiv(idx(latMax), st); i++ {
			for j := floorDiv(idx(lngMin), st); j <= floorDiv(idx(lngMax), st); j++ {
				cLat := (float64(i) + 0.5) * LevelDeg(level)
				cLng := (float64(j) + 0.5) * LevelDeg(level)
				if !inside(cLat, cLng) {
					continue
				}
				v := b.Get(cellKey(i, j))
				if v == nil {
					continue
				}
				es, err := seasonal.Decode(v)
				if err != nil {
					return err
				}
				agg.add(es)
				res.Units++
			}
		}
		return nil
	})
	res.Entries = agg.entries()
	return res, err
}

func (s *Store) points(latMin, latMax, lngMin, lngMax float64, inside func(lat, lng float64) bool) (Result, error) {
	res := Result{Level: "points"}
	agg := accumulator{}
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketPoints))
		if b == nil {
			return errors.New("areas: points bucket missing")
		}
		for i := idx(latMin); i <= idx(latMax); i++ {
			for j := idx(lngMin); j <= idx(lngMax); j++ {
				v := b.Get(cellKey(i, j))
				if v == nil {
					continue
				}
				if err := forEachPoint(i, j, v, func(lat, lng float64, blob []byte) error {
					if !inside(lat, lng) {
						return nil
					}
					es, err := seasonal.Decode(blob)
					if err != nil {
						return err
					}
					agg.add(es)
					res.Units++
					return nil
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	res.Entries = agg.entries()
	return res, err
}

// A point cell value is a run of {lat offset u16, lng offset u16, blob length
// uvarint, species blob}; offsets position the point inside its 0.01-degree
// cell, about 15 cm per step.
const offsetMax = 65535

func appendPoint(dst []byte, lat, lng float64, i, j int32, blob []byte) []byte {
	lo := func(deg float64, cell int32) uint16 {
		f := (deg - float64(cell)*pointDeg) / pointDeg * offsetMax
		return uint16(math.Max(0, math.Min(offsetMax, math.Round(f))))
	}
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], lo(lat, i))
	dst = append(dst, b[:]...)
	binary.BigEndian.PutUint16(b[:], lo(lng, j))
	dst = append(dst, b[:]...)
	dst = binary.AppendUvarint(dst, uint64(len(blob)))
	return append(dst, blob...)
}

func forEachPoint(i, j int32, v []byte, fn func(lat, lng float64, blob []byte) error) error {
	for len(v) > 0 {
		if len(v) < 5 {
			return errors.New("areas: truncated point cell")
		}
		latOff := binary.BigEndian.Uint16(v[0:2])
		lngOff := binary.BigEndian.Uint16(v[2:4])
		n, m := binary.Uvarint(v[4:])
		if m <= 0 || 4+m+int(n) > len(v) {
			return errors.New("areas: corrupt point cell")
		}
		blob := v[4+m : 4+m+int(n)]
		lat := (float64(i) + float64(latOff)/offsetMax) * pointDeg
		lng := (float64(j) + float64(lngOff)/offsetMax) * pointDeg
		if err := fn(lat, lng, blob); err != nil {
			return err
		}
		v = v[4+m+int(n):]
	}
	return nil
}

func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371.0
	p := math.Pi / 180
	dla, dlo := (lat2-lat1)*p, (lng2-lng1)*p
	a := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(lat1*p)*math.Cos(lat2*p)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}
