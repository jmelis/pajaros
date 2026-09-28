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
