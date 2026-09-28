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
download, processed into a single file the server reads at a configured
path — not `go:embed`, not committed to git. It's `scp`'d to the serving
host directly (it's large; see storage format below). No GBIF or eBird
calls happen at request time — see "How it's wired in" below for the
concrete pieces.

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

**Storage format**: a single `bbolt` key-value store (`hotspots.bolt`) with
four buckets, all keyed and read the same way — a pure key-value lookup,
never scanned or joined, so a relational layer (SQLite) buys nothing here.
`bbolt` is mmap-backed, so opening the file doesn't load anything into RAM;
each lookup pages in only the data it touches, independent of how many
hotspots exist worldwide (currently ~20 million).

- `species_by_hotspot`: hotspot ID (`"lat,lng"`) -> a binary blob of
  `[uint16 speciesID, varint count]` pairs sorted by count descending.
  Measured against Massachusetts's ~13K hotspots, this binary encoding
  (4.2MB) beats SQLite's normalized 3-table schema (5.3MB gzipped) and
  JSON-encoded `bbolt` values (16.8MB — per-value compression can't dedupe
  the same ~11K repeated species names across independent blobs the way
  one shared gzip stream can), because it stores integer species IDs
  instead of repeated scientific-name text.
- `hotspot_by_id`: hotspot ID -> an encoded `{lat, lng, name, totalCount}`
  record (lat/lng as float64, totalCount as a varint, name as the
  remaining bytes — the ID itself isn't repeated in the value, since it's
  already the bucket key). Backs `HotspotStore.Info`, a single `Get`.
- `hotspot_by_grid_cell`: encoded 1-degree grid cell (see
  `server/hotspots_data.go`'s `gridCell`/`cellFor`/`cellKey`) -> every
  hotspot in that cell, packed back-to-back as
  `{id, lat, lng, name, totalCount}` entries (here the ID *is* repeated per
  entry, since a cell's value holds many different hotspots). Backs
  `HotspotStore.Nearby`: cheap enough to fetch every grid cell a search
  radius could reach with a handful of direct `Get`s, entries decoded and
  haversine-filtered from there — see "How it's wired in" below. Full
  entries are stored here rather than just IDs so `Nearby` never needs a
  second `Get` per candidate hotspot.
- `hotspot_meta`: a couple of small fixed keys, currently just the total
  hotspot count, so `HotspotStore.Len()` is a single lookup instead of a
  full bucket scan.

At global scale this lands in the low single-digit GB — trivial to `scp`.

**No minimum-activity threshold is applied** — every distinct point
becomes a hotspot (on the order of 20 million worldwide). What's "worth
showing" by default (a map view, a search result) is a server-side query
concern, not a build-time one — it can change without rerunning the GBIF
download.

**Build algorithm**: because the input is sorted by point, this is a
single streaming pass with memory bounded by one point at a time for most
of the work, and by one latitude band at a time for the grid index —
never by total row or point count:

1. Read the file line by line. Accumulate the *current* point's data only
   — species counts (`speciesID → count`), locality counts, and a running
   total. The moment the `(lat, lng)` key changes, the previous point is
   complete: encode its species map as the sorted binary blob
   (`species_by_hotspot`) and its `{lat, lng, name, totalCount}` as a
   `hotspot_by_id` value, then reset the accumulator for the new point.
   (A cheap guard checks each new point's coordinates are `>=` the
   previous one — if GBIF's output ever weren't actually sorted, this
   fails loudly with a clear error instead of silently mis-grouping data.)
2. Resolve each `species` (scientific name) to a `uint16` ID via a table
   built from the embedded taxonomy, sorted by eBird `speciesCode` for a
   stable mapping — both the build tool and the server compute this
   independently from the same committed taxonomy file, so nothing extra
   needs to ship to keep them in sync.
3. Each completed point is also assigned to its 1-degree grid cell and
   appended to that cell's in-memory entry buffer. Because the input is
   sorted by latitude, a point's grid cell latitude band never decreases —
   once processing moves past a band, no later point can fall back into
   it. That makes "the band just finished" a safe, bounded-memory flush
   point: every cell touched by that band gets written to
   `hotspot_by_grid_cell` (one `Put` per cell, in ascending cell-key order)
   and its buffer is discarded, so at most one band's worth of points is
   ever held in memory — a small fraction of the worldwide total, not the
   whole dataset.
4. Completed points' `species_by_hotspot` and `hotspot_by_id` entries are
   batched (500,000 at a time) into single `bbolt` transactions, written
   as fresh keys in ascending order — no read-modify-write, since sorted
   input guarantees each point's ID is only ever seen once.
5. At the end, the total point count is written to `hotspot_meta`, and
   `hotspots.bolt` is `scp`'d to the serving host; point the server at its
   directory via config/env var.

At current global scale this runs in a few minutes.

Implemented in `server/cmd/gensnapshot/hotspots.go`.

### 6. How it's wired in

The server makes zero external calls at request time — Wikimedia (for
images) is the only upstream API left. `main.go` reads `HOTSPOTS_DATA_DIR`
(default `./data`) at startup and opens `hotspots.bolt` once, read-only, as
a single shared `*bbolt.DB` handle: `HotspotStore` (`hotspots_data.go`)
answers `Info`/`Nearby`/`Len` from its `hotspot_by_id`/`hotspot_by_grid_cell`/
`hotspot_meta` buckets, and `SpeciesStore` (`species_store.go`) answers
`Lookup(hotspotID)` from the sibling `species_by_hotspot` bucket — both
mmap-backed, so opening the file is close to instant and nothing is ever
fully loaded into memory regardless of how many hotspots exist worldwide.
`SpeciesResolver` (`species_resolver.go`) sits on top of `SpeciesStore`,
implementing the `hotspotSpeciesSource` interface `main.go`/`quiz.go` call.
A hotspot's ID is its `"lat,lng"` string, which is both its `bbolt` key (in
every bucket keyed by hotspot) and its API path segment
(`/api/hotspots/{locId}/...`) — no separate ID translation anywhere.

## Next refresh (~2027)

Re-run steps 2–6, then `scp` the rebuilt file over the old one on
the serving host. The dataset will have moved forward to whatever the
latest complete year is by then (check `facet=year` first to confirm).
No code changes needed unless GBIF's SQL dialect or the EOD dataset key
changes.
