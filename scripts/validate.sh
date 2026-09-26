#!/usr/bin/env bash
# validate.sh — catalog integrity checks. Exits 0 if all pass, 1 otherwise.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v jq >/dev/null || { echo "FATAL: jq is required (brew install jq / apt install jq)" >&2; exit 1; }

errors=0
warnings=0

err()  { echo "  ERROR: $1"; errors=$((errors+1)); }
warn() { echo "  WARN:  $1"; warnings=$((warnings+1)); }
ok()   { echo "  OK:    $1"; }

filesize() { stat -f%z "$1" 2>/dev/null || stat -c%s "$1" 2>/dev/null; }

echo
echo "=== Loading places.json ==="
if [[ ! -f places.json ]]; then
  echo "FATAL: places.json not found"
  exit 1
fi
if ! jq_err=$(jq empty places.json 2>&1); then
  echo "FATAL: Cannot parse places.json: $jq_err"
  exit 1
fi

for p in alicante ourense bruselas; do
  jq -e ".places[\"$p\"]" places.json > /dev/null 2>&1 || err "Missing required place: $p"
done

echo
echo "=== Checking places ==="

while IFS= read -r place; do
  echo
  echo "--- $place ---"

  name_es=$(jq -r ".places[\"$place\"].name_es // empty" places.json)
  [[ -n "$name_es" ]] || err "$place: missing name_es"
  name_fr=$(jq -r ".places[\"$place\"].name_fr // empty" places.json)
  [[ -n "$name_fr" ]] || err "$place: missing name_fr"

  count=$(jq ".places[\"$place\"].species | length" places.json)
  if [[ "$count" -ne 20 ]]; then
    err "$place: expected 20 species, got $count"
  else
    ok "$place: 20 species"
  fi

  dup=$(jq -r ".places[\"$place\"].species | group_by(.) | map(select(length>1) | .[0]) | .[]" places.json)
  if [[ -n "$dup" ]]; then
    while IFS= read -r d; do err "$place: duplicate species \"$d\""; done <<< "$dup"
  else
    ok "$place: no duplicates"
  fi

  while IFS= read -r sp; do
    [[ -d "birds/$sp" ]] || err "$place: species directory missing: birds/$sp/"
  done < <(jq -r ".places[\"$place\"].species[]" places.json)
done < <(jq -r '.places | keys[]' places.json)

echo
echo "=== Checking species metadata ==="

# All unique species referenced across every place, sorted.
all_species=()
while IFS= read -r sp; do all_species+=("$sp"); done < <(jq -r '[.places[].species[]] | unique | .[]' places.json)

for sp in "${all_species[@]}"; do
  echo
  echo "--- $sp ---"
  dir="birds/$sp"
  meta="$dir/metadata.json"

  if [[ ! -d "$dir" ]]; then
    err "Directory missing: $dir/"
    continue
  fi
  if [[ ! -f "$meta" ]]; then
    err "metadata.json missing in $dir/"
    continue
  fi
  if ! jq_err=$(jq empty "$meta" 2>&1); then
    err "Invalid JSON in $meta: $jq_err"
    continue
  fi

  sci=$(jq -r '.scientific_name // empty' "$meta")
  if [[ -z "$sci" ]]; then
    err "$sp: missing scientific_name"
  elif [[ "$sci" != "$sp" ]]; then
    err "$sp: scientific_name \"$sci\" doesn't match directory name"
  else
    ok "$sp: scientific_name matches"
  fi

  name_es=$(jq -r '.name_es // empty' "$meta")
  [[ -n "$name_es" ]] && ok "$sp: name_es = \"$name_es\"" || err "$sp: missing name_es"

  name_fr=$(jq -r '.name_fr // empty' "$meta")
  [[ -n "$name_fr" ]] && ok "$sp: name_fr = \"$name_fr\"" || err "$sp: missing name_fr"

  poster=$(jq -r '.poster_image // empty' "$meta")
  [[ -n "$poster" ]] || err "$sp: missing poster_image"

  img_count=$(jq '.images | length' "$meta" 2>/dev/null)
  if [[ -z "$img_count" || "$img_count" -eq 0 ]]; then
    err "$sp: images array missing or empty"
    continue
  fi
  ok "$sp: $img_count image(s)"

  if [[ -n "$poster" ]]; then
    has_poster=$(jq --arg f "$poster" '[.images[] | select(.file == $f)] | length' "$meta")
    if [[ "$has_poster" -eq 0 ]]; then
      err "$sp: poster_image \"$poster\" not in images list"
    else
      ok "$sp: poster_image found in images"
    fi
  fi

  while IFS=$'\t' read -r file author source_url license license_url; do
    if [[ -z "$file" ]]; then
      err "$sp: image entry missing file field"
      continue
    fi
    path="$dir/$file"
    if [[ ! -f "$path" ]]; then
      err "$sp: image file missing: $file"
    else
      size=$(filesize "$path")
      if [[ "$size" -lt 1000 ]]; then
        warn "$sp: image $file is very small ($size bytes) - may be corrupt"
      else
        ok "$sp: image $file exists ($((size/1024))KB)"
      fi
    fi
    [[ -n "$author" ]]      || err "$sp: image $file missing field: author"
    [[ -n "$source_url" ]]  || err "$sp: image $file missing field: source_url"
    [[ -n "$license" ]]     || err "$sp: image $file missing field: license"
    [[ -n "$license_url" ]] || err "$sp: image $file missing field: license_url"
  done < <(jq -r '.images[] | [.file, (.author//""), (.source_url//""), (.license//""), (.license_url//"")] | @tsv' "$meta")
done

echo
echo "=== Summary ==="
echo "  Species checked: ${#all_species[@]}"
echo "  Errors:   $errors"
echo "  Warnings: $warnings"

if [[ "$errors" -gt 0 ]]; then
  echo
  echo "Validation FAILED"
  exit 1
else
  echo
  echo "Validation PASSED"
  exit 0
fi
