# birdquiz

A family bird-learning web app: search hotspots worldwide, browse the
species seen at one, learn them in a full-screen swipeable card deck. Go
server, SQLite for accounts, offline GBIF-derived data for
hotspots/species. See `ARCHITECTURE.md` for how it's built.

## Run locally

```
make server-open   # http://localhost:8080 — no login required
make server         # same, but honors GOOGLE_AUTH_ENABLED/APPLE_AUTH_ENABLED
```

Needs Go and `server/data/hotspots.bolt` and `server/data/places.bolt`
present (see below) — everything else is a plain file on disk, no other
services required. Full env var list is documented at the top of
`server/main.go`.

## Deploy

```
make image-push   # build + push the container image
make deploy        # ...and point the GitOps manifest at the new tag
```

## Species image cache

Learn card photos are fetched on demand from Wikimedia/iNaturalist and
cached to disk (`CACHE_DIR`, default `./cache`) — nothing to refresh or
migrate manually in normal operation. If `CACHE_DIR` has leftovers from an
older cache layout, the server folds them into the current one itself, on
every startup, before it starts listening (see "Learn mode and species
images" in `ARCHITECTURE.md`) — local disk I/O only, done in well under a
second, logged to both stdout and `<CACHE_DIR>/migration.log`.

Two offline modes of the same binary are there for when you want more than
that automatic minimum:

```
go run . warmcache       # pre-fetch images for a high-value species subset
go run . migrateimages    # eagerly top every species up to the full image
                          # count now, instead of waiting on live traffic
```

Run either against a local `CACHE_DIR`, then `rsync` the resulting `cache/`
directory up to the deployed server's host path — see `server/warmcache.go`
and `server/migrateimages.go` for what each one actually does.

## Refresh the bird data

Four independent sources, all refreshed roughly yearly, none called at
request time by the deployed server:

| source | provides | committed to git? |
|---|---|---|
| eBird taxonomy API | which species exist, classification (no names) | yes |
| GBIF (species API) | common names, species and family, ~20 languages | yes |
| GBIF (EOD dataset) | which birds occur where, how often | no — `scp`'d |
| GeoNames (cities500) | place names for search, worldwide | no — `scp`'d |
| db-ip (IP to Country Lite) | default UI language for a new account | yes |

### Taxonomy and common names (a few minutes)

```
cd server
EBIRD_API_KEY=... go run ./cmd/gensnapshot taxonomy   # run from home; eBird blocks cloud IPs
git add data/taxonomy_core.json.gz data/species_names.json.gz data/family_names.json.gz
git commit -m "Refresh taxonomy and common names"
```

Prints each language's common-name coverage to stderr as it runs — see
"Taxonomy and common names" in `ARCHITECTURE.md` for what it's doing and
why only species names (not family names) are gated by a coverage
threshold.

### Hotspot/species data (GBIF, ~20 min once downloaded)

```bash
curl -X POST -H "Content-Type: application/json" \
  -u '<gbif_username>:<gbif_password>' \
  https://api.gbif.org/v1/occurrence/download/request \
  -d '{
    "sendNotification": true,
    "notificationAddresses": ["you@example.com"],
    "format": "SQL_TSV_ZIP",
    "sql": "SELECT decimalLatitude, decimalLongitude, locality, species, COUNT(*) AS n FROM occurrence WHERE datasetKey = '\''4fa7b334-ce0d-4e88-aaae-2e0c138d049e'\'' AND decimalLatitude IS NOT NULL GROUP BY decimalLatitude, decimalLongitude, locality, species ORDER BY decimalLatitude, decimalLongitude"
  }'
```

Poll `https://api.gbif.org/v1/occurrence/download/<KEY>` until `status` is
`SUCCEEDED` (~10 min–a few hours depending on GBIF's queue), then:

```
curl -sL -o gbif_download.zip "<downloadLink>"
cd server
go run ./cmd/gensnapshot hotspots gbif_download.zip   # ~20 min, writes data/hotspots.bolt
scp data/hotspots.bolt <server-host>:<data-dir>
```

Then restart the server (or just let it pick up the file — opening it is
near-instant regardless of size). `server/.gitignore` already excludes
`hotspots.bolt` — don't `git add` it.

**Gotchas:**
- GBIF account needs a **username** (not email); free signup at
  gbif.org. Keep the password out of shell history — a `.netrc` entry or a
  gitignored env file you `source` is safer.
- The `ORDER BY` in the query above is required, not optional — the build
  tool depends on sorted input and fails loudly (`"input not sorted by
  point"`) if it isn't.
- `year` is a reserved word in GBIF's SQL dialect; needs double-quoting
  (`"year"`) if a query ever needs to filter by it.

See `ARCHITECTURE.md` for why the pipeline is shaped this way (GBIF vs.
live eBird, the `bbolt` schema, the build algorithm).

### Place-search data (GeoNames, a couple of minutes)

```bash
curl -sL -o cities500.zip http://download.geonames.org/export/dump/cities500.zip
cd server
go run ./cmd/gensnapshot places cities500.zip   # writes data/places.bolt (tens of MB)
scp data/places.bolt <server-host>:<data-dir>
```

Same directory, same restart-or-let-it-pick-up-the-file story as
`hotspots.bolt` above; `server/.gitignore` excludes `places.bolt` too. No
account or key needed — GeoNames' dumps are a plain public download.
`cities500.zip` (every place with population > 500 or that's a seat of
local government, ~185K rows) is the right file, not `allCountries.zip`
(~12M rows, mostly geographic features no one searches for by name).

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
