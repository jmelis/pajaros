package main

import (
	"testing"

	"github.com/jmelis/pajaros/server/internal/seasonal"
)

func TestParseAreaKey(t *testing.T) {
	valid := map[string]string{
		"p12345":             "p12345",
		"c40.416,-3.703,5.0": "c40.416,-3.703,5.0",
		"c40.4163,-3.7034,5": "c40.416,-3.703,5.0",
		"c0,0,1":             "c0.000,0.000,1.0",
		"c-33.9,151.2,500":   "c-33.900,151.200,500.0",
		"c40.416,-3.703,0.5": "", // below the minimum radius
	}
	for in, want := range valid {
		ref, ok := parseAreaKey(in)
		if want == "" {
			if ok {
				t.Errorf("parseAreaKey(%q) accepted", in)
			}
			continue
		}
		if !ok || ref.key() != want {
			t.Errorf("parseAreaKey(%q) = %q, %v; want %q", in, ref.key(), ok, want)
		}
	}
	for _, bad := range []string{"", "p", "p0", "p01", "p-1", "px", "c", "c1,2", "c91,0,5", "c0,181,5", "c0,0,501", "c0,0,NaN", "c0,0,Inf", "q1", "p1,2", "c1,2,3,4"} {
		if _, ok := parseAreaKey(bad); ok {
			t.Errorf("parseAreaKey(%q) accepted", bad)
		}
	}
}

func TestValidAreaKeyRequiresCanonicalForm(t *testing.T) {
	if !validAreaKey("c40.416,-3.703,5.0") || !validAreaKey("p9") {
		t.Error("canonical keys rejected")
	}
	if validAreaKey("c40.4163,-3.703,5.0") || validAreaKey("c40.416,-3.703,5") {
		t.Error("non-canonical custom key accepted")
	}
}

func TestInfoForCustomArea(t *testing.T) {
	r := &AreaResolver{places: buildTestPlaceStore(t, nil)}
	info, err := r.Info("c40.416,-3.703,5.0")
	if err != nil || info.Lat != 40.416 || info.Lng != -3.703 || info.RadiusKm != 5 || info.Name != "" {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	if _, err := r.Info("nope"); err != errUnknownArea {
		t.Errorf("bad key err = %v", err)
	}
}

func TestEntriesCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newEntriesCache(2)
	e := func(id uint16) []seasonal.Entry { return []seasonal.Entry{{ID: id}} }
	c.put("a", e(1))
	c.put("b", e(2))
	c.get("a") // b is now the oldest
	c.put("c", e(3))
	if _, ok := c.get("b"); ok {
		t.Error("b should have been evicted")
	}
	if got, ok := c.get("a"); !ok || got[0].ID != 1 {
		t.Error("a was evicted")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("c missing")
	}
}
