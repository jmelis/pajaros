package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	bolt "go.etcd.io/bbolt"
)

// Bucket names in places.bolt. See server/places_data.go, which reads both.
const (
	placeByNameBucket = "place_by_name"
	placeMetaBucket   = "place_meta"
	placeCountMetaKey = "count"
)

type geoPlace struct {
	id          uint64
	name        string
	asciiName   string
	lat, lng    float64
	countryCode string
	population  int
}

// runPlaces builds data/places.bolt from a GeoNames gazetteer dump — see
// README.md for exactly which file (cities500.zip) and why. Unlike
// runHotspots, cities500 (a few hundred thousand rows) comfortably fits in
// memory, so this doesn't need runHotspots' streaming/sorted-input
// discipline: read everything, sort by key, write once.
//
// Writes one file, data/places.bolt, a bbolt KV store with two buckets:
//   - place_by_name: normalized-name key -> encoded {lat,lng,population,
//     countryCode,displayName}, ordered so a prefix scan is a name search.
//   - place_meta: small fixed keys, currently just the total place count.
func runPlaces(path string) error {
	f, err := openTSV(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var places []geoPlace
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	skipped := 0
	for sc.Scan() {
		// GeoNames' tab-separated dump format (see readme.txt at
		// download.geonames.org/export/dump/): geonameid, name, asciiname,
		// alternatenames, latitude, longitude, feature class, feature code,
		// country code, cc2, admin1..4 code, population, elevation, dem,
		// timezone, modification date.
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) < 15 {
			skipped++
			continue
		}
		id, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			skipped++
			continue
		}
		name, ascii, cc := parts[1], parts[2], parts[8]
		if ascii == "" || len(cc) != 2 {
			skipped++
			continue
		}
		lat, errLat := strconv.ParseFloat(parts[4], 64)
		lng, errLng := strconv.ParseFloat(parts[5], 64)
		if errLat != nil || errLng != nil {
			skipped++
			continue
		}
		pop, _ := strconv.Atoi(parts[14]) // blank/unparseable population just means 0, not a skip
		places = append(places, geoPlace{id: id, name: name, asciiName: ascii, lat: lat, lng: lng, countryCode: cc, population: pop})
	}
	if err := sc.Err(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d places parsed, %d rows skipped\n", len(places), skipped)

	type kv struct{ key, blob []byte }
	entries := make([]kv, 0, len(places))
	foldedAliases := 0
	for _, p := range places {
		blob := encodePlaceValue(p.lat, p.lng, p.population, p.countryCode, p.name)
		entries = append(entries, kv{key: placeKey(p.asciiName, p.id), blob: blob})

		// GeoNames' own asciiname transliteration doesn't always match a
		// simple accent-strip of the display name (e.g. Zürich's asciiname
		// is "Zuerich", the German convention, not "Zurich"). Index a
		// second, diacritic-folded key too when it differs and is fully
		// coverable by foldLatinDiacritics, so the everyday English
		// spelling finds it as well — same id, so it's always a distinct
		// bbolt key even when the folded string collides with another
		// place's name.
		if folded, ok := foldLatinDiacritics(p.name); ok {
			if normalized := normalizePlaceName(folded); normalized != normalizePlaceName(p.asciiName) {
				entries = append(entries, kv{key: placeKey(folded, p.id), blob: blob})
				foldedAliases++
			}
		}
	}
	fmt.Fprintf(os.Stderr, "%d diacritic-folded alias keys added\n", foldedAliases)
	// Ascending key order is bbolt's fast append-only path (see
	// hotspots.go's identical reasoning) — every key here is written
	// exactly once and never touched again.
	sort.Slice(entries, func(i, j int) bool { return string(entries[i].key) < string(entries[j].key) })

	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}
	boltPath := "data/places.bolt"
	os.Remove(boltPath)
	db, err := bolt.Open(boltPath, 0o644, nil)
	if err != nil {
		return fmt.Errorf("open %s: %w", boltPath, err)
	}
	defer db.Close()

	if err := db.Update(func(tx *bolt.Tx) error {
		nb, err := tx.CreateBucketIfNotExists([]byte(placeByNameBucket))
		if err != nil {
			return err
		}
		nb.FillPercent = 1.0
		for _, e := range entries {
			if err := nb.Put(e.key, e.blob); err != nil {
				return err
			}
		}
		mb, err := tx.CreateBucketIfNotExists([]byte(placeMetaBucket))
		if err != nil {
			return err
		}
		return mb.Put([]byte(placeCountMetaKey), appendUvarint(nil, uint64(len(entries))))
	}); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "wrote %s (%d places)\n", boltPath, len(entries))
	return nil
}

// placeKey is place_by_name's bucket key: the normalized ASCII name, a NUL
// separator, then the GeoNames id (big-endian, so byte order matches
// numeric order — not that it matters for uniqueness, only that it's
// fixed-width). The separator keeps a short name from ever reading as a
// byte-for-byte prefix of an unrelated longer one that happens to start the
// same way; the id keeps entries that share a name (there are many "San
// Jose"s worldwide) each their own key. Must match server/places_data.go's
// decoding/Search exactly.
func placeKey(asciiName string, id uint64) []byte {
	buf := []byte(normalizePlaceName(asciiName))
	buf = append(buf, 0)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], id)
	return append(buf, idBuf[:]...)
}

// latinDiacriticFold maps a common accented Latin letter to its plain-ASCII
// base, covering the everyday "strip the accent" reading of a name — as
// opposed to GeoNames' own asciiname column, which transliterates by each
// language's own convention (ü -> "ue" in German place names, not "u").
// Deliberately not exhaustive: anything outside this table makes
// foldLatinDiacritics bail rather than guess.
var latinDiacriticFold = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'ā': 'a', 'ă': 'a', 'ą': 'a',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e', 'ĕ': 'e', 'ė': 'e', 'ę': 'e', 'ě': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i', 'ī': 'i', 'ĭ': 'i', 'į': 'i',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o', 'ō': 'o', 'ŏ': 'o', 'ő': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ū': 'u', 'ŭ': 'u', 'ů': 'u', 'ű': 'u', 'ų': 'u',
	'ñ': 'n', 'ń': 'n', 'ņ': 'n', 'ň': 'n',
	'ç': 'c', 'ć': 'c', 'ĉ': 'c', 'č': 'c',
	'ý': 'y', 'ÿ': 'y',
	'ž': 'z', 'ź': 'z', 'ż': 'z',
	'š': 's', 'ś': 's', 'ş': 's',
	'ğ': 'g', 'ĝ': 'g',
	'ł': 'l', 'ĺ': 'l', 'ľ': 'l',
	'ř': 'r', 'ŕ': 'r',
	'ť': 't', 'ţ': 't',
	'đ': 'd', 'ď': 'd',
	'ß': 's',
}

// foldLatinDiacritics returns s with every accented letter replaced by its
// plain-ASCII base, and ok = true only if the result is now fully ASCII —
// a name with any character outside latinDiacriticFold's table (a
// different script entirely, or an accent this table doesn't know) isn't
// coverable, so the caller skips indexing a folded alias for it rather
// than index a string still full of non-Latin characters.
func foldLatinDiacritics(s string) (string, bool) {
	var b strings.Builder
	for _, r := range s {
		lower := unicode.ToLower(r)
		if folded, ok := latinDiacriticFold[lower]; ok {
			b.WriteRune(folded)
			continue
		}
		if r > unicode.MaxASCII {
			return "", false
		}
		b.WriteRune(lower)
	}
	return b.String(), true
}

// normalizePlaceName is the query-time and build-time name normalization —
// lowercased, trimmed. GeoNames' asciiname column already strips
// diacritics/non-Latin scripts down to plain ASCII (e.g. "Zürich" ->
// "Zurich"), so nothing fancier is needed for "zurich" to find it. Must
// match server/places_data.go's normalizePlaceName exactly.
func normalizePlaceName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// encodePlaceValue is place_by_name's value format: lat, lng, population,
// a fixed 2-byte country code, then the original (possibly non-ASCII)
// display name to the end of the buffer. Must exactly match
// server/places_data.go's decodePlace.
func encodePlaceValue(lat, lng float64, population int, countryCode, displayName string) []byte {
	buf := make([]byte, 0, 16+binary.MaxVarintLen64+2+len(displayName))
	buf = appendFloat64(buf, lat)
	buf = appendFloat64(buf, lng)
	buf = appendUvarint(buf, uint64(population))
	buf = append(buf, countryCode...)
	buf = append(buf, displayName...)
	return buf
}
