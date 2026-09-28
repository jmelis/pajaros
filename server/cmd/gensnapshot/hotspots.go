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
	"sort"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// batchPoints is how many completed points to buffer before writing them to
// bbolt in one transaction. The input is sorted by point (see below), so
// each key is written exactly once — no read-modify-write, no decoding an
// existing blob — and writes land in ascending key order within each
// batch, which is bbolt's B+tree fast path (mostly appending, minimal page
// splits) rather than the random-order-insert cost a large unsorted batch
// would pay.
const batchPoints = 500_000

// hotspotOut is one entry in the small in-memory hotspot index
// (data/hotspots_index.json.gz) — lat/lng/name/totalCount only, no species
// data, cheap enough to load fully into memory at server startup for
// map/search browsing. The bulky per-species data lives in the sibling
// data/hotspots.bolt KV store instead, fetched lazily by ID.
type hotspotOut struct {
	ID         string  `json:"id"`
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	Name       string  `json:"name"`
	TotalCount int     `json:"totalCount"`
}

// runHotspots streams the GBIF aggregate TSV — columns decimallatitude,
// decimallongitude, locality, species, n, sorted by (lat, lng) — see
// docs/GBIF_DATA_PIPELINE.md for the exact SQL, which includes
// "ORDER BY decimalLatitude, decimalLongitude" specifically so this tool
// can rely on same-point rows being adjacent.
//
// Because the input is sorted, memory stays O(1) relative to both row
// count and total point count: only the point currently being read is
// held in full; the moment the key changes, that point is complete and
// gets queued for writing, never touched again. Completed points are
// batched (batchPoints at a time) into single bbolt transactions with
// fresh, sorted-order Puts.
//
// Writes two files under data/:
//   - hotspots_index.json.gz: every point's {id,lat,lng,name,totalCount},
//     for map/search browsing.
//   - hotspots.bolt: a bbolt KV store (bucket "species_by_hotspot") mapping
//     hotspot ID to a binary blob of [uint16 speciesID, varint count] pairs,
//     sorted by count descending, fetched lazily by ID at request time.
func runHotspots(tsvPath string) error {
	speciesIndex, err := loadSpeciesIndex()
	if err != nil {
		return fmt.Errorf("load species index (run `gensnapshot taxonomy` first): %w", err)
	}
	fmt.Fprintf(os.Stderr, "%d species in taxonomy\n", len(speciesIndex))

	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}
	boltPath := "data/hotspots.bolt"
	os.Remove(boltPath)
	db, err := bolt.Open(boltPath, 0o644, nil)
	if err != nil {
		return fmt.Errorf("open %s: %w", boltPath, err)
	}
	defer db.Close()

	const bucketName = "species_by_hotspot"
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	}); err != nil {
		return err
	}

	type pendingWrite struct {
		key  string
		blob []byte
	}
	pending := make([]pendingWrite, 0, batchPoints)

	flushBatch := func() error {
		if len(pending) == 0 {
			return nil
		}
		err := db.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte(bucketName))
			for _, w := range pending {
				if err := b.Put([]byte(w.key), w.blob); err != nil {
					return err
				}
			}
			return nil
		})
		pending = pending[:0]
		return err
	}

	var index []hotspotOut

	// Current point's accumulator — the only per-point state ever held at
	// once, discarded the moment the key changes.
	var curKey, curLat, curLng string
	localityCounts := map[string]int{}
	speciesCounts := map[uint16]int{}
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
		index = append(index, hotspotOut{
			ID: curKey, Lat: latF, Lng: lngF,
			Name:       dominantLocality(localityCounts),
			TotalCount: total,
		})
		pending = append(pending, pendingWrite{key: curKey, blob: encodeSpeciesBlob(speciesCounts)})
		if len(pending) >= batchPoints {
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
	rows, unresolvedSpecies := 0, 0
	// Detects out-of-order input early rather than silently mis-grouping.
	// Must compare numerically, not as strings: GBIF's ORDER BY sorts the
	// underlying doubles, and text order disagrees with numeric order
	// across digit-count boundaries (e.g. "10.2" < "9.5" as strings, but
	// 10.2 > 9.5) — a naive string comparison here would false-positive
	// constantly at global scale, where latitude crosses such boundaries
	// throughout the file.
	var prevLat, prevLng float64
	havePrev := false

	for sc.Scan() {
		rows++
		if rows%20_000_000 == 0 {
			fmt.Fprintf(os.Stderr, "%dM rows processed, %d points so far\n", rows/1_000_000, len(index))
		}
		parts := strings.SplitN(sc.Text(), "\t", 5)
		if len(parts) != 5 {
			continue // malformed line (or header row) — skip rather than abort a multi-hour build
		}
		lat, lng, locality, species, nStr := parts[0], parts[1], parts[2], parts[3], parts[4]
		n, err := strconv.Atoi(nStr)
		if err != nil {
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
			speciesCounts = map[uint16]int{}
			total = 0
			started = true
			prevLat, prevLng, havePrev = latF, lngF, true
		}
		localityCounts[locality] += n
		total += n
		if spID, ok := speciesIndex[species]; ok {
			speciesCounts[spID] += n
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
	fmt.Fprintf(os.Stderr, "%d rows processed, %d hotspots, %d species occurrences unresolved against taxonomy\n",
		rows, len(index), unresolvedSpecies)

	indexPath := "data/hotspots_index.json.gz"
	if err := writeGzippedJSON(indexPath, index); err != nil {
		return fmt.Errorf("write %s: %w", indexPath, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d hotspots) and %s\n", indexPath, len(index), boltPath)
	return nil
}

// encodeSpeciesBlob packs speciesID -> count as [uint16 id, varint count]
// pairs, sorted by count descending (popularity order — no runtime sort
// needed when the server reads it back).
func encodeSpeciesBlob(counts map[uint16]int) []byte {
	type entry struct {
		id uint16
		n  int
	}
	entries := make([]entry, 0, len(counts))
	for id, n := range counts {
		entries = append(entries, entry{id, n})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].n > entries[j].n })

	var buf []byte
	var tmp [binary.MaxVarintLen64]byte
	for _, e := range entries {
		buf = append(buf, byte(e.id), byte(e.id>>8))
		m := binary.PutUvarint(tmp[:], uint64(e.n))
		buf = append(buf, tmp[:m]...)
	}
	return buf
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
// mapping independently from the same committed data/taxonomy_en.json.gz,
// so there's nothing to keep in sync by hand.
func loadSpeciesIndex() (map[string]uint16, error) {
	f, err := os.Open("data/taxonomy_en.json.gz")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var taxa []taxonOut
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
