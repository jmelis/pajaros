#!/usr/bin/env bash
# Builds server/data/places.bolt from OpenStreetMap (Geofabrik extracts) and
# Natural Earth. Needs osmium-tool, curl, python3 and ~15 GB of free disk; the
# whole run downloads about 95 GB and takes a few hours.
#
#   scripts/places_osm.sh <workdir>
#
# Each extract is downloaded, reduced to candidate places and deleted, so only
# the small <workdir>/cand/*.jsonl files accumulate. Re-running skips extracts
# whose candidate file already exists.
set -euo pipefail

work=${1:?usage: places_osm.sh <workdir>}
here=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$work/cand" "$work/tmp"
gs="$work/gensnapshot"
(cd "$here" && go build -o "$gs" ./cmd/gensnapshot)

# Features that become places: boundaries, parks, reserves, islands, deserts
# and the city/town/village nodes.
filter=(r/boundary=administrative,national_park,protected_area
        nwr/leisure=park,nature_reserve
        n/place=city,town,village,island,archipelago
        nwr/place=island,archipelago
        r/natural=desert w/natural=desert)

curl -fsS -m 120 -o "$work/index.json" https://download.geofabrik.de/index-v1-nogeom.json

# Continents are fetched whole; Europe and Russia as country extracts so no
# single download is huge. Aggregates that overlap country extracts are skipped.
python3 - "$work/index.json" > "$work/extracts.txt" <<'PY'
import json, sys
skip = {"europe", "north-america", "alps", "britain-and-ireland", "dach", "great-britain",
        "us-midwest", "us-northeast", "us-pacific", "us-south", "us-west", "south-africa-and-lesotho"}
whole = {"africa", "asia", "australia-oceania", "central-america", "south-america", "antarctica", "russia"}
for f in json.load(open(sys.argv[1]))["features"]:
    p = f["properties"]
    url = p["urls"].get("pbf")
    if not url or p["id"] in skip:
        continue
    parent = p.get("parent")
    if p["id"] in whole or parent in ("europe", "north-america") and not p["id"].startswith("us/"):
        print(p["id"].replace("/", "_"), url)
PY

while read -r name url; do
  out="$work/cand/$name.jsonl"
  [ -s "$out" ] && continue
  echo "$(date +%T) $name: download"
  curl -fSL --retry 5 --retry-delay 10 -C - -o "$work/tmp/$name.pbf" "$url"
  echo "$(date +%T) $name: filter"
  osmium tags-filter -o "$work/tmp/$name.f.pbf" --overwrite "$work/tmp/$name.pbf" "${filter[@]}"
  rm -f "$work/tmp/$name.pbf"
  osmium export -f geojsonseq --geometry-types=point,polygon --add-unique-id=type_id \
    -o "$work/tmp/$name.geojsonseq" --overwrite "$work/tmp/$name.f.pbf"
  rm -f "$work/tmp/$name.f.pbf"
  "$gs" osm "$work/tmp/$name.geojsonseq" "$out.part"
  mv "$out.part" "$out"
  rm -f "$work/tmp/$name.geojsonseq"
done < "$work/extracts.txt"

if [ ! -s "$work/cand/ne.jsonl" ]; then
  ne=https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson
  curl -fS -o "$work/ne_admin0.geojson" $ne/ne_10m_admin_0_countries.geojson
  curl -fS -o "$work/ne_regions.geojson" $ne/ne_10m_geography_regions_polys.geojson
  "$gs" naturalearth "$work/ne_admin0.geojson" "$work/ne_regions.geojson" "$work/cand/ne.jsonl"
fi

"$gs" places "$here/data/places.bolt" "$work"/cand/*.jsonl
echo "$(date +%T) done"
