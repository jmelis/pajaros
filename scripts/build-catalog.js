#!/usr/bin/env node
/**
 * build-catalog.js — Generate catalog.json from birds/ and places.yaml
 * Output: catalog.json in project root
 */

const fs = require('fs');
const path = require('path');
const yaml = require('js-yaml');

const ROOT = path.join(__dirname, '..');
const BIRDS_DIR = path.join(ROOT, 'birds');
const PLACES_FILE = path.join(ROOT, 'places.yaml');
const OUTPUT_FILE = path.join(ROOT, 'catalog.json');

// Load places.yaml
const placesData = yaml.load(fs.readFileSync(PLACES_FILE, 'utf8'));
const places = placesData.places;

// Collect all unique species
const allSpecies = new Set();
for (const place of Object.values(places)) {
  for (const sp of place.species) allSpecies.add(sp);
}

// Build species map
const speciesMap = {};
for (const sp of allSpecies) {
  const metaFile = path.join(BIRDS_DIR, sp, 'metadata.json');
  if (!fs.existsSync(metaFile)) {
    console.error(`Missing: birds/${sp}/metadata.json`);
    process.exit(1);
  }
  const meta = JSON.parse(fs.readFileSync(metaFile, 'utf8'));
  speciesMap[sp] = meta;
}

// Build catalog
const catalog = {
  generated: new Date().toISOString(),
  places: {},
  species: {}
};

// Places section
for (const [placeId, place] of Object.entries(places)) {
  catalog.places[placeId] = {
    name_es: place.name_es,
    name_fr: place.name_fr,
    species: place.species
  };
}

// Species section
for (const [sp, meta] of Object.entries(speciesMap)) {
  catalog.species[sp] = {
    scientific_name: meta.scientific_name,
    name_es: meta.name_es,
    name_fr: meta.name_fr,
    poster_image: meta.poster_image,
    images: meta.images.map(img => ({
      file: img.file,
      alt_es: img.alt_es || `${meta.name_es}`,
      sex_age: img.sex_age || null
    })),
    // Attribution kept separate for credits page
    attribution: meta.images.map(img => ({
      file: img.file,
      author: img.author,
      source_url: img.source_url,
      license: img.license,
      license_url: img.license_url
    }))
  };
}

fs.writeFileSync(OUTPUT_FILE, JSON.stringify(catalog, null, 2));
console.log(`catalog.json written: ${Object.keys(catalog.species).length} species, ${Object.keys(catalog.places).length} places`);
