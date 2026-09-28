package main

import (
	"encoding/binary"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

const speciesBucketName = "species_by_hotspot"

// SpeciesStore answers "which species, how often" for a hotspot ID from
// hotspots.bolt (built by cmd/gensnapshot — see docs/GBIF_DATA_PIPELINE.md).
// bbolt is mmap-backed, so a lookup only pages in the data it actually
// touches — the worldwide dataset (low single-digit GB) is never loaded
// wholesale into memory.
type SpeciesStore struct {
	db        *bolt.DB
	taxonByID []Taxon // position i = stable species ID i, see TaxonomyStore.StableIDOrder
}

func openSpeciesStore(path string, taxonomy *TaxonomyStore) (*SpeciesStore, error) {
	db, err := bolt.Open(path, 0o444, &bolt.Options{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	return &SpeciesStore{db: db, taxonByID: taxonomy.StableIDOrder()}, nil
}

func (s *SpeciesStore) Close() error {
	return s.db.Close()
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
