package main

import (
	"log"
	"net/http"
	"sort"
)

// SpeciesCredits is one species' entry on a hotspot's credits page: the
// attribution for each photo currently cached for it.
type SpeciesCredits struct {
	SpeciesCode string        `json:"speciesCode"`
	SciName     string        `json:"sciName"`
	ComName     string        `json:"comName"`
	Images      []ImageCredit `json:"images"`
}

// ImageCredit is the attribution for one cached photo. Field names mirror
// what the license terms ask a reuser to show: the work, its author, the
// license, and where it came from.
type ImageCredit struct {
	Title      string `json:"title"`
	Author     string `json:"author"`
	License    string `json:"license"`
	LicenseURL string `json:"licenseUrl"`
	SourceURL  string `json:"sourceUrl"`
}

// handleHotspotCredits lists the photo credits for every species at a
// hotspot, from each species' cached image metadata — so it names exactly the
// photos the app has downloaded and can show, no more. Species with no cached
// photo are omitted; ordered by common name in the requested language.
func (s *Server) handleHotspotCredits(w http.ResponseWriter, r *http.Request) {
	locID := r.PathValue("locId")
	if !validLocID(locID) {
		http.Error(w, "invalid locId", http.StatusBadRequest)
		return
	}
	lang, ok := s.resolveLang(w, r)
	if !ok {
		return
	}
	codes, taxa, err := s.species.Species(locID, lang)
	if err != nil {
		log.Printf("Species(%s, %s): %v", locID, lang, err)
		http.Error(w, "failed to look up hotspot species", http.StatusNotFound)
		return
	}

	out := []SpeciesCredits{}
	for _, code := range codes {
		taxon := taxa[code]
		if taxon.SciName == "" {
			continue
		}
		images := s.cache.readMetadata(taxon.SciName)
		if len(images) == 0 {
			continue
		}
		entry := SpeciesCredits{SpeciesCode: code, SciName: taxon.SciName, ComName: taxon.ComName}
		for _, img := range images {
			entry.Images = append(entry.Images, ImageCredit{
				Title:      img.Title,
				Author:     img.Author,
				License:    img.License,
				LicenseURL: img.LicenseURL,
				SourceURL:  img.SourceURL,
			})
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ComName < out[j].ComName })
	writeJSON(w, out)
}
