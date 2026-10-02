package main

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jmelis/pajaros/server/internal/seasonal"
	bolt "go.etcd.io/bbolt"
)

// batchPoints is how many completed points to buffer before writing their
// species_by_hotspot and hotspot_by_id entries to bbolt in one transaction.
// The input is sorted by point (see below), so each key is written exactly
// once — no read-modify-write, no decoding an existing blob — and writes
// land in ascending key order within each batch, which is bbolt's B+tree
// fast path (mostly appending, minimal page splits) rather than the
// random-order-insert cost a large unsorted batch would pay.
const batchPoints = 500_000

// Bucket names in hotspots_seasonal.bolt. speciesBucketName holds the per-hotspot
// species popularity data; the other three hold hotspot metadata (lat,
// lng, name, totalCount). See server/hotspots_data.go, which reads all
// four (that file's speciesBucketName equivalent is species_store.go's own
// constant of the same name/value — both are declared independently, but
// must agree).
const (
	speciesBucketName   = "species_by_hotspot"
	hotspotByIDBucket   = "hotspot_by_id"
	hotspotByCellBucket = "hotspot_by_grid_cell"
	hotspotMetaBucket   = "hotspot_meta"
	hotspotCountMetaKey = "count"

	// speciesFormatMetaKey/speciesFormatValue label the encoding of
	// species_by_hotspot values; server/species_store.go refuses a file whose
	// label differs from the one it decodes.
	speciesFormatMetaKey = "species_format"
	speciesFormatValue   = "seasonal-bits-1"
)

// gridDegrees must match server/hotspots_data.go's constant of the same
// name exactly — it determines which bbolt key ("lat,lng" cell) each
// hotspot's grid-cell entry is written under, and the server computes the
// identical key at query time to find it.
const gridDegrees = 1.0

type gridCell struct{ latCell, lngCell int }

func cellFor(lat, lng float64) gridCell {
	return gridCell{int(math.Floor(lat / gridDegrees)), int(math.Floor(lng / gridDegrees))}
}

// cellKeyOffset shifts cell coordinates into an always-non-negative range
// before big-endian encoding, so byte-order comparison of encoded keys
// matches numeric cell order (comfortably larger than any real cell
// coordinate: |latCell| <= 90, |lngCell| <= 180). This encoding must match
// server/hotspots_data.go's cellKey exactly, or Nearby's Gets simply won't
// find what this build wrote for a given cell.
const cellKeyOffset = 1 << 20

func cellKey(c gridCell) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf[0:4], uint32(c.latCell+cellKeyOffset))
	binary.BigEndian.PutUint32(buf[4:8], uint32(c.lngCell+cellKeyOffset))
	return buf
}

// runHotspots streams the GBIF aggregate TSV — columns decimallatitude,
// decimallongitude, locality, species, month, n, sorted by (lat, lng) — see
// ARCHITECTURE.md for the exact SQL, which includes
// "ORDER BY decimalLatitude, decimalLongitude" specifically so this tool
// can rely on same-point rows being adjacent, and on points themselves
// arriving in non-decreasing (lat, lng) order.
//
// Because the input is sorted, memory stays O(1) relative to row count and
// close to O(1) relative to total point count too: only the point
// currently being read is held in full (species counts, locality counts, a
// running total); the moment its key changes, that point is complete and
// gets queued for writing, never touched again. Completed points are
// batched (batchPoints at a time) into single bbolt transactions against
// species_by_hotspot and hotspot_by_id, with fresh, sorted-order Puts.
//
// hotspot_by_grid_cell entries can't be flushed per-point the same way,
// since a cell accumulates entries from many points before it's complete.
// Instead this exploits the same sort order at a coarser grain: since lat
// only increases as the file is read, a point's 1-degree latitude cell
// (gridCell.latCell) is non-decreasing too, so once it advances past a
// given value, no later point can ever fall back into it. That makes each
// "latitude band" (a run of points sharing one latCell) a safe flush
// boundary — cells belonging to it are accumulated in memory only until
// the band ends, then written and discarded, bounding memory to one
// band's worth of points (a small fraction of the worldwide total) rather
// than the whole dataset.
//
// Writes one file, data/hotspots_seasonal.bolt, a bbolt KV store with four buckets:
//   - species_by_hotspot: hotspot ID -> bit-packed record counts per
//     species per calendar month (see internal/seasonal).
//   - hotspot_by_id: hotspot ID -> encoded {lat,lng,name,totalCount}.
//   - hotspot_by_grid_cell: encoded grid cell -> encoded
//     {id,lat,lng,name,totalCount} entries for every hotspot in that cell,
//     packed back-to-back.
//   - hotspot_meta: small fixed keys: the total hotspot count (so the
//     server's Len() doesn't need a full bucket scan) and the species blob
//     format version (so a server never misreads a file built for another
//     encoding).
func runHotspots(tsvPath string) error {
	speciesIndex, err := loadSpeciesIndex()
	if err != nil {
		return fmt.Errorf("load species index (run `gensnapshot taxonomy` first): %w", err)
	}
	fmt.Fprintf(os.Stderr, "%d species in taxonomy\n", len(speciesIndex))

	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}
	boltPath := filepath.Join("data", seasonal.FileName)
	os.Remove(boltPath)
	db, err := bolt.Open(boltPath, 0o644, nil)
	if err != nil {
		return fmt.Errorf("open %s: %w", boltPath, err)
	}
	defer db.Close()

	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{speciesBucketName, hotspotByIDBucket, hotspotByCellBucket, hotspotMetaBucket} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	type kv struct{ key, blob []byte }
	pendingSpecies := make([]kv, 0, batchPoints)
	pendingHotspot := make([]kv, 0, batchPoints)

	flushBatch := func() error {
		if len(pendingSpecies) == 0 {
			return nil
		}
		err := db.Update(func(tx *bolt.Tx) error {
			// FillPercent defaults to 0.5 (bbolt leaves half of each page
			// free to absorb future random-order inserts). Every key here
			// is written exactly once, in strictly ascending order, and
			// never touched again, so that headroom is pure waste — fill
			// pages completely instead.
			sb := tx.Bucket([]byte(speciesBucketName))
			sb.FillPercent = 1.0
			for _, w := range pendingSpecies {
				if err := sb.Put(w.key, w.blob); err != nil {
					return err
				}
			}
			hb := tx.Bucket([]byte(hotspotByIDBucket))
			hb.FillPercent = 1.0
			for _, w := range pendingHotspot {
				if err := hb.Put(w.key, w.blob); err != nil {
					return err
				}
			}
			return nil
		})
		pendingSpecies = pendingSpecies[:0]
		pendingHotspot = pendingHotspot[:0]
		return err
	}

	// Grid-cell accumulator for the current latitude band — see the
	// function doc above for why this is a safe, bounded-memory flush
	// boundary given the input's sort order.
	gridBandStarted := false
	curGridLatCell := 0
	gridCells := map[int][]byte{}

	flushGridBand := func() error {
		if !gridBandStarted || len(gridCells) == 0 {
			gridCells = map[int][]byte{}
			gridBandStarted = false
			return nil
		}
		// Ascending lngCell order matches cellKey's big-endian encoding, so
		// (combined with latCell only ever increasing between bands) every
		// grid-cell Put across the whole build lands in ascending key
		// order — bbolt's fast path.
		lngCells := make([]int, 0, len(gridCells))
		for lc := range gridCells {
			lngCells = append(lngCells, lc)
		}
		sort.Ints(lngCells)
		err := db.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte(hotspotByCellBucket))
			b.FillPercent = 1.0 // see flushBatch's comment — same append-only reasoning applies here
			for _, lc := range lngCells {
				key := cellKey(gridCell{curGridLatCell, lc})
				if err := b.Put(key, gridCells[lc]); err != nil {
					return err
				}
			}
			return nil
		})
		gridCells = map[int][]byte{}
		gridBandStarted = false
		return err
	}

	pointCount := 0

	// Current point's accumulator — the only per-point state ever held at
	// once, discarded the moment the key changes.
	var curKey, curLat, curLng string
	localityCounts := map[string]int{}
	speciesCounts := map[uint16]*seasonal.Months{}
	total := 0
	started := false

	finishPoint := func() error {
		if !started {
			return nil
		}
		latF, err := strconv.ParseFloat(curLat, 64)
		if err != nil {
			return fmt.Errorf("bad lat %q: %w", curLat, err)
		}
		lngF, err := strconv.ParseFloat(curLng, 64)
		if err != nil {
			return fmt.Errorf("bad lng %q: %w", curLng, err)
		}
		name := dominantLocality(localityCounts)

		pendingSpecies = append(pendingSpecies, kv{key: []byte(curKey), blob: encodeSpeciesBlob(speciesCounts)})
		pendingHotspot = append(pendingHotspot, kv{key: []byte(curKey), blob: encodeHotspotByIDValue(latF, lngF, name, total)})

		cell := cellFor(latF, lngF)
		if gridBandStarted && cell.latCell != curGridLatCell {
			if err := flushGridBand(); err != nil {
				return err
			}
		}
		curGridLatCell = cell.latCell
		gridBandStarted = true
		gridCells[cell.lngCell] = append(gridCells[cell.lngCell], encodeHotspotEntry(curKey, latF, lngF, name, total)...)

		pointCount++
		if len(pendingSpecies) >= batchPoints {
			if err := flushBatch(); err != nil {
				return err
			}
		}
		return nil
	}

	f, err := openTSV(tsvPath)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	rows, unresolvedSpecies, badMonth := 0, 0, 0
	// Detects out-of-order input early rather than silently mis-grouping.
	// Must compare numerically, not as strings: GBIF's ORDER BY sorts the
	// underlying doubles, and text order disagrees with numeric order
	// across digit-count boundaries (e.g. "10.2" < "9.5" as strings, but
	// 10.2 > 9.5) — a naive string comparison here would false-positive
	// constantly at global scale, where latitude crosses such boundaries
	// throughout the file. The grid-cell batching above also depends on
	// this ordering holding, not just the per-point grouping.
	var prevLat, prevLng float64
	havePrev := false

	for sc.Scan() {
		rows++
		if rows%20_000_000 == 0 {
			fmt.Fprintf(os.Stderr, "%dM rows processed, %d points so far\n", rows/1_000_000, pointCount)
		}
		parts := strings.SplitN(sc.Text(), "\t", 6)
		if len(parts) != 6 {
			continue // malformed line (or header row) — skip rather than abort a multi-hour build
		}
		lat, lng, locality, species, monthStr, nStr := parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]
		n, err := strconv.Atoi(nStr)
		if err != nil {
			continue // also skips the header row
		}
		month, err := strconv.Atoi(monthStr)
		if err != nil || month < 1 || month > 12 {
			badMonth++
			continue
		}
		key := lat + "," + lng

		if key != curKey {
			latF, errLat := strconv.ParseFloat(lat, 64)
			lngF, errLng := strconv.ParseFloat(lng, 64)
			if errLat != nil || errLng != nil {
				continue // unparseable coordinate — skip rather than abort
			}
			if havePrev && (latF < prevLat || (latF == prevLat && lngF < prevLng)) {
				return fmt.Errorf("input not sorted by point: (%v,%v) came after (%v,%v) at row %d — "+
					"did the GBIF query include ORDER BY decimalLatitude, decimalLongitude?",
					latF, lngF, prevLat, prevLng, rows)
			}
			if err := finishPoint(); err != nil {
				return err
			}
			curKey, curLat, curLng = key, lat, lng
			localityCounts = map[string]int{}
			speciesCounts = map[uint16]*seasonal.Months{}
			total = 0
			started = true
			prevLat, prevLng, havePrev = latF, lngF, true
		}
		localityCounts[locality] += n
		total += n
		if spID, ok := speciesIndex[species]; ok {
			m := speciesCounts[spID]
			if m == nil {
				m = new(seasonal.Months)
				speciesCounts[spID] = m
			}
			m[month-1] += uint32(n)
		} else {
			unresolvedSpecies++
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if err := finishPoint(); err != nil {
		return err
	}
	if err := flushBatch(); err != nil {
		return err
	}
	if err := flushGridBand(); err != nil {
		return err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(hotspotMetaBucket))
		if err := b.Put([]byte(hotspotCountMetaKey), appendUvarint(nil, uint64(pointCount))); err != nil {
			return err
		}
		return b.Put([]byte(speciesFormatMetaKey), []byte(speciesFormatValue))
	}); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "%d rows processed, %d hotspots, %d rows with an unusable month skipped, %d species rows unresolved against taxonomy\n",
		rows, pointCount, badMonth, unresolvedSpecies)
	fmt.Fprintf(os.Stderr, "wrote %s (%d hotspots)\n", boltPath, pointCount)
	return nil
}

// encodeSpeciesBlob packs one hotspot's per-species, per-month counts into
// the species_by_hotspot value format (see internal/seasonal).
func encodeSpeciesBlob(counts map[uint16]*seasonal.Months) []byte {
	entries := make([]seasonal.Entry, 0, len(counts))
	for id, m := range counts {
		entries = append(entries, seasonal.Entry{ID: id, Months: *m})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return seasonal.Encode(entries)
}

// encodeHotspotByIDValue is the hotspot_by_id bucket's value format. The ID
// isn't repeated here (it's already the bucket key) — contrast with
// encodeHotspotEntry below, whose values pack multiple different hotspots
// together and so must carry each one's ID inline. Must exactly match
// server/hotspots_data.go's decodeHotspotByID.
func encodeHotspotByIDValue(lat, lng float64, name string, totalCount int) []byte {
	buf := make([]byte, 0, 16+binary.MaxVarintLen64+len(name))
	buf = appendFloat64(buf, lat)
	buf = appendFloat64(buf, lng)
	buf = appendUvarint(buf, uint64(totalCount))
	buf = append(buf, name...)
	return buf
}

// encodeHotspotEntry is one {id, lat, lng, name, totalCount} entry within a
// hotspot_by_grid_cell bucket value, where every hotspot in a cell is
// packed back-to-back. Must exactly match server/hotspots_data.go's
// decodeHotspotEntry.
func encodeHotspotEntry(id string, lat, lng float64, name string, totalCount int) []byte {
	buf := make([]byte, 0, len(id)+16+2*binary.MaxVarintLen64+len(name))
	buf = appendUvarint(buf, uint64(len(id)))
	buf = append(buf, id...)
	buf = appendFloat64(buf, lat)
	buf = appendFloat64(buf, lng)
	buf = appendUvarint(buf, uint64(totalCount))
	buf = appendUvarint(buf, uint64(len(name)))
	buf = append(buf, name...)
	return buf
}

func appendFloat64(buf []byte, f float64) []byte {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(f))
	return append(buf, tmp[:]...)
}

func appendUvarint(buf []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(buf, tmp[:n]...)
}

func dominantLocality(counts map[string]int) string {
	best, bestN := "", -1
	for name, n := range counts {
		if n > bestN {
			best, bestN = name, n
		}
	}
	return best
}

// openTSV opens path for a streaming pass. A ".zip" path (the
// as-downloaded shape of a GBIF SQL Download result) is decompressed on
// the fly straight from its single entry — the multi-GB unzipped CSV is
// never written to disk, only streamed. Any other path is opened as-is
// (used by tests with plain-text fixtures).
func openTSV(path string) (io.ReadCloser, error) {
	if !strings.HasSuffix(path, ".zip") {
		return os.Open(path)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	if len(zr.File) != 1 {
		zr.Close()
		return nil, fmt.Errorf("expected exactly one file in %s, found %d", path, len(zr.File))
	}
	entry, err := zr.File[0].Open()
	if err != nil {
		zr.Close()
		return nil, err
	}
	return &zipEntryReader{ReadCloser: entry, zr: zr}, nil
}

// zipEntryReader closes both the decompressing entry reader and the parent
// zip.ReadCloser (which holds the underlying *os.File) together, so a
// single Close() fully releases the zip.
type zipEntryReader struct {
	io.ReadCloser
	zr *zip.ReadCloser
}

func (z *zipEntryReader) Close() error {
	err := z.ReadCloser.Close()
	if zerr := z.zr.Close(); err == nil {
		err = zerr
	}
	return err
}

// loadSpeciesIndex assigns each taxonomy species a stable uint16 ID, keyed
// by scientific name (what the GBIF TSV's `species` column carries). IDs
// are assigned by sorting on eBird speciesCode, not read from a separately
// shipped file — both this build tool and the server compute the same
// mapping independently from the same committed data/taxonomy_core.json.gz,
// so there's nothing to keep in sync by hand.
func loadSpeciesIndex() (map[string]uint16, error) {
	f, err := os.Open("data/taxonomy_core.json.gz")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var taxa []coreTaxonOut
	if err := json.NewDecoder(gz).Decode(&taxa); err != nil {
		return nil, err
	}
	if len(taxa) > math.MaxUint16 {
		return nil, fmt.Errorf("%d species exceeds uint16 range — widen the ID type", len(taxa))
	}

	sort.Slice(taxa, func(i, j int) bool { return taxa[i].SpeciesCode < taxa[j].SpeciesCode })
	idBySciName := make(map[string]uint16, len(taxa))
	for i, t := range taxa {
		idBySciName[t.SciName] = uint16(i)
	}
	return idBySciName, nil
}
