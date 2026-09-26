#!/usr/bin/env node
/**
 * validate.js — Catalog integrity checks
 * Exits 0 if all checks pass, 1 if any errors found.
 */

const fs = require('fs');
const path = require('path');
const yaml = require('js-yaml');

const ROOT = path.join(__dirname, '..');
const BIRDS_DIR = path.join(ROOT, 'birds');
const PLACES_FILE = path.join(ROOT, 'places.yaml');

let errors = 0;
let warnings = 0;

function error(msg) {
  console.error(`  ERROR: ${msg}`);
  errors++;
}

function warn(msg) {
  console.warn(`  WARN:  ${msg}`);
  warnings++;
}

function ok(msg) {
  console.log(`  OK:    ${msg}`);
}

// ─── Load places.yaml ────────────────────────────────────────────────────────

console.log('\n=== Loading places.yaml ===');
if (!fs.existsSync(PLACES_FILE)) {
  console.error('FATAL: places.yaml not found');
  process.exit(1);
}

let placesData;
try {
  placesData = yaml.load(fs.readFileSync(PLACES_FILE, 'utf8'));
} catch (e) {
  console.error(`FATAL: Cannot parse places.yaml: ${e.message}`);
  process.exit(1);
}

const places = placesData.places;
if (!places || typeof places !== 'object') {
  console.error('FATAL: places.yaml must have a top-level "places" key');
  process.exit(1);
}

const REQUIRED_PLACES = ['alicante', 'ourense', 'bruselas'];
for (const p of REQUIRED_PLACES) {
  if (!places[p]) error(`Missing required place: ${p}`);
}

// ─── Check each place ────────────────────────────────────────────────────────

console.log('\n=== Checking places ===');
const allSpecies = new Set();

for (const [placeId, place] of Object.entries(places)) {
  console.log(`\n--- ${placeId} ---`);
  if (!place.name_es) error(`${placeId}: missing name_es`);
  if (!place.name_fr) error(`${placeId}: missing name_fr`);
  if (!Array.isArray(place.species)) {
    error(`${placeId}: species must be an array`);
    continue;
  }

  // Exactly 20 species
  if (place.species.length !== 20) {
    error(`${placeId}: expected 20 species, got ${place.species.length}`);
  } else {
    ok(`${placeId}: 20 species`);
  }

  // No duplicates within place
  const seen = new Set();
  for (const sp of place.species) {
    if (seen.has(sp)) error(`${placeId}: duplicate species "${sp}"`);
    seen.add(sp);
    allSpecies.add(sp);
  }
  if (seen.size === place.species.length) ok(`${placeId}: no duplicates`);

  // Each species folder exists
  for (const sp of place.species) {
    const dir = path.join(BIRDS_DIR, sp);
    if (!fs.existsSync(dir)) {
      error(`${placeId}: species directory missing: birds/${sp}/`);
    }
  }
}

// ─── Check each species ──────────────────────────────────────────────────────

console.log('\n=== Checking species metadata ===');
const REQUIRED_IMAGE_FIELDS = ['file', 'author', 'source_url', 'license', 'license_url'];

if (!fs.existsSync(BIRDS_DIR)) {
  error('birds/ directory does not exist');
} else {
  for (const sp of allSpecies) {
    console.log(`\n--- ${sp} ---`);
    const dir = path.join(BIRDS_DIR, sp);
    const metaFile = path.join(dir, 'metadata.json');

    if (!fs.existsSync(dir)) {
      error(`Directory missing: birds/${sp}/`);
      continue;
    }

    if (!fs.existsSync(metaFile)) {
      error(`metadata.json missing in birds/${sp}/`);
      continue;
    }

    let meta;
    try {
      meta = JSON.parse(fs.readFileSync(metaFile, 'utf8'));
    } catch (e) {
      error(`Invalid JSON in birds/${sp}/metadata.json: ${e.message}`);
      continue;
    }

    // Required top-level fields
    if (!meta.scientific_name) error(`${sp}: missing scientific_name`);
    else if (meta.scientific_name !== sp) error(`${sp}: scientific_name "${meta.scientific_name}" doesn't match directory name`);
    else ok(`${sp}: scientific_name matches`);

    if (!meta.name_es) error(`${sp}: missing name_es`);
    else ok(`${sp}: name_es = "${meta.name_es}"`);

    if (!meta.name_fr) error(`${sp}: missing name_fr`);
    else ok(`${sp}: name_fr = "${meta.name_fr}"`);

    if (!meta.poster_image) error(`${sp}: missing poster_image`);

    // Images array
    if (!Array.isArray(meta.images) || meta.images.length === 0) {
      error(`${sp}: images array missing or empty`);
      continue;
    }
    ok(`${sp}: ${meta.images.length} image(s)`);

    // poster_image must exist in images list
    if (meta.poster_image) {
      const posterInList = meta.images.some(img => img.file === meta.poster_image);
      if (!posterInList) error(`${sp}: poster_image "${meta.poster_image}" not in images list`);
      else ok(`${sp}: poster_image found in images`);
    }

    // Check each image
    for (const img of meta.images) {
      if (!img.file) {
        error(`${sp}: image entry missing file field`);
        continue;
      }

      // Image file must exist
      const imgPath = path.join(dir, img.file);
      if (!fs.existsSync(imgPath)) {
        error(`${sp}: image file missing: ${img.file}`);
      } else {
        // Check file is not empty
        const stat = fs.statSync(imgPath);
        if (stat.size < 1000) {
          warn(`${sp}: image ${img.file} is very small (${stat.size} bytes) - may be corrupt`);
        } else {
          ok(`${sp}: image ${img.file} exists (${Math.round(stat.size/1024)}KB)`);
        }
      }

      // Required attribution fields
      for (const field of REQUIRED_IMAGE_FIELDS) {
        if (!img[field]) error(`${sp}: image ${img.file} missing field: ${field}`);
      }
    }
  }
}

// ─── Summary ─────────────────────────────────────────────────────────────────

console.log('\n=== Summary ===');
console.log(`  Species checked: ${allSpecies.size}`);
console.log(`  Errors:   ${errors}`);
console.log(`  Warnings: ${warnings}`);

if (errors > 0) {
  console.error('\nValidation FAILED');
  process.exit(1);
} else {
  console.log('\nValidation PASSED');
  process.exit(0);
}
