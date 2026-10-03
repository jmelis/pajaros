package main

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"github.com/jmelis/pajaros/server/internal/placekey"

	bolt "go.etcd.io/bbolt"
)

// encodeTestPlaceValue mirrors cmd/gensnapshot/places.go's encodePlaceValue
// (a separate `main` package this test can't import), so it must match
// decodePlace's format exactly.
func encodeTestPlaceValue(lat, lng float64, population int, countryCode string, kind byte, radiusKm float64, country, displayName string) []byte {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint64(buf[0:8], math.Float64bits(lat))
	binary.LittleEndian.PutUint64(buf[8:16], math.Float64bits(lng))
	buf = binary.AppendUvarint(buf, uint64(population))
	if len(countryCode) != 2 {
		countryCode = "  "
	}
	buf = append(buf, countryCode...)
	buf = append(buf, kind)
	buf = binary.AppendUvarint(buf, uint64(math.Round(radiusKm*10)))
	buf = append(buf, byte(len(country)))
	buf = append(buf, country...)
	buf = append(buf, displayName...)
	return buf
}

// buildTestPlaceStore writes a tiny places.bolt in the same shape
// cmd/gensnapshot's `places` subcommand does, so PlaceStore.Search is
// tested against the real encoding rather than a mock.
func buildTestPlaceStore(t *testing.T, rows []struct {
	name, ascii, cc string
	id              uint64
	lat, lng        float64
	population      int
}) *PlaceStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "places.bolt")
	db, err := bolt.Open(path, 0o644, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	err = db.Update(func(tx *bolt.Tx) error {
		nb, err := tx.CreateBucketIfNotExists([]byte(placeByNameBucket))
		if err != nil {
			return err
		}
		ib, err := tx.CreateBucketIfNotExists([]byte(placeByIDBucket))
		if err != nil {
			return err
		}
		for _, r := range rows {
			key := append([]byte(placekey.Normalize(r.ascii)), 0)
			var idBuf [8]byte
			binary.BigEndian.PutUint64(idBuf[:], r.id)
			key = append(key, idBuf[:]...)
			val := encodeTestPlaceValue(r.lat, r.lng, r.population, r.cc, 0, 2, "Testland", r.name)
			if err := nb.Put(key, val); err != nil {
				return err
			}
			if err := ib.Put(idBuf[:], val); err != nil {
				return err
			}
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(placeShapesBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(placeByCellBucket)); err != nil {
			return err
		}
		mb, err := tx.CreateBucketIfNotExists([]byte(placeMetaBucket))
		if err != nil {
			return err
		}
		return mb.Put([]byte(placeCountMetaKey), binary.AppendUvarint(nil, uint64(len(rows))))
	})
	if err != nil {
		t.Fatal(err)
	}

	s, err := openPlaceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlaceStoreSearch(t *testing.T) {
	rows := []struct {
		name, ascii, cc string
		id              uint64
		lat, lng        float64
		population      int
	}{
		{"Pozuelo del Rey", "Pozuelo del Rey", "ES", 1, 40.35, -3.29, 2400},
		{"Pozuelo de Alarcón", "Pozuelo de Alarcon", "ES", 2, 40.43, -3.81, 86300},
		{"San Jose", "San Jose", "US", 3, 37.34, -121.89, 1000000},
		{"San José", "San Jose", "CR", 4, 9.93, -84.08, 342000},
		{"Zürich", "Zurich", "CH", 5, 47.37, 8.54, 400000},
	}
	s := buildTestPlaceStore(t, rows)

	if got := s.Len(); got != len(rows) {
		t.Fatalf("Len() = %d, want %d", got, len(rows))
	}

	t.Run("prefix match ranks by population", func(t *testing.T) {
		got := s.Search("pozuelo", 10)
		if len(got) != 2 {
			t.Fatalf("got %d results, want 2: %+v", len(got), got)
		}
		if got[0].Name != "Pozuelo de Alarcón" || got[1].Name != "Pozuelo del Rey" {
			t.Fatalf("wrong order: %+v", got)
		}
	})

	t.Run("accented query matches the folded key", func(t *testing.T) {
		got := s.Search("Zürich", 10)
		if len(got) != 1 || got[0].Name != "Zürich" {
			t.Fatalf("got %+v, want Zürich", got)
		}
	})

	t.Run("case and accent insensitive via asciiname", func(t *testing.T) {
		got := s.Search("ZURICH", 10)
		if len(got) != 1 || got[0].Name != "Zürich" {
			t.Fatalf("got %+v, want Zürich", got)
		}
	})

	t.Run("same name different countries both come back", func(t *testing.T) {
		got := s.Search("san jose", 10)
		if len(got) != 2 {
			t.Fatalf("got %d results, want 2: %+v", len(got), got)
		}
		ccs := map[string]bool{got[0].CountryCode: true, got[1].CountryCode: true}
		if !ccs["US"] || !ccs["CR"] {
			t.Fatalf("missing expected country codes: %+v", got)
		}
	})

	t.Run("limit caps results", func(t *testing.T) {
		got := s.Search("san jose", 1)
		if len(got) != 1 {
			t.Fatalf("got %d results, want 1", len(got))
		}
		if got[0].CountryCode != "US" { // higher population
			t.Fatalf("limit=1 should keep the most populous match, got %+v", got[0])
		}
	})

	t.Run("no match returns empty", func(t *testing.T) {
		got := s.Search("nowhere", 10)
		if len(got) != 0 {
			t.Fatalf("got %+v, want none", got)
		}
	})

	t.Run("short query returns nothing rather than scanning everything", func(t *testing.T) {
		got := s.Search("p", 10)
		if got != nil {
			t.Fatalf("got %+v, want nil for a 1-char query", got)
		}
	})
}

func TestTopPlacePerCountry(t *testing.T) {
	rows := []struct {
		name, ascii, cc string
		id              uint64
		lat, lng        float64
		population      int
	}{
		{"Pozuelo del Rey", "Pozuelo del Rey", "ES", 1, 40.35, -3.29, 2400},
		{"Pozuelo de Alarcón", "Pozuelo de Alarcon", "ES", 2, 40.43, -3.81, 86300},
		{"San Jose", "San Jose", "US", 3, 37.34, -121.89, 1000000},
		{"San José", "San Jose", "CR", 4, 9.93, -84.08, 342000},
		{"Zürich", "Zurich", "CH", 5, 47.37, 8.54, 400000},
	}
	s := buildTestPlaceStore(t, rows)

	got := s.TopPlacePerCountry()
	want := map[string]string{"ES": "Pozuelo de Alarcón", "US": "San Jose", "CR": "San José", "CH": "Zürich"}
	if len(got) != len(want) {
		t.Fatalf("got %d countries, want %d: %+v", len(got), len(want), got)
	}
	for cc, name := range want {
		if got[cc].Name != name {
			t.Errorf("%s: got %q, want %q", cc, got[cc].Name, name)
		}
	}
}

func TestOpenPlaceStoreMissingBucket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.bolt")
	db, err := bolt.Open(path, 0o644, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := openPlaceStore(db); err == nil {
		t.Fatal("expected an error opening a places.bolt with no buckets")
	}
}

func TestPlaceShapeAndLabel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "places.bolt")
	db, err := bolt.Open(path, 0o644, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// A square from (0,0) to (2,2), stored as float32 pairs.
	shape := binary.AppendUvarint(nil, 1)
	shape = binary.AppendUvarint(shape, 4)
	for _, p := range [][2]float32{{0, 0}, {2, 0}, {2, 2}, {0, 2}} {
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
		// Two names for one place: a prefix matching both must return it once.
		for _, n := range []string{"galicia", "galiza"} {
			key := append(append([]byte(n), 0), id[:]...)
			nb.Put(key, encodeTestPlaceValue(1, 1, 1000000, "ES", 2, 15, "Spain", "Galicia"))
		}
		ib.Put(id[:], encodeTestPlaceValue(1, 1, 1000000, "ES", 2, 15, "Spain", "Galicia"))
		sb.Put(id[:], shape)
		return mb.Put([]byte(placeCountMetaKey), binary.AppendUvarint(nil, 2))
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := openPlaceStore(db)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Search("gali", 10)
	if len(got) != 1 {
		t.Fatalf("got %+v, want one deduplicated result", got)
	}
	p := got[0]
	if p.ID != 77 || p.Kind != "region" || p.Country != "Spain" || p.CountryCode != "ES" || p.RadiusKm != 15 {
		t.Fatalf("decoded %+v", p)
	}
	sh, ok := s.Shape(77)
	if !ok || !sh.Contains(1, 1) || sh.Contains(3, 1) {
		t.Fatalf("shape lookup failed: ok=%v", ok)
	}
	if _, ok := s.Shape(78); ok {
		t.Fatal("unknown id returned a shape")
	}
	byID, ok := s.ByID(77)
	if !ok || byID.Name != "Galicia" || byID.Kind != "region" || byID.ID != 77 {
		t.Fatalf("ByID(77) = %+v, %v", byID, ok)
	}
	if _, ok := s.ByID(78); ok {
		t.Fatal("unknown id found by ID")
	}
}

func TestLocate(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "places.bolt"), 0o644, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	square := func(x0, y0, x1, y1 float32) []byte {
		b := binary.AppendUvarint(nil, 1)
		b = binary.AppendUvarint(b, 4)
		for _, p := range [][2]float32{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}} {
			for _, f := range p {
				b = binary.LittleEndian.AppendUint32(b, math.Float32bits(f))
			}
		}
		return b
	}
	id := func(n uint64) []byte { return binary.BigEndian.AppendUint64(nil, n) }
	cell := func(lat, lng float64) []byte {
		i, j := cellRowCol(lat, lng)
		return binary.BigEndian.AppendUint32(nil, uint32(i)<<16|uint32(j))
	}
	err = db.Update(func(tx *bolt.Tx) error {
		nb, _ := tx.CreateBucket([]byte(placeByNameBucket))
		_ = nb
		ib, _ := tx.CreateBucket([]byte(placeByIDBucket))
		sb, _ := tx.CreateBucket([]byte(placeShapesBucket))
		cb, _ := tx.CreateBucket([]byte(placeByCellBucket))
		mb, _ := tx.CreateBucket([]byte(placeMetaBucket))
		ib.Put(id(1), encodeTestPlaceValue(1, 1, 1e6, "", 1, 100, "", "Spain"))
		ib.Put(id(2), encodeTestPlaceValue(1, 1, 1e6, "ES", 2, 30, "Spain", "Galicia"))
		ib.Put(id(3), encodeTestPlaceValue(1, 1, 5e5, "ES", 2, 5, "Spain", "A Coruña"))
		ib.Put(id(6), encodeTestPlaceValue(1.5, 1.5, 9e5, "ES", 10, 3, "Spain", "Santiago"))
		sb.Put(id(6), square(1, 1, 1.4, 1.4))
		ib.Put(id(4), encodeTestPlaceValue(1.25, 1.25, 500, "ES", 9, 2, "Spain", "Ames"))
		ib.Put(id(5), encodeTestPlaceValue(1.9, 1.9, 5000, "ES", 8, 2, "Spain", "Far"))
		sb.Put(id(1), square(-5, -5, 5, 5))
		sb.Put(id(2), square(0, 0, 2, 2))
		sb.Put(id(3), square(0.5, 0.5, 1.5, 1.5)) // smaller region inside Galicia
		entry := func(n uint64, class uint64) []byte { return binary.AppendUvarint(nil, n<<4|class) }
		var c []byte
		c = append(c, entry(1, cellClassCountry)...)
		c = append(c, entry(2, cellClassRegionBase+4)...)
		c = append(c, entry(3, cellClassRegionBase+6)...)
		c = append(c, entry(4, cellClassLocality)...)
		c = append(c, entry(6, cellClassMunicipality)...)
		c = append(c, entry(5, cellClassLocality)...)
		cb.Put(cell(1.1, 1.1), c)
		return mb.Put([]byte(placeCountMetaKey), binary.AppendUvarint(nil, 0))
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := openPlaceStore(db)
	if err != nil {
		t.Fatal(err)
	}

	// Inside the municipality: it names the point, and the broadest region
	// (level 4) beats the province inside it.
	got := s.Locate(1.1, 1.1)
	want := Locality{Near: "Santiago", Region: "Galicia", Country: "Spain", CountryCode: "ES"}
	if got != want {
		t.Fatalf("Locate(1.1, 1.1) = %+v, want %+v", got, want)
	}
	// Outside the municipality the closest locality names the point.
	if got := s.Locate(1.3, 1.45); got.Near != "Ames" || got.Region != "Galicia" {
		t.Fatalf("Locate(1.3, 1.45) = %+v, want Near Ames in Galicia", got)
	}
	// Nothing indexed here: an empty label, so the UI falls back to coordinates.
	if got := s.Locate(-40, 100); got != (Locality{}) {
		t.Fatalf("Locate in an empty cell = %+v, want zero", got)
	}
}
