package main

import (
	"container/list"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/jmelis/pajaros/server/internal/areas"
	"github.com/jmelis/pajaros/server/internal/seasonal"
)

// An area key names the region whose species are listed: "p<id>" is a place
// from places.bolt (its polygon when it has one, otherwise a circle of the
// place's default radius around its centre), and "c<lat>,<lng>,<km>" is a
// custom circle. Keys are what the API, the favorites and the URLs carry.
const (
	minCustomRadiusKm = 1.0
	maxCustomRadiusKm = 500.0
)

// areaRef is a parsed area key.
type areaRef struct {
	placeID          uint64
	custom           bool
	lat, lng, radius float64
}

// parseAreaKey reads an area key; ok is false for anything malformed or out of
// range. Custom coordinates are rounded to 3 decimals (about 100 m) and the
// radius to 0.1 km, so equal areas share one key (and one cache entry).
func parseAreaKey(key string) (areaRef, bool) {
	switch {
	case strings.HasPrefix(key, "p"):
		id, err := strconv.ParseUint(key[1:], 10, 64)
		if err != nil || id >= 1<<53 || key[1] == '0' {
			return areaRef{}, false
		}
		return areaRef{placeID: id}, true
	case strings.HasPrefix(key, "c"):
		parts := strings.Split(key[1:], ",")
		if len(parts) != 3 {
			return areaRef{}, false
		}
		var v [3]float64
		for i, p := range parts {
			f, err := strconv.ParseFloat(p, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return areaRef{}, false
			}
			v[i] = f
		}
		if v[0] < -90 || v[0] > 90 || v[1] < -180 || v[1] > 180 || v[2] < minCustomRadiusKm || v[2] > maxCustomRadiusKm {
			return areaRef{}, false
		}
		r := areaRef{custom: true, lat: round(v[0], 3), lng: round(v[1], 3), radius: round(v[2], 1)}
		if r.radius < minCustomRadiusKm {
			r.radius = minCustomRadiusKm
		}
		return r, true
	}
	return areaRef{}, false
}

func round(f float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(f*p) / p
}

// key is the canonical form of the reference.
func (a areaRef) key() string {
	if a.custom {
		return fmt.Sprintf("c%s,%s,%s", trimFloat(a.lat, 3), trimFloat(a.lng, 3), trimFloat(a.radius, 1))
	}
	return "p" + strconv.FormatUint(a.placeID, 10)
}

func trimFloat(f float64, decimals int) string {
	return strconv.FormatFloat(f, 'f', decimals, 64)
}

func validAreaKey(key string) bool {
	a, ok := parseAreaKey(key)
	return ok && a.key() == key
}

// AreaInfo describes an area: for a place, its search entry; for a custom
// circle (Kind empty), its centre and radius, with Name the nearest locality
// and Region and Country the ones containing the centre.
type AreaInfo struct {
	Key         string  `json:"key"`
	Name        string  `json:"name,omitempty"`
	Kind        string  `json:"kind,omitempty"`
	Region      string  `json:"region,omitempty"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"countryCode,omitempty"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	RadiusKm    float64 `json:"radiusKm"`
	Polygon     bool    `json:"polygon,omitempty"`
}

var errUnknownArea = errors.New("unknown area")

// AreaResolver turns area keys into pooled seasonal species entries, reading
// the grid file and the places file. Pooling a large area reads thousands of
// cells, so results are kept in a small LRU: one request asks for the same
// area several times (species, popularity, seasonality).
type AreaResolver struct {
	areas  *areas.Store
	places *PlaceStore
	cache  *entriesCache
}

func NewAreaResolver(a *areas.Store, p *PlaceStore) *AreaResolver {
	return &AreaResolver{areas: a, places: p, cache: newEntriesCache(128)}
}

// Info describes the area, or reports errUnknownArea for a place that does not
// exist.
func (r *AreaResolver) Info(key string) (AreaInfo, error) {
	ref, ok := parseAreaKey(key)
	if !ok {
		return AreaInfo{}, errUnknownArea
	}
	if ref.custom {
		loc := r.places.Locate(ref.lat, ref.lng)
		return AreaInfo{
			Key: ref.key(), Name: loc.Near, Region: loc.Region, Country: loc.Country, CountryCode: loc.CountryCode,
			Lat: ref.lat, Lng: ref.lng, RadiusKm: ref.radius,
		}, nil
	}
	p, ok := r.places.ByID(ref.placeID)
	if !ok {
		return AreaInfo{}, errUnknownArea
	}
	_, hasShape := r.places.Shape(p.ID)
	return AreaInfo{
		Key: ref.key(), Name: p.Name, Kind: p.Kind, Country: p.Country, CountryCode: p.CountryCode,
		Lat: p.Lat, Lng: p.Lng, RadiusKm: p.RadiusKm, Polygon: hasShape,
	}, nil
}

// Entries returns the area's pooled per-month counts by stable species ID.
func (r *AreaResolver) Entries(key string) ([]seasonal.Entry, error) {
	ref, ok := parseAreaKey(key)
	if !ok {
		return nil, errUnknownArea
	}
	key = ref.key()
	if es, ok := r.cache.get(key); ok {
		return es, nil
	}
	defer bboltTimer("areas", "pool")()
	var res areas.Result
	var err error
	switch {
	case ref.custom:
		res, err = r.areas.Circle(ref.lat, ref.lng, ref.radius)
	default:
		p, found := r.places.ByID(ref.placeID)
		if !found {
			return nil, errUnknownArea
		}
		if sh, ok := r.places.Shape(p.ID); ok {
			res, err = r.areas.Shape(sh)
		} else {
			res, err = r.areas.Circle(p.Lat, p.Lng, math.Max(p.RadiusKm, minCustomRadiusKm))
		}
	}
	if err != nil {
		return nil, err
	}
	r.cache.put(key, res.Entries)
	return res.Entries, nil
}

// entriesCache is a mutex-guarded LRU of pooled entries by area key.
type entriesCache struct {
	mu    sync.Mutex
	max   int
	order *list.List // front = most recent
	items map[string]*list.Element
}

type cacheItem struct {
	key     string
	entries []seasonal.Entry
}

func newEntriesCache(max int) *entriesCache {
	return &entriesCache{max: max, order: list.New(), items: map[string]*list.Element{}}
}

func (c *entriesCache) get(key string) ([]seasonal.Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*cacheItem).entries, true
}

func (c *entriesCache) put(key string, es []seasonal.Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*cacheItem).entries = es
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&cacheItem{key, es})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*cacheItem).key)
	}
}
