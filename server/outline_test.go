package main

import (
	"encoding/binary"
	"math"
	"net/http/httptest"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestSimplifyRing(t *testing.T) {
	// A closed square whose bottom edge carries many collinear-ish vertices.
	ring := [][2]float64{{0, 0}}
	for i := 1; i < 50; i++ {
		ring = append(ring, [2]float64{float64(i) / 50 * 10, 0.0001 * float64(i%2)})
	}
	ring = append(ring, [2]float64{10, 0}, [2]float64{10, 10}, [2]float64{0, 10}, [2]float64{0, 0})
	got := simplifyRing(ring, 0.05)
	if len(got) != 5 {
		t.Fatalf("got %d vertices, want the 4 corners plus the closing one: %v", len(got), got)
	}
	if got[0] != got[len(got)-1] {
		t.Fatal("ring no longer closed")
	}
}

func TestAreaOutline(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "places.bolt"), 0o644, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	shape := binary.AppendUvarint(nil, 1)
	shape = binary.AppendUvarint(shape, 5)
	for _, p := range [][2]float32{{0, 0}, {2, 0}, {2, 2}, {0, 2}, {0, 0}} {
		for _, f := range p {
			shape = binary.LittleEndian.AppendUint32(shape, math.Float32bits(f))
		}
	}
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], 77)
	err = db.Update(func(tx *bolt.Tx) error {
		nb, _ := tx.CreateBucket([]byte(placeByNameBucket))
		sb, _ := tx.CreateBucket([]byte(placeShapesBucket))
		ib, _ := tx.CreateBucket([]byte(placeByIDBucket))
		tx.CreateBucket([]byte(placeByCellBucket))
		mb, _ := tx.CreateBucket([]byte(placeMetaBucket))
		val := encodeTestPlaceValue(1, 1, 1000000, "ES", 2, 15, "Spain", "Galicia")
		nb.Put(append(append([]byte("galicia"), 0), id[:]...), val)
		ib.Put(id[:], val)
		sb.Put(id[:], shape)
		return mb.Put([]byte(placeCountMetaKey), binary.AppendUvarint(nil, 1))
	})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := openPlaceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	r := &AreaResolver{places: ps}

	rings, err := r.Outline("p77")
	if err != nil || len(rings) != 1 || len(rings[0]) != 5 {
		t.Fatalf("Outline(p77) = %v, %v", rings, err)
	}
	if rings[0][1] != [2]float64{0, 2} { // [lat, lng] of the (lng 2, lat 0) vertex
		t.Fatalf("vertex order: %v", rings[0])
	}
	for _, key := range []string{"p78", "c40.000,-3.000,5.0", "bogus"} {
		if _, err := r.Outline(key); err == nil {
			t.Errorf("Outline(%q) succeeded", key)
		}
	}

	s := &Server{areas: r}
	for key, want := range map[string]int{"p77": 200, "p78": 404, "bogus": 400} {
		req := httptest.NewRequest("GET", "/api/places/"+key+"/outline", nil)
		req.SetPathValue("key", key)
		w := httptest.NewRecorder()
		s.handleAreaOutline(w, req)
		if w.Code != want {
			t.Errorf("%s: status %d, want %d", key, w.Code, want)
		}
	}
}
