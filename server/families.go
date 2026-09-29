package main

import (
	_ "embed"
	"log"
)

// GBIF-sourced common names for eBird's ~251 bird families, keyed by
// familyCode, refreshed alongside the taxonomy snapshot by cmd/gensnapshot's
// taxonomy command. Not eBird's own familyComName (licensing — see the note
// on Taxon in taxonomy_data.go): FamilyName falls back from the target locale to
// English to the family's own scientific name (e.g. "Struthionidae")
// instead, so a family with sparse GBIF coverage still gets a sensible
// label without reaching for eBird's wording.
//
//go:embed data/family_names.json.gz
var embeddedFamilyNames []byte

// FamilyInfo is one family's GBIF-derived data.
type FamilyInfo struct {
	SciName string            `json:"sciName"` // the family's scientific name, e.g. "Struthionidae" — always present, the ultimate fallback
	Names   map[string]string `json:"names"`   // lang -> common name, wherever GBIF had one
}

// familyInfo is computed eagerly, like validLang in taxonomy_data.go, so
// it's ready without any explicit startup wiring.
var familyInfo = mustLoadFamilyInfo()

func mustLoadFamilyInfo() map[string]FamilyInfo {
	m, err := decodeGzippedJSON[map[string]FamilyInfo](embeddedFamilyNames)
	if err != nil {
		log.Fatalf("embedded family names: %v", err)
	}
	return m
}

// FamilyName returns familyCode's display name in locale: its GBIF common
// name in locale, then in English, then its scientific name, then (only if
// gensnapshot never saw this family at all) familyCode itself.
func FamilyName(familyCode, locale string) string {
	info, ok := familyInfo[familyCode]
	if !ok {
		return familyCode
	}
	if name, ok := info.Names[locale]; ok {
		return name
	}
	if name, ok := info.Names["en"]; ok {
		return name
	}
	if info.SciName != "" {
		return info.SciName
	}
	return familyCode
}
