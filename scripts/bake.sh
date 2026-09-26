#!/usr/bin/env bash
# bake.sh — merge places.json + birds/*/metadata.json into one JSON blob and
# inline it into index.template.html, producing index.html.
#
# No fetch() at runtime means the page works straight from file://, no local
# server needed. index.html is generated — edit index.template.html instead.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v jq >/dev/null || { echo "FATAL: jq is required (brew install jq / apt install jq)" >&2; exit 1; }

TMP_SPECIES="$(mktemp)"
TMP_CATALOG="$(mktemp)"
TMP_CUTOUTS="$(mktemp)"
trap 'rm -f "$TMP_SPECIES" "$TMP_CATALOG" "$TMP_CUTOUTS"' EXIT

# Map of scientific_name -> poster-cutout.png, only for species that have one
# (background-removed version of poster_image, used by the print poster).
{
  for dir in birds/*/; do
    sp="$(basename "$dir")"
    if [[ -f "${dir}poster-cutout.png" ]]; then
      jq -n --arg sp "$sp" '{($sp): "poster-cutout.png"}'
    fi
  done
} | jq -s 'add // {}' > "$TMP_CUTOUTS"

# One object per species, keyed by scientific_name, in the shape app.js expects.
jq -s --slurpfile cutouts "$TMP_CUTOUTS" '
  map(. as $m | {($m.scientific_name): {
    scientific_name: $m.scientific_name,
    name_es: $m.name_es,
    name_fr: $m.name_fr,
    poster_image: $m.poster_image,
    poster_cutout: ($cutouts[0][$m.scientific_name] // null),
    poster_cutout_aspect: ($m.poster_cutout_aspect // null),
    images: ($m.images | map({file, alt_es: (.alt_es // $m.name_es), sex_age: (.sex_age // null)})),
    attribution: ($m.images | map({file, author, source_url, license, license_url})),
    audio: ($m.audio // null)
  }}) | add
' birds/*/metadata.json > "$TMP_SPECIES"

jq -s '{generated: (now | todate), places: .[0].places, species: .[1]}' \
  places.json "$TMP_SPECIES" > "$TMP_CATALOG"

awk -v datafile="$TMP_CATALOG" '
  /<!--CATALOG_DATA-->/ {
    print "<script type=\"application/json\" id=\"catalog-data\">"
    while ((getline line < datafile) > 0) print line
    print "</script>"
    next
  }
  { print }
' index.template.html > index.html

# Cache-bust src/app.js and src/style.css with a content hash, so browsers
# (and GitHub Pages' CDN) pick up changes immediately instead of serving a
# stale cached copy — a plain filename URL has no way to signal "this changed".
file_hash() {
  if command -v shasum >/dev/null; then shasum -a 256 "$1" | cut -c1-10
  else sha256sum "$1" | cut -c1-10
  fi
}
JS_HASH="$(file_hash src/app.js)"
CSS_HASH="$(file_hash src/style.css)"
sed -i.bak \
  -e "s|src/app\.js\"|src/app.js?v=${JS_HASH}\"|" \
  -e "s|src/style\.css\"|src/style.css?v=${CSS_HASH}\"|" \
  index.html
rm -f index.html.bak

species_count=$(jq 'length' "$TMP_SPECIES")
places_count=$(jq '.places | length' places.json)
echo "index.html written: $species_count species, $places_count places"
