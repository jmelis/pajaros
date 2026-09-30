package main

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func sideOf(entries map[string]int, taxa map[string]Taxon) compareSide {
	codes := make([]string, 0, len(entries))
	counts := map[string]int{}
	for code, n := range entries {
		codes = append(codes, code)
		counts[taxa[code].SciName] = n
	}
	return compareSide{codes: codes, taxa: taxa, counts: counts}
}

func TestBuildComparison(t *testing.T) {
	taxa := map[string]Taxon{
		"mallar": {SciName: "Anas platyrhynchos", ComName: "Mallard"},
		"eurcoo": {SciName: "Fulica atra", ComName: "Eurasian Coot"},
		"grecre": {SciName: "Podiceps cristatus", ComName: "Great Crested Grebe"},
		"eurnut": {SciName: "Sitta europaea", ComName: "Eurasian Nuthatch"},
	}
	// A has twice the data of B, so equal counts in B mean twice the share.
	a := sideOf(map[string]int{"mallar": 600, "eurcoo": 300, "grecre": 100}, taxa)
	b := sideOf(map[string]int{"mallar": 100, "eurcoo": 100, "eurnut": 100}, taxa)

	res := buildComparison(a, b, "en")

	if res.A.Observations != 1000 || res.B.Observations != 300 {
		t.Fatalf("observations = %d/%d, want 1000/300", res.A.Observations, res.B.Observations)
	}
	if res.Shared != 2 || res.OnlyA != 1 || res.OnlyB != 1 {
		t.Fatalf("shared/onlyA/onlyB = %d/%d/%d, want 2/1/1", res.Shared, res.OnlyA, res.OnlyB)
	}
	if len(res.Species) != 4 {
		t.Fatalf("len(species) = %d, want 4", len(res.Species))
	}
	// Most observed combined first: Mallard (700).
	if res.Species[0].Code != "mallar" {
		t.Errorf("first species = %s, want mallar", res.Species[0].Code)
	}
	by := map[string]compareSpecies{}
	for _, sp := range res.Species {
		by[sp.Code] = sp
	}

	mallard := by["mallar"]
	// B ties at 100 each; ties break by common name: Eurasian Coot, Eurasian Nuthatch, Mallard.
	if mallard.RankA != 1 || mallard.RankB != 3 {
		t.Errorf("mallard ranks = %d/%d, want 1/3", mallard.RankA, mallard.RankB)
	}
	if mallard.LogRatio == nil {
		t.Fatal("mallard LogRatio is nil, want a value")
	}
	// shareA 0.6, shareB 1/3 -> log2(1.8)
	if want := math.Log2(0.6 / (100.0 / 300.0)); math.Abs(*mallard.LogRatio-want) > 1e-9 {
		t.Errorf("mallard LogRatio = %v, want %v", *mallard.LogRatio, want)
	}
	if !mallard.Reliable {
		t.Error("mallard should be reliable (combined 700)")
	}

	grebe := by["grecre"]
	if grebe.RankB != 0 || grebe.CountB != 0 || grebe.LogRatio != nil || grebe.Reliable {
		t.Errorf("grebe (only at A) = %+v, want zero B side, no ratio", grebe)
	}
	nuthatch := by["eurnut"]
	if nuthatch.RankA != 0 || nuthatch.RankB == 0 {
		t.Errorf("nuthatch ranks = %d/%d, want 0/>0", nuthatch.RankA, nuthatch.RankB)
	}
}

func TestBuildComparisonLowSampleNotReliable(t *testing.T) {
	taxa := map[string]Taxon{"x": {SciName: "X x", ComName: "X"}}
	a := sideOf(map[string]int{"x": 5}, taxa)
	b := sideOf(map[string]int{"x": 7}, taxa)
	res := buildComparison(a, b, "en")
	if len(res.Species) != 1 || res.Species[0].Reliable {
		t.Fatalf("species = %+v, want one not-reliable species (combined 12 < %d)", res.Species, compareMinReliable)
	}
	if res.Species[0].LogRatio == nil {
		t.Error("LogRatio should still be set for low-sample shared species")
	}
}

func TestBuildComparisonEmptySide(t *testing.T) {
	taxa := map[string]Taxon{"x": {SciName: "X x", ComName: "X"}}
	res := buildComparison(sideOf(map[string]int{"x": 5}, taxa), compareSide{}, "en")
	if res.Shared != 0 || res.OnlyA != 1 || res.OnlyB != 0 || res.B.Observations != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestHandleCompareRejectsBadInput(t *testing.T) {
	srv := &Server{species: newFakeSpeciesSource()}
	for _, tc := range []struct{ name, target string }{
		{"missing", "/api/compare"},
		{"invalid a", "/api/compare?a=nope&b=50.8,4.4"},
		{"invalid b", "/api/compare?a=50.8,4.4&b=../x"},
		{"same", "/api/compare?a=50.8,4.4&b=50.8,4.4"},
	} {
		rr := httptest.NewRecorder()
		srv.handleCompare(rr, httptest.NewRequest(http.MethodGet, tc.target, nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, rr.Code)
		}
	}
}
