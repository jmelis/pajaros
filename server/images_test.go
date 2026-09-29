package main

import "testing"

func TestMergeImageSourcesDedupsBySourceURL(t *testing.T) {
	base := []*ImageInfo{{SourceURL: "a"}, {SourceURL: "b"}}
	extra := []*ImageInfo{{SourceURL: "b"}, {SourceURL: "c"}}

	got := mergeImageSources(base, extra)

	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].SourceURL != w {
			t.Errorf("got[%d].SourceURL = %q, want %q", i, got[i].SourceURL, w)
		}
	}
}

func TestMergeImageSourcesCapsAtMax(t *testing.T) {
	base := []*ImageInfo{{SourceURL: "a"}, {SourceURL: "b"}, {SourceURL: "c"}, {SourceURL: "d"}}
	extra := []*ImageInfo{{SourceURL: "e"}}

	got := mergeImageSources(base, extra)

	if len(got) != maxImagesPerSpecies {
		t.Fatalf("len(got) = %d, want %d", len(got), maxImagesPerSpecies)
	}
	for _, info := range got {
		if info.SourceURL == "e" {
			t.Errorf("got %q, expected the cap to be hit before extra was appended", info.SourceURL)
		}
	}
}

func TestMergeImageSourcesEmptyBase(t *testing.T) {
	extra := []*ImageInfo{{SourceURL: "a"}, {SourceURL: "b"}}

	got := mergeImageSources(nil, extra)

	if len(got) != 2 || got[0].SourceURL != "a" || got[1].SourceURL != "b" {
		t.Fatalf("got %+v", got)
	}
}
