package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/jmelis/pajaros/server/internal/areas"
	"github.com/jmelis/pajaros/server/internal/seasonal"
	bolt "go.etcd.io/bbolt"
)

// runAreas rebuilds data/seasonal_cells.bolt from data/hotspots_seasonal.bolt: the same
// points, regrouped by 0.01-degree cell and pre-summed into 0.05, 0.25 and
// 1-degree cells (see internal/areas). It never calls out; the source file
// must already exist.
//
// Pass 1 walks species_by_hotspot sequentially, noting only each point's cell
// and where its key sits in an arena. After sorting by cell, pass 2 fetches
// each blob by key and feeds the Builder in cell order.
func runAreas() error {
	srcPath := filepath.Join("data", seasonal.FileName)
	src, err := bolt.Open(srcPath, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer src.Close()

	type rec struct {
		i, j   int32
		lat    float64
		lng    float64
		keyOff uint32
		keyLen uint8
	}
	var recs []rec
	var arena []byte
	err = src.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(speciesBucketName)).ForEach(func(k, _ []byte) error {
			c := bytes.IndexByte(k, ',')
			if c < 0 || len(k) > 255 {
				return fmt.Errorf("unexpected hotspot key %q", k)
			}
			lat, err := strconv.ParseFloat(string(k[:c]), 64)
			if err != nil {
				return err
			}
			lng, err := strconv.ParseFloat(string(k[c+1:]), 64)
			if err != nil {
				return err
			}
			recs = append(recs, rec{
				i: int32(math.Floor(lat / 0.01)), j: int32(math.Floor(lng / 0.01)),
				lat: lat, lng: lng, keyOff: uint32(len(arena)), keyLen: uint8(len(k)),
			})
			arena = append(arena, k...)
			return nil
		})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d points read; sorting\n", len(recs))
	sort.Slice(recs, func(a, b int) bool {
		if recs[a].i != recs[b].i {
			return recs[a].i < recs[b].i
		}
		return recs[a].j < recs[b].j
	})

	out := filepath.Join("data", areas.FileName)
	b, err := areas.NewBuilder(out)
	if err != nil {
		return err
	}
	var sourceTotal uint64
	err = src.View(func(tx *bolt.Tx) error {
		sb := tx.Bucket([]byte(speciesBucketName))
		for n, r := range recs {
			key := arena[r.keyOff : r.keyOff+uint32(r.keyLen)]
			blob := sb.Get(key)
			if blob == nil {
				return fmt.Errorf("blob for %q vanished", key)
			}
			es, err := seasonal.Decode(blob)
			if err != nil {
				return err
			}
			for _, e := range es {
				sourceTotal += uint64(e.Months.Total())
			}
			if err := b.Add(r.lat, r.lng, blob); err != nil {
				return err
			}
			if n%2_000_000 == 0 {
				fmt.Fprintf(os.Stderr, "%d / %d\n", n, len(recs))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	totals := b.Totals
	if err := b.Finish(); err != nil {
		return err
	}
	totals = b.Totals
	fmt.Fprintf(os.Stderr, "records: source %d, points %d, 0.05° %d, 0.25° %d, 1° %d\n",
		sourceTotal, totals[0], totals[1], totals[2], totals[3])
	for l := 0; l < 4; l++ {
		if totals[l] != sourceTotal {
			return fmt.Errorf("record total mismatch at level %d: %d != %d", l, totals[l], sourceTotal)
		}
	}
	st, err := os.Stat(out)
	if err == nil {
		fmt.Fprintf(os.Stderr, "wrote %s (%.2f GB)\n", out, float64(st.Size())/1e9)
	}
	return err
}
