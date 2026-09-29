package main

// Multi-source image resolution: a species' Learn card shows up to
// maxImagesPerSpecies photos. Wikimedia (see wikimedia.go) supplies at most
// one — the Wikipedia infobox photo, the one Commons image trusted enough to
// use without a human rechecking it, since everything else in a Commons
// category is just a free-text tag any contributor can add with nothing
// enforcing that it actually depicts the species. iNaturalist (see
// inaturalist.go) supplies the rest, for every species — its research-grade
// observations are tied to a specific community-identified sighting rather
// than a category tag, a real correctness guarantee a Commons category walk
// never had.

// maxImagesPerSpecies caps how many photos the cache keeps per species —
// the Learn card's image count.
const maxImagesPerSpecies = 4

// ResolveImages finds up to max freely-licensed candidate images for
// sciName, width pixels wide: the Wikipedia infobox photo from Wikimedia
// Commons if there is one, then iNaturalist for the rest.
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
