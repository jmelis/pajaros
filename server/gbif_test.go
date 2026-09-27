package main

import "testing"

func TestSortByPopularityOrdersCountThenNameThenMissingLast(t *testing.T) {
	type item struct {
		name  string
		count int
		has   bool
	}
	items := []item{
		{"Zebra Finch", 0, false}, // no count data: sorts last
		{"Common Blackbird", 5, true},
		{"Mirlo común", 100, true},
		{"Aaa Bird", 5, true},
	}
	sortByPopularity(items,
		func(i item) (int, bool) { return i.count, i.has },
		func(i item) string { return i.name },
	)
	want := []string{"Mirlo común", "Aaa Bird", "Common Blackbird", "Zebra Finch"}
	for i := range want {
		if items[i].name != want[i] {
			t.Fatalf("sorted = %v, want %v", items, want)
		}
	}
}

func TestBinomialName(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantOK   bool
	}{
		{"Columba palumbus Linnaeus, 1758", "Columba palumbus", true},
		{"Psittacula krameri (Scopoli, 1769)", "Psittacula krameri", true},
		{"Cyanistes caeruleus (Linnaeus, 1758)", "Cyanistes caeruleus", true},
		{"Turdus merula", "Turdus merula", true},       // no suffix at all
		{"Larus argentatus × Larus fuscus", "", false}, // real hybrid marker (×, U+00D7)
		{"Anas sp.", "", false},                        // unidentified sp.
		{"Passer cf. domesticus", "", false},           // cf.
		{"Corvus aff. corone", "", false},              // aff.
		{"parus major", "", false},                     // lowercase genus, should reject
		{"Parus", "", false},                           // single word, no species epithet
	}
	for _, c := range cases {
		got, ok := binomialName(c.in)
		if ok != c.wantOK || (ok && got != c.wantName) {
			t.Errorf("binomialName(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.wantName, c.wantOK)
		}
	}
}
