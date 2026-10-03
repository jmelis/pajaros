package main

import (
	"log"
	"net/http"
	"sort"
)

// SpeciesCredits is one species' entry on an area's credits page: the
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

// handleAreaCredits lists the photo credits for the species in an area,
// from each species' cached image metadata — so it names exactly the photos
// the app has downloaded and can show, no more. The optional ?species=<code>
// narrows it to that one species. Species with no cached photo are omitted;
// ordered by common name in the requested language.
func (s *Server) handleAreaCredits(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !validAreaKey(key) {
		http.Error(w, "invalid area key", http.StatusBadRequest)
		return
	}
	lang, ok := s.resolveLang(w, r)
	if !ok {
		return
	}
	codes, taxa, err := s.species.Species(key, lang, 0)
	if err != nil {
		log.Printf("Species(%s, %s): %v", key, lang, err)
		http.Error(w, "failed to look up species", http.StatusNotFound)
		return
	}

	only := r.URL.Query().Get("species")
	out := []SpeciesCredits{}
	for _, code := range codes {
		if only != "" && code != only {
			continue
		}
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
