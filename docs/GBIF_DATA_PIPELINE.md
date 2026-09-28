# GBIF hotspot/species data pipeline

How the quiz server gets "which birds are popular where" without calling
any API at request time, and how to refresh the underlying data.

## Why this exists

eBird's live API blocks requests from the app's cloud deployment IP
(confirmed: same key, works from home, returns 403 from the deployed pod).
GBIF is not blocked from the same pod. eBird's own observation data is
mirrored on GBIF as a dataset called EOD (eBird Observation Dataset):

```
datasetKey = 4fa7b334-ce0d-4e88-aaae-2e0c138d049e
```

The server gets its hotspot/species-popularity data from one bulk GBIF
download, processed into two files the server reads at a configured path —
not `go:embed`, not committed to git. They're `scp`'d to the serving host
directly (they're large; see storage format below). No GBIF or eBird calls
happen at request time — see "How it's wired in" below for the concrete
pieces.

## The data source: GBIF's EOD mirror

Properties of this dataset that shape the design:

- **Annual static snapshot, not a rolling feed.** Exactly one EOD dataset
  key exists on GBIF. Its `pubDate` is 2025-08-08; a `facet=year` query
  shows no records past 2024 even as of Sept 2026. Whatever the latest
  complete year is when you query is what you get — it advances roughly
  once a year when Cornell republishes.
- **No eBird hotspot ID in the mirror.** Records carry `locality` (free
  text) and `decimalLatitude`/`decimalLongitude`, but no `locationID`
  field. There is no clean join key back to an eBird `L123456` hotspot ID.
- **`locality` often matches eBird's hotspot name** (e.g.
  `"Pozuelo del Rey--EDAR"`), but not reliably — personal (non-hotspot)
  locations show up too, with messy free text.
- **Coordinates don't round-trip to eBird's own numbers at full
  precision.** A hotspot registered by eBird as `41.4869773, -71.0375977`
  appears in GBIF's mirror as `41.486977, -71.0376` — agreement only to
  ~5 decimal places, and inconsistently per axis.

## Data model: GBIF's own points are the hotspots

Each distinct `(decimalLatitude, decimalLongitude, locality)` in GBIF's
EOD data is a hotspot for this app's purposes — not eBird's curated
hotspot list. Joining onto eBird's hotspot list by coordinate doesn't
recover enough of it to be worth the complexity: even a 50m haversine
match only finds 87.8% of Massachusetts's 3,920 real hotspots, because
GBIF's missing `locId` means "General Area"-style hotspots (a pin that's a
centroid over a park/refuge rather than a literal point) can't be reliably
matched — checklists submitted within that area can land 100–300m from the
registered pin, with no ID to disambiguate them. Using GBIF's own points
directly sidesteps this: the tradeoff is eBird's own location-naming
convention rather than a curated "official hotspot" list, which spot
checks (exact name+coordinate matches in Massachusetts and Madrid) show is
good enough for this app.

Most distinct points are one-off personal locations rather than real
birding sites (in Massachusetts: 48,735 distinct points, only 3,920 are
real eBird hotspots, ~8%). The build keeps every point regardless of
activity level — see storage format below for why.

## Refresh procedure

### 1. GBIF account

Free signup at gbif.org — you need a **username** (not email) and password
for Basic Auth on the download API. Keep credentials out of shell
history/chat — prefer a `.netrc` entry or a gitignored env file you
`source`, not typing it inline.

### 2. Submit the aggregation query

GBIF's SQL Downloads API (`api.gbif.org/v1/occurrence/download/request`)
runs the `GROUP BY` server-side, so only the aggregate gets downloaded, not
raw per-checklist records:

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

This returns a download key.

The `ORDER BY` matters: it makes GBIF's cluster return the result already
grouped by point, which is what lets the build tool (below) run in a
single streaming pass with memory bounded by one point at a time, instead
of needing to hold the whole dataset's structure in memory or sort it
locally.

`year` is a reserved word in GBIF's SQL dialect and needs double-quoting
as an identifier (`"year" = 2024`) if a query needs to filter by year —
unquoted `year = 2024` fails to parse. This query doesn't filter by year:
the data defines hotspot *identity* here, not a "how many recently" count,
and a single year makes quiet-but-real sites look sparser than they are
(same reasoning eBird itself uses for its own `numSpeciesAllTime`).
Recency is bounded by how often this whole procedure gets rerun (annually)
rather than by a query-time year filter.

### 3. Poll for completion

```bash
curl -s "https://api.gbif.org/v1/occurrence/download/<KEY>"
```

Watch the `status` field (`PREPARING` → `RUNNING` → `SUCCEEDED`). Also
viewable at `https://www.gbif.org/occurrence/download/<KEY>` (or list all
downloads at `https://www.gbif.org/user/download`) — not the same as
`gbif.org/occurrence/download` with no key, which is a generic "download
the entire GBIF corpus" page (424GB+ unfiltered, unrelated to this).

### 4. Download

`downloadLink` in the status response points at the zip. It contains one
`.csv` file that's actually tab-separated (columns: `decimallatitude`,
`decimallongitude`, `locality`, `species`, `n`), sorted by point.

Current full-world scale: on the order of 250 million rows, ~15GB zipped.

### 5. Build the snapshot

```bash
go run ./cmd/gensnapshot taxonomy   # if not already up to date
go run ./cmd/gensnapshot hotspots <path-to-gbif-download.zip>
```

Run from `server/`. The `.zip` is read directly — its single entry is
decompressed on the fly (`archive/zip`), so there's no manual unzip step
and the ~23GB unzipped CSV is never written to disk.

**Storage format**: a `bbolt` key-value store (`hotspots.bolt`), bucket
`species_by_hotspot`, keyed by hotspot ID (`"lat,lng"`), value = a binary
blob of `[uint16 speciesID, varint count]` pairs sorted by count
descending. The lookup this needs to serve — given a hotspot, fetch its
popularity-ordered species list — is a pure key-value read, once per quiz
session, never scanned or joined, so a relational layer (SQLite) buys
nothing here; measured against Massachusetts's ~13K hotspots, this binary
`bbolt` encoding (4.2MB) beats SQLite's normalized 3-table schema (5.3MB
gzipped) and JSON-encoded `bbolt` values (16.8MB — per-value compression
can't dedupe the same ~11K repeated species names across independent
blobs the way one shared gzip stream can), because it stores integer
species IDs instead of repeated scientific-name text. At global scale this
lands in the low single-digit GB — trivial to `scp`. `bbolt` is mmap-backed,
so nothing is ever fully loaded into RAM regardless of file size.

**No minimum-activity threshold is applied** — every distinct point
becomes a hotspot (on the order of 20 million worldwide). What's "worth
showing" by default (a map view, a search result) is a server-side query
concern (`ORDER BY total_count DESC LIMIT N`, or `WHERE total_count >= ?`
against the small index below), not a build-time one — it can change
without rerunning the GBIF download.

**Build algorithm**: because the input is sorted by point, this is a
single streaming pass with memory bounded by one point at a time, not by
total row or point count:

1. Read the file line by line. Accumulate the *current* point's data only
   — species counts (`speciesID → count`), locality counts, and a running
   total. The moment the `(lat, lng)` key changes, the previous point is
   complete: encode its species map as the sorted binary blob and its
   `{id, lat, lng, name, totalCount}` as an index entry, then reset the
   accumulator for the new point. (A cheap guard checks each new point's
   coordinates are `>=` the previous one — if GBIF's output ever weren't
   actually sorted, this fails loudly with a clear error instead of
   silently mis-grouping data.)
2. Resolve each `species` (scientific name) to a `uint16` ID via a table
   built from the embedded taxonomy, sorted by eBird `speciesCode` for a
   stable mapping — both the build tool and the server compute this
   independently from the same committed taxonomy file, so nothing extra
   needs to ship to keep them in sync.
3. Completed points are batched (500,000 at a time) into single `bbolt`
   transactions, written as fresh keys in ascending order — no
   read-modify-write, since sorted input guarantees each point is only
   ever seen once.
4. At the end, write every point's index entry to `hotspots_index.json.gz`.
5. `scp` both output files (`hotspots.bolt`, `hotspots_index.json.gz`) to
   the serving host; point the server at their path via config/env var.

At current global scale this runs in a few minutes.

Implemented in `server/cmd/gensnapshot/hotspots.go`.

### 6. How it's wired in

The server makes zero external calls at request time — Wikimedia (for
images) is the only upstream API left. `main.go` reads `HOTSPOTS_DATA_DIR`
(default `./data`) at startup and loads both files from there:
`HotspotStore` (`hotspots_data.go`) indexes `hotspots_index.json.gz` fully
in memory (small — no species data) with a 1°-grid spatial index for
`Nearby` queries; `SpeciesStore` (`species_store.go`) opens `hotspots.bolt`
read-only and answers `Lookup(hotspotID)` via `bbolt`, mmap-backed so nothing
is fully loaded. `SpeciesResolver` (`species_resolver.go`) sits on top of
both, implementing the `hotspotSpeciesSource` interface `main.go`/`quiz.go`
call. A hotspot's ID is its `"lat,lng"` string, which is both its `bbolt`
key and its API path segment (`/api/hotspots/{locId}/...`) — no separate ID
translation anywhere.

## Next refresh (~2027)

Re-run steps 2–6, then `scp` the two rebuilt files over the old ones on
the serving host. The dataset will have moved forward to whatever the
latest complete year is by then (check `facet=year` first to confirm).
No code changes needed unless GBIF's SQL dialect or the EOD dataset key
changes.
