package main

import (
	"net"
	"testing"
)

func testGeoIPStore(t *testing.T) *GeoIPStore {
	t.Helper()
	g, err := loadGeoIPStore()
	if err != nil {
		t.Fatalf("loadGeoIPStore: %v", err)
	}
	if len(g.ranges) == 0 {
		t.Fatal("loadGeoIPStore: no ranges loaded")
	}
	return g
}

func TestGeoIPLangForIP(t *testing.T) {
	g := testGeoIPStore(t)

	tests := []struct {
		name   string
		ip     string
		want   string
		wantOK bool
	}{
		// Real ranges pulled from data/geoip_country.json.gz.
		{"spain ipv4", "1.178.22.10", "es", true},
		{"france ipv4", "1.178.90.5", "fr", true},
		{"spain ipv6", "2001:448::1", "es", true},
		// Reserved/documentation addresses never appear in the table.
		{"unrecorded ipv4", "192.0.2.1", "", false},
		{"unrecorded ipv6", "2001:db8::1", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lang, ok := g.LangForIP(net.ParseIP(tt.ip))
			if ok != tt.wantOK || lang != tt.want {
				t.Errorf("LangForIP(%s) = (%q, %v), want (%q, %v)", tt.ip, lang, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestGeoIPLangForIPNilSafe(t *testing.T) {
	var g *GeoIPStore
	if lang, ok := g.LangForIP(net.ParseIP("1.178.22.10")); ok || lang != "" {
		t.Errorf("nil store: LangForIP = (%q, %v), want (\"\", false)", lang, ok)
	}
}

func TestGeoIPLangForIPNilAddr(t *testing.T) {
	g := testGeoIPStore(t)
	if lang, ok := g.LangForIP(nil); ok || lang != "" {
		t.Errorf("nil ip: LangForIP = (%q, %v), want (\"\", false)", lang, ok)
	}
}
