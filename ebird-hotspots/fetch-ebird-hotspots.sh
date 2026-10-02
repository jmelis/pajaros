#!/usr/bin/env bash
# Downloads eBird's curated hotspot list, one file per country, from
# GET /v2/ref/hotspot/{regionCode} — see ARCHITECTURE.md/README.md for why
# this is a separate source from the GBIF-derived hotspots_seasonal.bolt data.
#
# Usage:
#   EBIRD_API_KEY=... ./fetch-ebird-hotspots.sh [regions-csv] [out-dir]
#
# regions-csv defaults to ./ebird-regions (country_code,country_name,Continent)
# out-dir defaults to ./ebird-hotspots — one <regionCode>.csv per country.
# Re-running skips regions whose output file already exists and is non-empty,
# so an interrupted run can just be restarted.
set -euo pipefail

if [[ -z "${EBIRD_API_KEY:-}" ]]; then
  echo "EBIRD_API_KEY must be set" >&2
  exit 1
fi

regions_file="${1:-ebird-regions}"
out_dir="${2:-ebird-hotspots}"
mkdir -p "$out_dir"

total=$(($(wc -l < "$regions_file") - 1))
n=0

tail -n +2 "$regions_file" | while IFS=, read -r code _rest; do
  n=$((n + 1))
  out_file="$out_dir/$code.csv"

  if [[ -s "$out_file" ]]; then
    echo "[$n/$total] $code — already downloaded, skipping"
    continue
  fi

  status=$(curl -s -o "$out_file" -w "%{http_code}" \
    -H "X-eBirdApiToken: $EBIRD_API_KEY" \
    "https://api.ebird.org/v2/ref/hotspot/$code")

  if [[ "$status" != "200" ]]; then
    echo "[$n/$total] $code — HTTP $status (see $out_file)" >&2
  else
    lines=$(wc -l < "$out_file")
    echo "[$n/$total] $code — ok, $lines hotspots"
  fi

  sleep "$(awk 'BEGIN{srand(); print 1 + rand()}')"
done
