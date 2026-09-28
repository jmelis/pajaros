package main

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// encodeTestPlaceValue mirrors cmd/gensnapshot/places.go's encodePlaceValue
// (a separate `main` package this test can't import), so it must match
// decodePlace's format exactly: lat, lng, population, a fixed 2-byte
// country code, then the display name to the end of the buffer.
func encodeTestPlaceValue(lat, lng float64, population int, countryCode, displayName string) []byte {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint64(buf[0:8], math.Float64bits(lat))
	binary.LittleEndian.PutUint64(buf[8:16], math.Float64bits(lng))
	buf = binary.AppendUvarint(buf, uint64(population))
	buf = append(buf, countryCode...)
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
		for _, r := range rows {
			key := append([]byte(normalizePlaceName(r.ascii)), 0)
			var idBuf [8]byte
			binary.BigEndian.PutUint64(idBuf[:], r.id)
			key = append(key, idBuf[:]...)
			val := encodeTestPlaceValue(r.lat, r.lng, r.population, r.cc, r.name)
			if err := nb.Put(key, val); err != nil {
				return err
			}
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
