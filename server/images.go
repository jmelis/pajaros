package main

// Multi-source image resolution: a species' Learn card shows up to
// maxImagesPerSpecies photos, sourced from Wikimedia Commons first (see
// wikimedia.go) and topped up from iNaturalist (see inaturalist.go) when
// Commons doesn't have enough freely-licensed candidates on its own —
// verified during design: Commons categories are thin for some species
// (e.g. only 2 files for Aegithalos caudatus's top-level category) where
// iNaturalist's observation-photo volume reliably fills the gap.

// maxImagesPerSpecies caps how many photos the cache keeps per species —
// the Learn card's image count. Wikimedia is preferred (generally
// better-curated single representative photos); iNaturalist, filtered to
// the same redistributable licenses, fills whatever Wikimedia doesn't
// supply.
const maxImagesPerSpecies = 4

// ResolveImages finds up to max freely-licensed candidate images for sciName,
// width pixels wide, trying Wikimedia Commons first and topping up from
// iNaturalist only if Commons falls short.
func ResolveImages(sciName string, width, max int) []*ImageInfo {
	out := commonsImages(sciName, width, max)
	if len(out) < max {
		out = mergeImageSources(out, inaturalistImages(sciName, max-len(out)))
	}
	return out
}

// mergeImageSources appends extra to base, skipping any image whose
// SourceURL already appears in base, and caps the result at
// maxImagesPerSpecies. Pure and side-effect free so it's unit-testable
// without a network call, unlike the rest of this file.
func mergeImageSources(base, extra []*ImageInfo) []*ImageInfo {
	seen := make(map[string]bool, len(base))
	for _, info := range base {
		seen[info.SourceURL] = true
	}
	out := base
	for _, info := range extra {
		if len(out) >= maxImagesPerSpecies || seen[info.SourceURL] {
			continue
		}
		seen[info.SourceURL] = true
		out = append(out, info)
	}
	return out
}
