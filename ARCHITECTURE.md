# Architecture

How pajaros is built and why. For how to run it or refresh its data, see
`README.md`.

## Overview

pajaros is a Go server that serves a bird quiz app: search hotspots
worldwide, browse the species seen at one, quiz yourself on them with
spaced repetition, track progress per account. The frontend is a small
vanilla-JS single-page app embedded in the binary. Accounts and progress
live in SQLite; hotspot and species-popularity data live in a separately
distributed `bbolt` file, built offline from a GBIF dataset. The only
upstream call the deployed server makes at request time is to Wikimedia,
for species card images.

## Stack

- **Server**: Go, standard library `net/http` with Go 1.22+ method/path
  routing (`server/main.go`).
- **Accounts & progress**: SQLite via `modernc.org/sqlite` (pure Go, no
  cgo) — `server/userstore.go`.
- **Hotspot & species data**: `bbolt`, an embedded ordered key-value store,
  mmap-backed — `server/hotspots_data.go`, `server/species_store.go`. See
  "Hotspot and species data" below.
- **Taxonomy**: `go:embed`ded JSON, small enough to ship in the binary —
  `server/taxonomy_data.go`.
- **Frontend**: `server/static/` — plain HTML/CSS/JS, no bundler, no build
  step, served via `go:embed`.
- **Images**: fetched from Wikimedia Commons on demand and cached to disk
  (`server/cache.go`, `server/wikimedia.go`).
- **Deploy**: a `distroless/static` container (`Containerfile`) holding just
  the statically-linked binary, built and pushed via the root `Makefile`
  to a sibling GitOps repo.

## Authentication

Two independent sign-in providers, each turned on explicitly:

- `GOOGLE_AUTH_ENABLED` + `GOOGLE_CLIENT_ID`/`GOOGLE_CLIENT_SECRET`
- `APPLE_AUTH_ENABLED` + `APPLE_TEAM_ID`/`APPLE_SERVICES_ID`/`APPLE_KEY_ID`/`APPLE_PRIVATE_KEY`

Both need `OAUTH_REDIRECT_BASE_URL` (the externally reachable base URL) for
their callback. Setting an `_ENABLED` flag without its provider's complete
credentials is a fatal startup error naming what's missing.

With **neither** flag set, the server runs in open/development mode: every
route is reachable with no session, and every request acts as a single
fixed account (`dev:local`) so the account-preference routes still work
locally. `make server-open` forces this regardless of what's in the
environment; `make server` honors whatever's actually set.

Sessions are signed cookies (`server/session.go`), keyed by `SESSION_SECRET`
— if unset, a random key is generated at startup, so sessions don't survive
a restart (fine for local dev, not for production). The login page
(`server/static/login.html`) is served in English regardless of any account
preference, since it's shown before any preference is known.

## Server endpoints

```
GET  /healthz                                    liveness/readiness, ungated

GET  /api/hotspots                               nearby search (lat, lng, dist)
GET  /api/hotspots/{locId}                        hotspot info
GET  /api/hotspots/{locId}/species                 species list (category/popularity/alphabetical)
GET  /api/hotspots/{locId}/quiz                    build a quiz session

GET  /api/me                                      profile
GET  /PUT /api/me/language                        primary bird-name + UI language
GET  /PUT /api/me/secondary-language               optional subtitle language
GET  /PUT /api/me/star-rewards                     star-reward display toggle
GET  /api/me/progress                             quiz progress
POST /api/me/progress/{speciesCode}                submit a quiz answer
DELETE /api/me/progress                            reset all progress
GET  /api/me/mastered                             mastered species (local data only)
GET  /api/me/favorites                            saved hotspots (capped at 100/account)
PUT/DELETE /api/me/favorites/{locId}               save/remove a hotspot

GET  /login, POST /auth/logout                    (only when a provider is enabled)
GET  /auth/{google,apple}[/callback]               OAuth flow
```

Two independent rate-limit layers guard the hotspot routes: a global
per-upstream limiter (`server/ratelimit.go`, currently just
`WIKIMEDIA_RPS`) caps total outbound traffic across all clients, and a
per-account keyed limiter (`server/ipratelimit.go`) caps how much of that
shared budget one account can burn — tighter for the detail views (species
lookups can fan out to Wikimedia, one request per unseen species) than for
search (a single local `bbolt` lookup, no upstream fan-out at all). In open
mode there's no account to key by, so it falls back to client IP instead of
the single fixed dev account, which would otherwise rate-limit everyone
collectively.

## Frontend

Four pieces served straight from `server/static/`, embedded in the binary,
no build step: `index.html` (markup), `style.css`, `app.js`, `i18n.json`
(en/es/fr UI strings).

Hash-based routing (`#/home`, `#/search`, `#/hotspot/<locId>`, `#/mastered`,
`#/settings`) so every view survives a reload and is linkable without any
server-side routing. Navigation lives in the header (`<nav class="app-nav">`
in `index.html`) rather than a bottom bar. The old deep-link shape
(`/?locId=…&lang=…&mode=…`) still works and lands on that hotspot's view.

Views: **Home** (saved hotspots, a search shortcut, progress summary — a
welcome screen for a fresh account), **Search** (location/geolocation/map,
"search this area"), **Hotspot** (info, save toggle, explore — by
popularity/category/alphabetical — or quiz with a size picker), **Mastered**
(mastered species as cards, same style as explore), **Settings** (language,
secondary language, star rewards, reset, sign out).

Two independent language preferences: the **primary** language drives both
bird names (the `lang` query param) and UI text — there's no separate
interface-language setting. The **secondary** language is an optional
subtitle under each bird name. **Star rewards** is a single boolean
(default off) controlling only the celebratory UI (stars, the burst
animation, the visible counter) — quiz mechanics (boxes, due dates,
mastery) are unaffected either way.

`style.css` defines the light palette on `:root` and redefines it under
`prefers-color-scheme: dark`, so the app follows the OS theme automatically.

## Hotspot and species data

### Why GBIF, not live eBird

`api.ebird.org` blocks requests from this project's cloud egress IP
(verified: the same key and request work from a residential connection and
return 403 from the deployed pod — a common anti-scraping measure against
hosting-provider ranges). eBird's own observation data is mirrored on GBIF
as a dataset called EOD (eBird Observation Dataset,
`datasetKey = 4fa7b334-ce0d-4e88-aaae-2e0c138d049e`), and GBIF is not
blocked from the same pod. So instead of calling eBird live, the server
reads from a single bulk GBIF download, processed offline into one file —
no GBIF or eBird call happens at request time either; Wikimedia (species
images) is the only upstream API left.

Properties of this dataset that shape the design:

- **Annual static snapshot, not a rolling feed.** One EOD dataset key
  exists on GBIF; whatever the latest complete year is when queried is
  what you get. It advances roughly once a year when Cornell republishes.
- **No eBird hotspot ID in the mirror.** Records carry `locality` (free
  text) and `decimalLatitude`/`decimalLongitude`, but no `locationID` —
  there's no clean join key back to an eBird `L123456` hotspot ID, and
  coordinates don't round-trip to eBird's own numbers at full precision
  (`41.4869773, -71.0375977` in eBird appears as `41.486977, -71.0376` in
  GBIF's mirror — agreement to ~5 decimal places, inconsistently per axis).

**Data model: GBIF's own points are the hotspots.** Each distinct
`(decimalLatitude, decimalLongitude, locality)` in GBIF's EOD data is a
hotspot for this app's purposes — not eBird's curated hotspot list. Joining
onto eBird's hotspot list by coordinate doesn't recover enough of it to be
worth the complexity (even a 50m haversine match only finds 87.8% of
Massachusetts's real hotspots, since GBIF's missing ID means "General
Area"-style hotspots can't be reliably matched). Most distinct points are
one-off personal locations rather than real birding sites (in
Massachusetts: 48,735 distinct points, only 3,920 are real eBird hotspots,
~8%) — every point is kept regardless of activity level; what's "worth
showing" by default is a query-time concern, not a build-time one.

### bbolt schema

A single `bbolt` file, `hotspots.bolt` — mmap-backed, so opening it doesn't
load anything into RAM, and each lookup only pages in the data it touches.
Not `go:embed`'d or committed to git (low single-digit GB): it's `scp`'d to
the serving host and read from `HOTSPOTS_DATA_DIR`. Four buckets, all pure
key-value lookups (never scanned or joined — a relational layer buys
nothing here):

- **`species_by_hotspot`**: hotspot ID (`"lat,lng"`) → a binary blob of
  `[uint16 speciesID, varint count]` pairs, sorted by count descending.
  Backs `SpeciesStore.Lookup`. Integer species IDs (not repeated
  scientific-name text) keep this compact — measured against
  Massachusetts's ~13K hotspots, this encoding (4.2MB) beats a normalized
  SQLite schema (5.3MB gzipped) and JSON-encoded `bbolt` values (16.8MB).
- **`hotspot_by_id`**: hotspot ID → an encoded `{lat, lng, name,
  totalCount}` record (the ID itself isn't repeated in the value, since
  it's already the bucket key). Backs `HotspotStore.Info`, a single `Get`.
- **`hotspot_by_grid_cell`**: an encoded 1-degree grid cell (see
  `gridCell`/`cellFor`/`cellKey` in `hotspots_data.go`) → every hotspot in
  that cell, packed back-to-back as `{id, lat, lng, name, totalCount}`
  entries (here the ID *is* repeated, since one cell holds many
  hotspots). Backs `HotspotStore.Nearby`: fetch every grid cell a search
  radius could reach with a handful of direct `Get`s, decode, and
  haversine-filter — full entries are stored here (not just IDs) so
  `Nearby` never needs a second `Get` per candidate.
- **`hotspot_meta`**: a couple of small fixed keys, currently just the
  total hotspot count, so `HotspotStore.Len()` is a single lookup instead
  of a full bucket scan.

At global scale (~20 million hotspots) this lands in the low single-digit
GB. Every `Put` during the build happens in strictly ascending key order
with `FillPercent = 1.0` (bbolt's default `0.5` leaves half of each page
free for future random-order inserts, which never happens here — every key
is written exactly once and never touched again) — roughly halving on-disk
size versus the default.

### Build algorithm (`server/cmd/gensnapshot/hotspots.go`)

The input is a GBIF SQL Download: `decimallatitude`, `decimallongitude`,
`locality`, `species`, `n`, `ORDER BY decimalLatitude, decimalLongitude`.
Because it's sorted, the build is a single streaming pass with memory
bounded well below total row or point count — no giant in-memory map of
the ~20 million worldwide hotspots at any point (species-level state) or
close to it (grid-level state):

1. Accumulate the *current* point's species counts, locality counts, and
   running total. The moment `(lat, lng)` changes, the point is complete:
   encode it into `species_by_hotspot` and `hotspot_by_id`, then reset. (A
   cheap guard checks each new point's coordinates are `>=` the previous
   one — if the input weren't actually sorted, this fails loudly instead
   of silently mis-grouping data.)
2. Resolve each `species` (scientific name) to a `uint16` ID via a table
   built from the embedded taxonomy, sorted by eBird `speciesCode` — both
   the build tool and the server compute this independently from the same
   committed taxonomy file, so nothing extra ships to keep them in sync.
3. Each completed point is also assigned to its grid cell and appended to
   that cell's in-memory buffer. Since latitude only increases as the file
   is read, a point's grid latitude band never decreases — once
   processing moves past a band, nothing later can fall back into it. That
   makes "the band just finished" a safe flush point: every cell it
   touched gets written to `hotspot_by_grid_cell` and the buffer is
   discarded, so at most one band's worth of points is ever held at once —
   a small fraction of the worldwide total.
4. `species_by_hotspot`/`hotspot_by_id` entries are batched (500,000 at a
   time) into single `bbolt` transactions — no read-modify-write, since
   sorted input guarantees each point's ID is seen exactly once.
5. The total point count is written to `hotspot_meta` at the end.

### Wiring

`main.go` opens `hotspots.bolt` once, read-only, as a single shared
`*bbolt.DB` handle: `HotspotStore` answers `Info`/`Nearby`/`Len` from its
three buckets, `SpeciesStore` answers `Lookup(hotspotID)` from the sibling
bucket. `SpeciesResolver` sits on top of `SpeciesStore`, implementing the
`hotspotSpeciesSource` interface `main.go`/`quiz.go` call against — an
interface purely for testability, so quiz/progress tests can hand the
server made-up species codes without touching real data. A hotspot's ID is
its `"lat,lng"` string, which is both its `bbolt` key (in every
hotspot-keyed bucket) and its API path segment (`/api/hotspots/{locId}`) —
no separate ID translation anywhere.

Opening the file is close to instant (bbolt just mmaps it) regardless of
how many hotspots it holds — this replaced an earlier design that loaded
every hotspot into an in-memory map at startup, which took on the order of
a minute at full worldwide scale.

## Quiz mechanics

A 5-box Leitner spaced-repetition system over eBird species codes
(`server/quiz.go`). Card identity is the species code; progress is global
per account, not scoped to a hotspot (`userstore.go`'s `card_progress`
table). Box → next-due interval: `[0, 0, 1, 3, 7, 21]` days — box 1 is due
next session, box 5 is "mastered". A session mixes due cards with new ones,
ordering new cards by the hotspot's popularity data so a quiz favors
commonly-seen species first.

## Accounts & persistence

`server/userstore.go` reconciles its SQLite schema at startup, additively
and idempotently: it creates any missing tables and adds missing columns
via `ALTER TABLE`, without touching existing rows — so a database whose
`user_preferences` predates a later column (e.g. `secondary_language` or
`star_rewards`) repairs itself on the next startup, no migration step
needed. Resolved taxonomy (`species_taxonomy`) is persisted too, so the
mastered view never needs a network call.

## Deployment

The image (`Containerfile`) is `distroless/static` plus the statically
linked binary — no shell, ~2MB base. `distroless/static` rather than
`scratch`: the server makes outbound HTTPS calls (Wikimedia, Google's OAuth
token endpoint), and `scratch` carries no CA bundle, so certificate
verification would fail. The binary is cross-compiled on the host
(`make server-linux`, `CGO_ENABLED=0`, pure-Go SQLite driver — no
toolchain needed) rather than inside the image build.

`make deploy` builds, pushes to `quay.io/jmelis/birdquiz`, and rewrites the
image tag in a sibling GitOps repo's manifest — ArgoCD deploys what's
committed there, not what's on disk, so the target prints the `git commit`
command rather than running it.
