# pajaros

A family bird-quiz web app: search hotspots worldwide, browse the species
seen at one, quiz yourself with spaced repetition. Go server, SQLite for
accounts, offline GBIF-derived data for hotspots/species. See
`ARCHITECTURE.md` for how it's built.

## Run locally

```
make server-open   # http://localhost:8080 — no login required
make server         # same, but honors GOOGLE_AUTH_ENABLED/APPLE_AUTH_ENABLED
```

Needs Go and `server/data/hotspots.bolt` present (see below) — everything
else is a plain file on disk, no other services required. Full env var
list is documented at the top of `server/main.go`.

## Deploy

```
make image-push   # build + push the container image
make deploy        # ...and point the GitOps manifest at the new tag
```

## Refresh the bird data

Two independent sources, both refreshed roughly yearly, neither called at
request time by the deployed server:

| source | provides | committed to git? |
|---|---|---|
| eBird taxonomy API | species names (en/es/fr), classification | yes |
| GBIF (EOD dataset) | which birds occur where, how often | no — `scp`'d |

### Taxonomy (seconds)

```
cd server
EBIRD_API_KEY=... go run ./cmd/gensnapshot taxonomy   # run from home; eBird blocks cloud IPs
git add data/taxonomy_*.json.gz && git commit -m "Refresh eBird taxonomy"
```

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
