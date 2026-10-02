package main

import (
	"errors"
	"fmt"
	"sort"

	"github.com/jmelis/pajaros/server/internal/seasonal"
	bolt "go.etcd.io/bbolt"
)

const speciesBucketName = "species_by_hotspot"

// speciesFormatMetaKey/speciesFormatValue identify the encoding of
// species_by_hotspot values in hotspot_meta. Must match
// cmd/gensnapshot/hotspots.go; opening a file with another label fails fast
// rather than decoding garbage.
const (
	speciesFormatMetaKey = "species_format"
	speciesFormatValue   = "seasonal-bits-1"
)

// SpeciesStore answers "which species, how often" for a hotspot ID from
// hotspots_seasonal.bolt (built by cmd/gensnapshot — see ARCHITECTURE.md).
// bbolt is mmap-backed, so a lookup only pages in the data it actually
// touches — the worldwide dataset (low single-digit GB) is never loaded
// wholesale into memory.
type SpeciesStore struct {
	db        *bolt.DB
	taxonByID []Taxon // position i = stable species ID i, see TaxonomyStore.StableIDOrder
}

// openSpeciesStore reads from db's species_by_hotspot bucket — db is
// already open (see main.go, which shares one *bolt.DB handle between this
// and openHotspotStore, both reading from the same hotspots_seasonal.bolt file). It
// fails if the file's species encoding is not the one this build decodes.
func openSpeciesStore(db *bolt.DB, taxonomy *TaxonomyStore) (*SpeciesStore, error) {
	err := db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte(hotspotMetaBucket))
		if meta == nil {
			return fmt.Errorf("bucket %q not found", hotspotMetaBucket)
		}
		if got := string(meta.Get([]byte(speciesFormatMetaKey))); got != speciesFormatValue {
			return fmt.Errorf("%s species format is %q, this server reads %q — rebuild the file", seasonal.FileName, got, speciesFormatValue)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &SpeciesStore{db: db, taxonByID: taxonomy.StableIDOrder()}, nil
}

// SpeciesCount is one species' observation count at a hotspot.
type SpeciesCount struct {
	Code    string
	SciName string
	Count   int
}

// errUnknownHotspot is returned by Lookup for an ID with no species data.
var errUnknownHotspot = errors.New("unknown hotspot")

// Lookup returns a hotspot's species with their record counts, sorted by
// count descending (ties by species ID). month is 1..12 for the records of
// that calendar month only (years pooled; species never recorded in it are
// absent), or 0 for the whole year. A known hotspot with no records in the
// month yields an empty list; an unknown hotspot ID yields errUnknownHotspot.
func (s *SpeciesStore) Lookup(hotspotID string, month int) ([]SpeciesCount, error) {
	defer bboltTimer("species", "lookup")()
	entries, err := s.entries(hotspotID)
	if err != nil {
		return nil, err
	}
	out := make([]SpeciesCount, 0, len(entries))
	for _, e := range entries {
		n := e.Months.Total()
		if month != 0 {
			n = int(e.Months[month-1])
		}
		if n == 0 || int(e.ID) >= len(s.taxonByID) {
			continue
		}
		t := s.taxonByID[e.ID]
		out = append(out, SpeciesCount{Code: t.SpeciesCode, SciName: t.SciName, Count: n})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// entries decodes a hotspot's species blob.
func (s *SpeciesStore) entries(hotspotID string) ([]seasonal.Entry, error) {
	var entries []seasonal.Entry
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(speciesBucketName))
		if b == nil {
			return fmt.Errorf("bucket %q not found", speciesBucketName)
		}
		blob := b.Get([]byte(hotspotID))
		if len(blob) == 0 {
			return errUnknownHotspot
		}
		var err error
		entries, err = seasonal.Decode(blob)
		return err
	})
	return entries, err
}

// Seasons classifies a hotspot's species as year-round, seasonal or
// occasional (see classifySeasons), keyed by scientific name. A hotspot with
// too little data yields an empty map; an unknown ID yields errUnknownHotspot.
func (s *SpeciesStore) Seasons(hotspotID string) (map[string]SpeciesSeason, error) {
	defer bboltTimer("species", "seasons")()
	entries, err := s.entries(hotspotID)
	if err != nil {
		return nil, err
	}
	out := map[string]SpeciesSeason{}
	for id, season := range classifySeasons(entries) {
		if int(id) >= len(s.taxonByID) {
			continue
		}
		season.SciName = s.taxonByID[id].SciName
		out[season.SciName] = season
	}
	return out, nil
}
