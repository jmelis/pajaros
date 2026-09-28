# Refreshing the bird data

A step-by-step checklist for refreshing the server's offline bird data —
written so it can be followed a year from now with no other context. For
*why* the pipeline is built this way, see `docs/GBIF_DATA_PIPELINE.md`;
this document is only the "how".

Two independent data sources feed the server, both refreshed roughly
yearly, both downloaded once and processed into files — never called at
request time by the deployed server:

| source | what it provides | size | committed to git? |
|---|---|---|---|
| eBird taxonomy API | species names (en/es/fr), classification | ~250KB × 3 languages | yes |
| GBIF (EOD dataset) | which birds occur where, and how often | low single-digit GB | no — `scp`'d to the server |

## Prerequisites (one-time)

- Go toolchain installed.
- An eBird API key: https://ebird.org/api/keygen (free). Export it as
  `EBIRD_API_KEY`.
- A free GBIF account: https://www.gbif.org/user/profile. You'll need the
  **username** (not email) and password.
- Run every command below from a normal home/office network connection,
  **not** from the cloud deployment — eBird's API blocks that IP.
- Don't paste the GBIF password directly into a chat session or command
  history if you can avoid it; a `.netrc` entry or a gitignored env file
  you `source` is safer.

## Step 1: Refresh eBird taxonomy

```bash
cd server
EBIRD_API_KEY=your-key-here go run ./cmd/gensnapshot taxonomy
```

Takes a few seconds — three unpaginated API calls (one per language).
Writes:

- `server/data/taxonomy_en.json.gz`
- `server/data/taxonomy_es.json.gz`
- `server/data/taxonomy_fr.json.gz`

These are small and **are** committed to git (the server `go:embed`s them
into the binary). Commit them normally:

```bash
git add server/data/taxonomy_*.json.gz
git commit -m "Refresh eBird taxonomy"
```

## Step 2: Refresh GBIF hotspot/species data

### 2a. Submit the download request

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

The `ORDER BY` is required, not optional — the build tool in step 2c
depends on the download being sorted by point.

The response body is a download key, e.g. `0010387-260921141020460`. Save
it — you need it for the next step.

### 2b. Wait for it to finish

```bash
curl -s "https://api.gbif.org/v1/occurrence/download/<KEY>"
```

Check the `"status"` field. It goes `PREPARING` → `RUNNING` →
`SUCCEEDED`. This can take anywhere from ~10 minutes to a couple of
hours depending on GBIF's queue — just poll every minute or two. You can
also watch it at `https://www.gbif.org/occurrence/download/<KEY>` in a
browser (make sure the URL includes your key — the same page with no key
is a *different*, unrelated "download the entire GBIF corpus" flow).

Current scale, for reference: ~250 million rows, ~15GB zipped.

### 2c. Download and build

Once `status` is `SUCCEEDED`, the same response has a `downloadLink`:

```bash
curl -sL -o gbif_download.zip "<downloadLink>"
cd server
go run ./cmd/gensnapshot hotspots gbif_download.zip
```

No need to unzip it yourself — the tool reads the `.zip` directly. At
current global scale this takes a few minutes and prints progress every 20
million rows. It writes a single file:

- `server/data/hotspots.bolt`

This is large (low single-digit GB) and `server/.gitignore` already
excludes it — **do not** `git add` it.

### 2d. Deploy the new data

```bash
scp server/data/hotspots.bolt <server-host>:<data-dir>
```

The server opens this file from `HOTSPOTS_DATA_DIR` (default `./data`,
relative to wherever the binary runs) at startup — set that env var to
`<data-dir>` on the serving host if it isn't already `./data`, then restart
the server to pick up the new file. The open itself is close to instant
(bbolt just mmaps the file; nothing is loaded wholesale into memory), so a
restart with fresh data doesn't cost any real startup delay.

## If something looks wrong

- **eBird taxonomy step returns 403**: you're running it from the cloud
  deployment, or an IP eBird has otherwise blocked. Run it from home.
- **GBIF SQL request rejected with a parse error mentioning `year`**:
  `year` is a reserved word in GBIF's SQL dialect; it needs to be
  double-quoted (`"year"`) if a query ever needs to filter by it (the
  query above doesn't).
- **GBIF download stuck on `RUNNING` for a long time**: normal under
  load; just keep polling. No action needed.
- **`gensnapshot hotspots` fails with "input not sorted by point"**: the
  downloaded file isn't actually sorted — double-check the SQL in step 2a
  included `ORDER BY decimalLatitude, decimalLongitude`. The build tool
  depends on this and checks it explicitly rather than silently producing
  wrong data.
