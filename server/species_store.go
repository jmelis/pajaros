package main

import (
	"encoding/binary"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

const speciesBucketName = "species_by_hotspot"

// SpeciesStore answers "which species, how often" for a hotspot ID from
// hotspots.bolt (built by cmd/gensnapshot — see ARCHITECTURE.md).
// bbolt is mmap-backed, so a lookup only pages in the data it actually
// touches — the worldwide dataset (low single-digit GB) is never loaded
// wholesale into memory.
type SpeciesStore struct {
	db        *bolt.DB
	taxonByID []Taxon // position i = stable species ID i, see TaxonomyStore.StableIDOrder
}

// openSpeciesStore reads from db's species_by_hotspot bucket — db is
// already open (see main.go, which shares one *bolt.DB handle between this
// and openHotspotStore, both reading from the same hotspots.bolt file).
func openSpeciesStore(db *bolt.DB, taxonomy *TaxonomyStore) *SpeciesStore {
	return &SpeciesStore{db: db, taxonByID: taxonomy.StableIDOrder()}
}

// SpeciesCount is one species' observation count at a hotspot.
type SpeciesCount struct {
	Code    string
	SciName string
	Count   int
}

// Lookup returns a hotspot's species, sorted by count descending — the
// order cmd/gensnapshot encoded the blob in, so no runtime sort is needed
// here. Returns an empty (not nil-error) slice for an unknown hotspot ID.
func (s *SpeciesStore) Lookup(hotspotID string) ([]SpeciesCount, error) {
	var out []SpeciesCount
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(speciesBucketName))
		if b == nil {
			return fmt.Errorf("bucket %q not found", speciesBucketName)
		}
		blob := b.Get([]byte(hotspotID))
		i := 0
		for i < len(blob) {
			id := uint16(blob[i]) | uint16(blob[i+1])<<8
			i += 2
			n, m := binary.Uvarint(blob[i:])
			i += m
			if int(id) < len(s.taxonByID) {
				t := s.taxonByID[id]
				out = append(out, SpeciesCount{Code: t.SpeciesCode, SciName: t.SciName, Count: int(n)})
			}
		}
		return nil
	})
	return out, err
}
