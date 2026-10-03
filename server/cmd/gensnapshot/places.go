package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/jmelis/pajaros/server/internal/areas"
	"github.com/jmelis/pajaros/server/internal/placekey"

	bolt "go.etcd.io/bbolt"
)

// Bucket names in places.bolt. See server/places_data.go, which reads both.
const (
	placeByNameBucket = "place_by_name"
	placeByIDBucket   = "place_by_id"
	placeShapesBucket = "place_shapes"
	placeByCellBucket = "place_by_cell"
	placeMetaBucket   = "place_meta"
	placeCountMetaKey = "count"
)

// Default ranking populations for places whose OSM entry has no population
// tag; search results are sorted by population, so these put a country above
// a region above a town above a village, and parks and deserts in between.
func rankPop(c *candidate) int {
	if c.Pop > 0 && c.Kind != kindCountry {
		return c.Pop
	}
	switch c.Kind {
	case kindCountry:
		return 1_500_000_000
	case kindRegion:
		if c.Level <= 4 {
			return 1_000_000
		}
		return 200_000
	case kindCity:
		switch {
		case c.Level >= 9:
			return 1_000
		case c.Level == 8:
			return 3_000
		}
		switch c.Place {
		case "city":
			return 100_000
		case "town":
			return 5_000
		}
		return 500
	}
	switch c.Place {
	case "desert", "island", "archipelago", "region":
		return 400_000
	case "park":
		return 50_000
	}
	return 150_000
}

// placeTypes are the values of the type byte stored in each place; the first
// four are the kinds, the rest refine them for display. Must match
// server/places_data.go.
var placeTypes = [...]string{"city", "country", "region", "natural", "park", "reserve", "island", "desert", "town", "village", "municipality", "district"}

func placeType(c *candidate) byte {
	name := ""
	switch c.Kind {
	case kindCity:
		switch {
		case c.Level >= 9:
			name = "district"
		case c.Level == 8:
			name = "municipality"
		case c.Place == "town" || c.Place == "village":
			name = c.Place
		default:
			name = "city"
		}
	case kindNatural:
		switch c.Place {
		case "park", "reserve", "island", "desert":
			name = c.Place
		case "archipelago":
			name = "island"
		default:
			name = "natural"
		}
	default:
		return byte(c.Kind)
	}
	for i, n := range placeTypes {
		if n == name {
			return byte(i)
		}
	}
	return byte(c.Kind)
}

// runPlaces merges the candidate files that `gensnapshot osm` wrote (one per
// OSM extract) into out, a places.bolt file: a bbolt store with three buckets
//   - place_by_name: normalized-name key -> encoded place, ordered so a prefix
//     scan is a name search (one key per name and alias).
//   - place_by_id: place id -> the same encoded place.
//   - place_shapes: place id -> simplified polygon, for places with an extent.
//   - place_meta: the total key count.
//
// Everything fits in memory, so this reads all candidates, merges them, sorts
// the keys and writes once.
func runPlaces(out string, files []string) error {
	byID := map[uint64]*candidate{}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<20))
		for {
			var c candidate
			if err := dec.Decode(&c); err == io.EOF {
				break
			} else if err != nil {
				f.Close()
				return fmt.Errorf("%s: %w", path, err)
			}
			if len(c.Names) == 0 {
				continue
			}
			// The same boundary can come from several extracts, each
			// clipped differently: keep the most complete one.
			if prev, dup := byID[c.ID]; !dup || (len(c.Rings) > 0 && polygonAreaKm2(c.Rings) > polygonAreaKm2(prev.Rings)) {
				cc := c
				byID[c.ID] = &cc
			}
		}
		f.Close()
	}
	all := make([]*candidate, 0, len(byID))
	for _, c := range byID {
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	fmt.Fprintf(os.Stderr, "%d candidates merged from %d files\n", len(all), len(files))

	// Countries, one per ISO code (the biggest outline wins).
	best := map[string]*candidate{}
	for _, c := range all {
		// A few territories share their sovereign's code; the most populous
		// feature owns it.
		if c.Kind == kindCountry && c.CC != "" && (best[c.CC] == nil || c.Pop > best[c.CC].Pop) {
			best[c.CC] = c
		}
	}
	var countryList []countryInfo
	countries := map[string]countryInfo{}
	for cc, c := range best {
		name := c.En
		if name == "" {
			name = c.Names[0]
		}
		ci := countryInfo{cc: cc, name: name, shape: areas.NewShape(c.Rings)}
		countries[cc] = ci
		countryList = append(countryList, ci)
	}
	sort.Slice(countryList, func(i, j int) bool { return countryList[i].cc < countryList[j].cc })

	// A named point inside a polygon of the same name is that polygon
	// (Madrid the city node and Madrid the municipality): keep the polygon,
	// with the larger population.
	polygonsByName := map[string][]*candidate{}
	for _, c := range all {
		if len(c.Rings) == 0 || c.Kind == kindCountry {
			continue
		}
		for _, n := range c.Names {
			k := placekey.Normalize(n)
			polygonsByName[k] = append(polygonsByName[k], c)
		}
	}
	shapeOf := map[*candidate]*areas.Shape{}
	contains := func(poly *candidate, lat, lng float64) bool {
		sh := shapeOf[poly]
		if sh == nil {
			sh = areas.NewShape(poly.Rings)
			shapeOf[poly] = sh
		}
		return sh.Contains(lat, lng)
	}
	var keep []*candidate
	merged := 0
	for _, c := range all {
		if len(c.Rings) > 0 {
			keep = append(keep, c)
			continue
		}
		dup := false
		for _, n := range c.Names {
			for _, poly := range polygonsByName[placekey.Normalize(n)] {
				if contains(poly, c.Lat, c.Lng) {
					if c.Pop > poly.Pop {
						poly.Pop = c.Pop
					}
					dup = true
					break
				}
			}
			if dup {
				break
			}
		}
		if dup {
			merged++
			continue
		}
		keep = append(keep, c)
	}
	fmt.Fprintf(os.Stderr, "%d points folded into same-named polygons, %d places kept\n", merged, len(keep))

	// Twin polygons: the same boundary mapped twice under one name (a province
	// that is also an autonomous community, an island and its municipality).
	// Keep the lower admin level, else the larger area.
	type twinKey struct {
		kind int
		name string
	}
	groups := map[twinKey][]*candidate{}
	for _, c := range keep {
		if len(c.Rings) > 0 && c.Kind != kindCountry {
			k := twinKey{c.Kind, placekey.Normalize(c.Names[0])}
			groups[k] = append(groups[k], c)
		}
	}
	areaOf := map[*candidate]float64{}
	dropped := map[*candidate]bool{}
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		for _, c := range g {
			areaOf[c] = polygonAreaKm2(c.Rings)
		}
		for i := range g {
			for j := i + 1; j < len(g); j++ {
				a, b := g[i], g[j]
				if dropped[a] || dropped[b] {
					continue
				}
				if r := areaOf[a] / areaOf[b]; r < 0.7 || r > 1/0.7 {
					continue
				}
				if !contains(a, b.Lat, b.Lng) || !contains(b, a.Lat, a.Lng) {
					continue
				}
				win, lose := a, b
				switch {
				case a.Level != b.Level:
					if (b.Level != 0 && b.Level < a.Level) || a.Level == 0 {
						win, lose = b, a
					}
				case areaOf[b] > areaOf[a]:
					win, lose = b, a
				}
				if lose.Pop > win.Pop {
					win.Pop = lose.Pop
				}
				dropped[lose] = true
			}
		}
	}
	if len(dropped) > 0 {
		kept := keep[:0]
		for _, c := range keep {
			if !dropped[c] {
				kept = append(kept, c)
			}
		}
		keep = kept
		fmt.Fprintf(os.Stderr, "%d twin polygons dropped\n", len(dropped))
	}

	type kv struct{ key, blob []byte }
	var entries, shapes, byIDRows []kv
	perKind := map[int]int{}
	cells := map[uint32][]byte{}
	for _, c := range keep {
		switch {
		case len(c.Rings) == 0 && c.Kind == kindCity:
			addToCells(cells, cellClassLocality, c.ID, c.Lat, c.Lat, c.Lng, c.Lng)
		case len(c.Rings) > 0 && (c.Kind == kindRegion || c.Kind == kindCountry || (c.Kind == kindCity && c.Level == 8)):
			class := cellClassMunicipality
			switch c.Kind {
			case kindCountry:
				class = cellClassCountry
			case kindRegion:
				class = cellClassRegionBase + min(max(c.Level, 3), 7)
				if c.Level == 0 {
					class = cellClassRegionBase + 7
				}
			}
			minLat, maxLat, minLng, maxLng := ringsBBox(c.Rings)
			addToCells(cells, class, c.ID, minLat, maxLat, minLng, maxLng)
		}
		var cc, countryName string
		lat, lng := c.Lat, c.Lng
		radius := 0.0
		switch {
		case len(c.Rings) > 0:
			lat, lng, radius = centreRadius(c.Rings)
			if c.Kind != kindCountry {
				cc, countryName = containingCountryOf(c.Rings, lat, lng, countryList)
			}
		default:
			radius = cityRadiusKm(rankPop(c))
			cc, countryName = containingCountryOfPoint(lat, lng, countryList)
		}
		if c.Kind == kindCountry {
			cc = ""
		}
		blob := encodePlaceValue(lat, lng, rankPop(c), cc, placeType(c), radius, countryName, c.Names[0])
		for _, n := range c.Names {
			for _, key := range searchKeys(n, c.Kind) {
				entries = append(entries, kv{key: placeKey(key, c.ID), blob: blob})
			}
		}
		var idKey [8]byte
		binary.BigEndian.PutUint64(idKey[:], c.ID)
		byIDRows = append(byIDRows, kv{key: idKey[:], blob: blob})
		if len(c.Rings) > 0 {
			var idBuf [8]byte
			binary.BigEndian.PutUint64(idBuf[:], c.ID)
			shapes = append(shapes, kv{key: idBuf[:], blob: encodeShape(c.Rings)})
		}
		perKind[c.Kind]++
	}
	fmt.Fprintf(os.Stderr, "cities %d, countries %d, regions %d, natural %d; %d shapes\n",
		perKind[kindCity], perKind[kindCountry], perKind[kindRegion], perKind[kindNatural], len(shapes))
	sort.Slice(shapes, func(i, j int) bool { return string(shapes[i].key) < string(shapes[j].key) })
	sort.Slice(byIDRows, func(i, j int) bool { return string(byIDRows[i].key) < string(byIDRows[j].key) })
	// Ascending key order is bbolt's fast append-only path: every key is
	// written exactly once and never touched again.
	sort.Slice(entries, func(i, j int) bool { return string(entries[i].key) < string(entries[j].key) })

	os.Remove(out)
	db, err := bolt.Open(out, 0o644, nil)
	if err != nil {
		return fmt.Errorf("open %s: %w", out, err)
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
		ib, err := tx.CreateBucketIfNotExists([]byte(placeByIDBucket))
		if err != nil {
			return err
		}
		ib.FillPercent = 1.0
		for _, e := range byIDRows {
			if err := ib.Put(e.key, e.blob); err != nil {
				return err
			}
		}
		sb, err := tx.CreateBucketIfNotExists([]byte(placeShapesBucket))
		if err != nil {
			return err
		}
		sb.FillPercent = 1.0
		for _, e := range shapes {
			if err := sb.Put(e.key, e.blob); err != nil {
				return err
			}
		}
		cb, err := tx.CreateBucketIfNotExists([]byte(placeByCellBucket))
		if err != nil {
			return err
		}
		cb.FillPercent = 1.0
		cellKeys := make([]uint32, 0, len(cells))
		for k := range cells {
			cellKeys = append(cellKeys, k)
		}
		sort.Slice(cellKeys, func(i, j int) bool { return cellKeys[i] < cellKeys[j] })
		for _, k := range cellKeys {
			var key [4]byte
			binary.BigEndian.PutUint32(key[:], k)
			if err := cb.Put(key[:], cells[k]); err != nil {
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
	fmt.Fprintf(os.Stderr, "wrote %s (%d keys)\n", out, len(entries))
	return nil
}

// searchKeyStop are words too generic to start a search on.
var searchKeyStop = map[string]bool{
	"parque": true, "natural": true, "nacional": true, "regional": true, "reserva": true, "park": true,
	"national": true, "nature": true, "reserve": true, "parc": true, "parco": true, "paraje": true,
	"zona": true, "area": true,
}

// searchKeys returns the strings a place is searchable under: its name, and
// for region and natural places also each later word onwards, so "Doñana"
// finds "Parque Nacional de Doñana". Short words and generic ones
// ("Parque", "de") are skipped as starting points.
func searchKeys(name string, kind int) []string {
	keys := []string{name}
	if kind != kindNatural && kind != kindRegion {
		return keys
	}
	words := strings.Fields(name)
	for i := 1; i < len(words) && i <= 5; i++ {
		w := strings.ToLower(words[i])
		if len([]rune(w)) <= 3 || searchKeyStop[w] {
			continue
		}
		keys = append(keys, strings.Join(words[i:], " "))
	}
	return keys
}

// containingCountryOfPoint returns the country holding the point, trying a
// few km around it when it falls just off a coastline.
func containingCountryOfPoint(lat, lng float64, countries []countryInfo) (string, string) {
	const near = 0.05
	for _, d := range [][2]float64{{0, 0}, {near, 0}, {-near, 0}, {0, near}, {0, -near}} {
		for _, c := range countries {
			if c.shape.Contains(lat+d[1], lng+d[0]) {
				return c.cc, c.name
			}
		}
	}
	return "", ""
}

// containingCountryOf labels a polygon: small ones by their centre, large
// ones (which may span several countries) by sampled vertices.
func containingCountryOf(rings [][][2]float64, lat, lng float64, countries []countryInfo) (string, string) {
	if bboxSpan(rings) < 0.5 {
		return containingCountryOfPoint(lat, lng, countries)
	}
	return containingCountry(rings, countries)
}

// placeKey is place_by_name's bucket key: the normalized ASCII name, a NUL
// separator, then the place id (big-endian, so byte order matches
// numeric order — not that it matters for uniqueness, only that it's
// fixed-width). The separator keeps a short name from ever reading as a
// byte-for-byte prefix of an unrelated longer one that happens to start the
// same way; the id keeps entries that share a name (there are many "San
// Jose"s worldwide) each their own key. Must match server/places_data.go's
// decoding/Search exactly.
func placeKey(asciiName string, id uint64) []byte {
	buf := []byte(placekey.Normalize(asciiName))
	buf = append(buf, 0)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], id)
	return append(buf, idBuf[:]...)
}

// cityRadiusKm is the default search radius around a city, town or village, which
// has a point but no extent: it grows with the square root of the population
// (a town of 1,000 gets the 2 km floor, Madrid about 17 km) and is capped.
func cityRadiusKm(population int) float64 {
	return math.Max(2, math.Min(25, 0.3*math.Sqrt(float64(population)/1000)))
}

// encodePlaceValue is place_by_name's value format: lat, lng, population,
// a fixed 2-byte country code (blank "  " when the place has none), a type
// byte (index into placeTypes), the default radius in 100 m units (uvarint), the containing
// country's English name (length byte + bytes, empty for countries and
// places that span several), then the display name to the end of the
// buffer. Must exactly match server/places_data.go's decodePlace.
func encodePlaceValue(lat, lng float64, population int, countryCode string, kind byte, radiusKm float64, country, displayName string) []byte {
	buf := make([]byte, 0, 16+2*binary.MaxVarintLen64+4+len(country)+len(displayName))
	buf = appendFloat64(buf, lat)
	buf = appendFloat64(buf, lng)
	buf = appendUvarint(buf, uint64(population))
	if len(countryCode) != 2 {
		countryCode = "  "
	}
	buf = append(buf, countryCode...)
	buf = append(buf, kind)
	buf = appendUvarint(buf, uint64(math.Round(radiusKm*10)))
	buf = append(buf, byte(len(country)))
	buf = append(buf, country...)
	buf = append(buf, displayName...)
	return buf
}

// place_by_cell lets the server describe an arbitrary point ("near Ames, in
// Galicia, Spain") without scanning every place. The world is cut into
// cellsPerDeg x cellsPerDeg-degree cells; a cell's key is its row and column
// (uint16 each, big-endian) and its value lists, as uvarints of id<<4|class,
// the localities (class 0) inside it and the country, municipality and region
// polygons whose bounding box overlaps it. A region's class is
// cellClassRegionBase plus its OSM admin level (3 to 7). Must match
// server/places_data.go.
const (
	cellsPerDeg           = 2
	cellClassLocality     = 0
	cellClassCountry      = 1
	cellClassMunicipality = 2
	cellClassRegionBase   = 8
)

func cellIndex(v, origin float64) int {
	i := int(math.Floor((v + origin) * cellsPerDeg))
	return min(max(i, 0), int(origin*2*cellsPerDeg)-1)
}

func addToCells(cells map[uint32][]byte, class int, id uint64, minLat, maxLat, minLng, maxLng float64) {
	entry := appendUvarint(nil, id<<4|uint64(class))
	for i := cellIndex(minLat, 90); i <= cellIndex(maxLat, 90); i++ {
		for j := cellIndex(minLng, 180); j <= cellIndex(maxLng, 180); j++ {
			k := uint32(i)<<16 | uint32(j)
			cells[k] = append(cells[k], entry...)
		}
	}
}

func ringsBBox(rings [][][2]float64) (minLat, maxLat, minLng, maxLng float64) {
	minLat, maxLat, minLng, maxLng = math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, r := range rings {
		for _, p := range r {
			minLng, maxLng = math.Min(minLng, p[0]), math.Max(maxLng, p[0])
			minLat, maxLat = math.Min(minLat, p[1]), math.Max(maxLat, p[1])
		}
	}
	return
}
