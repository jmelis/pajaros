package main

// maxImagesPerSpecies caps how many photos the cache keeps per species —
// the Learn card's image count. A species may have fewer (see commonsImages
// in wikimedia.go): only curated, filtered Wikimedia photos are used.
const maxImagesPerSpecies = 4

// ResolveImages finds up to max freely-licensed candidate images for sciName,
// width pixels wide, from the Wikipedia infobox photo and Commons' quality
// images.
func ResolveImages(sciName string, width, max int) []*ImageInfo {
	return commonsImages(sciName, width, max)
}
