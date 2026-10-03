# birdsnearby

A family bird-learning web app: search any place worldwide (or draw a
circle on the map), browse the species seen there, learn them in a
full-screen swipeable card deck. Go server, SQLite for accounts, offline
GBIF-derived data for species and OpenStreetMap-derived data for places. See `ARCHITECTURE.md` for how it's built.

## Run locally

```
make server-open   # http://localhost:8080 — no login required
make server         # same, but honors GOOGLE_AUTH_ENABLED/APPLE_AUTH_ENABLED
```

Needs Go and `server/data/seasonal_cells.bolt` and `server/data/places.bolt`
present (see below; `DATA_DIR` points elsewhere) — everything else is a plain file on disk, no other
services required. Full env var list is documented at the top of
`server/main.go`.

## Deploy

```
make image-push   # build + push the container image
make deploy        # ...and point the GitOps manifest at the new tag
```

## Species image cache

Learn card photos are fetched on demand from Wikimedia/iNaturalist and
cached to disk (`CACHE_DIR`, default `./cache`) — nothing to refresh
manually in normal operation. When the image sourcing policy changes
(`imagePolicyVersion` in `server/imagepolicy.go`), the server re-trims the
cache once at startup (see "Learn mode and species images" in
`ARCHITECTURE.md`), logged to stdout and `<CACHE_DIR>/migration.log`.

One offline mode of the same binary is there for when you want more than
that automatic minimum:

```
go run . warmcache          # pre-fetch images for a high-value species subset
```

Run it against a local `CACHE_DIR`, then `rsync` the resulting `cache/`
directory up to the deployed server's host path — see `server/warmcache.go`
for what it does.

## Refresh the bird data

Four independent sources, all refreshed roughly yearly, none called at
request time by the deployed server:

| source | provides | committed to git? |
|---|---|---|
| eBird taxonomy API | which species exist, classification (no names) | yes |
| Multilingual IOC World Bird List | species common names, ~23 languages (CC BY 3.0) | yes — download the .xlsx |
| GBIF (species API) | species names IOC lacks, family names | yes |
| GBIF (EOD dataset) | which birds occur where, how often | no — `scp`'d as `seasonal_cells.bolt` |
| OpenStreetMap (Geofabrik extracts) | place names and outlines for search: cities, towns, boundaries, parks, reserves, islands, deserts (ODbL) | no — `scp`'d as `places.bolt` |
| Natural Earth (admin-0, geography regions) | country outlines and big natural regions (public domain) | no — folded into `places.bolt` |
| db-ip (IP to Country Lite) | default UI language for a new account | yes |

### Taxonomy and common names (a few minutes)

```
cd server
# Download the latest "Multilingual" .xlsx from
# https://www.worldbirdnames.org/new/ioc-lists/master-list-2/
EBIRD_API_KEY=... go run ./cmd/gensnapshot taxonomy Multiling-IOC-15.2.xlsx   # run from home; eBird blocks cloud IPs
git add data/taxonomy_core.json.gz data/species_names.json.gz data/family_names.json.gz
git commit -m "Refresh taxonomy and common names"
```

Prints each language's common-name coverage to stderr as it runs — see
"Taxonomy and common names" in `ARCHITECTURE.md` for what it's doing and
why only species names (not family names) are gated by a coverage
threshold.

### Species data (GBIF, ~1 h once downloaded)

```bash
curl -X POST -H "Content-Type: application/json" \
  -u '<gbif_username>:<gbif_password>' \
  https://api.gbif.org/v1/occurrence/download/request \
  -d '{
    "sendNotification": true,
    "notificationAddresses": ["you@example.com"],
    "format": "SQL_TSV_ZIP",
    "sql": "SELECT decimalLatitude, decimalLongitude, locality, species, \"month\", COUNT(*) AS n FROM occurrence WHERE datasetKey = '\''4fa7b334-ce0d-4e88-aaae-2e0c138d049e'\'' AND decimalLatitude IS NOT NULL GROUP BY decimalLatitude, decimalLongitude, locality, species, \"month\" ORDER BY decimalLatitude, decimalLongitude"
  }'
```

Poll `https://api.gbif.org/v1/occurrence/download/<KEY>` until `status` is
`SUCCEEDED` (~10 min–a few hours depending on GBIF's queue), then:

```
curl -sL -o gbif_download.zip "<downloadLink>"
cd server
go run ./cmd/gensnapshot hotspots gbif_download.zip   # ~20 min, writes data/hotspots_seasonal.bolt (an intermediate)
go run ./cmd/gensnapshot areas                        # regroups it into data/seasonal_cells.bolt (~1.2 GB)
scp data/seasonal_cells.bolt <server-host>:<data-dir>
```

Only `seasonal_cells.bolt` goes to the server; `hotspots_seasonal.bolt` is just
the input of the `areas` step. `scp` the new file before deploying a server
build that needs it. `server/.gitignore` already excludes both — don't `git add`
them.

**Gotchas:**
- GBIF account needs a **username** (not email); free signup at
  gbif.org. Keep the password out of shell history — a `.netrc` entry or a
  gitignored env file you `source` is safer.
- The `ORDER BY` in the query above is required, not optional — the build
  tool depends on sorted input and fails loudly (`"input not sorted by
  point"`) if it isn't.
- The `month` column is required: the build keeps per-month counts per
  species per location (the species list's month selector reads them). The
  download is roughly 10 GB zipped; the build streams it from the zip
  without unpacking.
- The file carries a species-format label, and the server refuses to start
  on one it can't decode.
- `year` is a reserved word in GBIF's SQL dialect; needs double-quoting
  (`"year"`) if a query ever needs to filter by it.

See `ARCHITECTURE.md` for why the pipeline is shaped this way (GBIF vs.
live eBird, the `bbolt` schema, the build algorithm).

### Place-search data (OpenStreetMap + Natural Earth, a few hours)

```bash
cd server
scripts/places_osm.sh ~/work/places   # writes data/places.bolt
scp data/places.bolt <server-host>:<data-dir>
```

The script downloads Geofabrik's regional extracts one at a time, keeps only the
named places in each (`osmium tags-filter`, then `gensnapshot osm`), deletes the
extract, fetches Natural Earth's country and region outlines, and finally merges
everything with `gensnapshot places`. It needs `osmium-tool`, `curl` and
`python3`, about 15 GB of free disk, and downloads about 95 GB in total. The
per-extract candidate files stay in the work directory, so an interrupted run
resumes where it stopped and the final merge can be rerun on its own:

```
go run ./cmd/gensnapshot places data/places.bolt ~/work/places/cand/*.jsonl
```

`server/.gitignore` excludes `places.bolt` too. See "Place search" in
`ARCHITECTURE.md` for what is kept and how places are labeled and ranked.

### Default-language geoip table (db-ip, seconds)

```
curl -sL -o dbip-country-lite.csv.gz "https://download.db-ip.com/free/dbip-country-lite-<YYYY>-<MM>.csv.gz"
cd server
go run ./cmd/gensnapshot geoip dbip-country-lite.csv.gz   # writes data/geoip_country.json.gz
git add data/geoip_country.json.gz && git commit -m "Refresh geoip country table"
```

The exact filename (with the current year/month) is on
[db-ip.com/db/download/ip-to-country-lite](https://db-ip.com/db/download/ip-to-country-lite).
It's a free, monthly-refreshed, CC-BY-4.0 IP-to-country database; the
`geoip` subcommand keeps only the rows for the Spanish/French-speaking
countries listed in `server/cmd/gensnapshot/geoip.go` — everything else
maps to English by default, so there's no need to ship the whole world.
See ARCHITECTURE.md's "Authentication" section for how the result is used.
