package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleHotspotCredits(t *testing.T) {
	cache, err := NewImageCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := newFakeSpeciesSource()
	src.set("50.1,4.2", "en", []string{"gretit1", "eurrob1", "nophoto"}, map[string]Taxon{
		"gretit1": {SciName: "Parus major", ComName: "Great Tit"},
		"eurrob1": {SciName: "Erithacus rubecula", ComName: "European Robin"},
		"nophoto": {SciName: "Certhia brachydactyla", ComName: "Short-toed Treecreeper"},
	})
	if err := cache.writeMetadata("Parus major", []*ImageInfo{{
		Title: "Great tit.jpg", Author: "Jane Doe", License: "CC BY-SA 4.0",
		LicenseURL: "https://creativecommons.org/licenses/by-sa/4.0", SourceURL: "https://commons.wikimedia.org/wiki/File:Great_tit.jpg",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.writeMetadata("Erithacus rubecula", []*ImageInfo{{Title: "Robin.jpg", Author: "A"}, {Title: "Robin2.jpg", Author: "B"}}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{cache: cache, species: src}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hotspots/{locId}/credits", srv.handleHotspotCredits)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/hotspots/50.1,4.2/credits?lang=en", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got []SpeciesCredits
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d species, want 2 (species without a photo are omitted): %+v", len(got), got)
	}
	if got[0].ComName != "European Robin" || got[1].ComName != "Great Tit" {
		t.Errorf("not sorted by common name: %s, %s", got[0].ComName, got[1].ComName)
	}
	if len(got[0].Images) != 2 {
		t.Errorf("robin has %d credits, want 2", len(got[0].Images))
	}
	c := got[1].Images[0]
	if c.Author != "Jane Doe" || c.License != "CC BY-SA 4.0" || c.SourceURL == "" || c.LicenseURL == "" {
		t.Errorf("credit = %+v", c)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/hotspots/50.1,4.2/credits?lang=en&species="+got[0].SpeciesCode, nil))
	var one []SpeciesCredits
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].SpeciesCode != got[0].SpeciesCode {
		t.Errorf("species filter returned %+v, want only %s", one, got[0].SpeciesCode)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/hotspots/bogus/credits", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid locId: status %d, want 400", rec.Code)
	}
}
