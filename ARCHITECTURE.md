# Architecture

How birdquiz is built and why. For how to run it or refresh its data, see
`README.md`.

## Overview

birdquiz is a Go server that serves a bird learning app: search hotspots
worldwide, browse the species seen at one, and learn them in a full-screen
swipeable card deck (Learn mode). The frontend is a small vanilla-JS
single-page app embedded in the binary. Accounts and preferences live in
SQLite; hotspot and species-popularity data live in a separately distributed
`bbolt` file, built offline from a GBIF dataset. The only upstream calls the
deployed server makes at request time are to Wikimedia and iNaturalist, for
species card images.

## Stack

- **Server**: Go, standard library `net/http` with Go 1.22+ method/path
  routing (`server/main.go`).
- **Accounts & preferences**: SQLite via `modernc.org/sqlite` (pure Go, no
  cgo) — `server/userstore.go`.
- **Hotspot & species data**: `bbolt`, an embedded ordered key-value store,
  mmap-backed — `server/hotspots_data.go`, `server/species_store.go`. See
  "Hotspot and species data" below.
- **Taxonomy**: `go:embed`ded JSON, small enough to ship in the binary —
  `server/taxonomy_data.go` (structure and species names), `server/families.go`
  (family names). See "Taxonomy and common names" below.
- **Frontend**: `server/static/` — plain HTML/CSS/JS, no bundler, no build
  step, served via `go:embed`.
- **Images**: up to four per species, fetched from Wikimedia Commons and
  (to top up) iNaturalist on demand and cached to disk, bucketed across
  subdirectories (`server/cache.go`, `server/wikimedia.go`,
  `server/inaturalist.go`, `server/images.go`). See "Learn mode and species
  images" below.
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

A brand-new account's language preference is seeded from its signup IP's
geolocated country — Spanish-speaking, French-speaking, or (everything
else, including a lookup miss) English — via an embedded, offline-built
IP-to-country table (`server/geoip_data.go`; see "Hotspot and species data"
below for the same offline-snapshot pattern applied here). This only ever
runs once, at `completeLogin` in `server/auth.go`, when the account row
doesn't already exist; every later login leaves an existing preference (set
this way or explicitly in settings) untouched. Lookup misses — most
countries, since the table only records the Spanish/French ones — and IPs
outside the table both resolve the same way: no preference is set, so the
account falls back to `defaultLang` (English) same as always.

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
GET  /api/hotspots/{locId}/species                 species list (category/popularity/alphabetical) — Learn's card deck too

GET  /api/me                                      profile
GET  /PUT /api/me/language                        primary bird-name + UI language
GET  /PUT /api/me/secondary-language               optional subtitle language
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

Hash-based routing (`#/home`, `#/search`, `#/hotspot/<locId>`, `#/settings`)
so every view survives a reload and is linkable without any server-side
routing. Navigation is a bottom tab bar (`<nav class="app-nav tab-bar">` in
`index.html`), the canonical iOS primary-navigation placement. The old
deep-link shape (`/?locId=…&lang=…&mode=…`) still works and lands on that
hotspot's view.

Views: **Home** (saved hotspots, a search shortcut — a welcome screen for a
fresh account), **Search** (place-name search, geolocation, map, "search
this area" — see "Place-name search" below), **Hotspot** (info, save
toggle, explore — by popularity/category/alphabetical — or Learn's
full-screen card deck, see "Learn mode and species images" below),
**Settings** (language, secondary language, sign out).

Two independent language preferences: the **primary** language drives both
bird names (the `lang` query param) and UI text — there's no separate
interface-language setting. The **secondary** language is an optional
subtitle under each bird name.

`style.css` defines the light palette on `:root` and redefines it under
`prefers-color-scheme: dark`, so the app follows the OS theme automatically.

## Taxonomy and common names

eBird supplies the taxonomic *structure* — which species exist
(`server/data/taxonomy_core.json.gz`: `sciName`, `speciesCode`, `order`,
`familyCode`, `taxonOrder`) — from a single `/ref/taxonomy/ebird` call
(`cmd/gensnapshot taxonomy`, any locale, since none of these fields vary by
one). Every common name — species and family both — comes from GBIF
instead, joined onto that structure by scientific name. eBird's own
per-locale common names aren't used anywhere: eBird/Clements' translated
checklist text carries redistribution restrictions its bare taxonomic
structure doesn't, so only the structure is eBird's; every name a user
sees is GBIF's.

GBIF's `species/search` endpoint returns each taxon's vernacular names
inline, so building the name tables needs no bulk archive download — just
many small calls, one per family rather than one giant paginated walk:
paginating GBIF's ~14,600 bird species by a single global offset stalls
badly past roughly 10,000 results (its search backend's offset pagination
degrades hard at depth), but a family rarely has more than a few hundred
species, so paginating within each of Aves' ~488 families instead keeps
every query's offset shallow. `cmd/gensnapshot/taxonomy.go`:

1. Fetches every GBIF family in Aves (with their own inline vernacular
   names — no separate per-family call needed).
2. For each, fetches its member species (with their vernacular names) and
   joins each one to an eBird species by scientific name — roughly 88% of
   eBird's species resolve this way; the rest are taxonomic splits/lumps
   or spelling differences between eBird/Clements and GBIF's backbone that
   a name join can't paper over, and fall back to English, then to the
   species' own scientific name (see `TaxonomyStore.Lookup`).
3. Where several GBIF records offer different strings for the same
   (species, language) or (family, language) pair, one is picked
   deterministically: most-voted first, then shortest, then alphabetical.
4. GBIF's vernacular names carry ISO 639-2/3 three-letter language codes;
   `iso6391ByGBIFCode` maps the ones this app cares about to the
   two-letter codes used everywhere else (`validLang`, the `lang` query
   param, `i18n.json`).

**Species names are gated by coverage; family names aren't.** A language
only becomes a bird-name option (`validLang`, computed from
`data/species_names.json.gz`'s own keys — see `mustComputeValidLang`) if
GBIF names at least 80% of eBird's species in it; below that a language's
coverage drops off sharply; below it, most languages cover under 10%.
Family names have no such gate — GBIF's family-rank coverage doesn't track
its species-rank coverage closely enough to reuse the same cutoff (Spanish,
for instance, clears 80% for species but only two-thirds for families) —
so `FamilyName` (`server/families.go`) just falls back from the requested
locale to English to the family's own scientific name (e.g.
"Struthionidae"), which degrades gracefully regardless of how sparse a
given language's family coverage is.

The **primary language** setting (see Frontend, above) selects a
`validLang` member for both bird names and UI text — there's no separate
interface-language setting. `static/i18n.json` only has full UI-string
translations for English, French and Spanish; the other `validLang`
members render bird and category names in that language with English UI
chrome around them, via `t()`'s existing fallback-to-English behavior — the
same "show what you can, fall back to English for the rest" approach as
the name resolution above, not a special case for these languages.

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
`hotspotSpeciesSource` interface `main.go` calls against — an interface
purely for testability, so tests can hand the server made-up species codes
without touching real data. A hotspot's ID is
its `"lat,lng"` string, which is both its `bbolt` key (in every
hotspot-keyed bucket) and its API path segment (`/api/hotspots/{locId}`) —
no separate ID translation anywhere.

Opening the file is close to instant (bbolt just mmaps it) regardless of
how many hotspots it holds — this replaced an earlier design that loaded
every hotspot into an in-memory map at startup, which took on the order of
a minute at full worldwide scale.

## Place-name search

Search starts from a place name, not a hotspot — the frontend has no
concept of "pick a hotspot ID" as an entry point, only "search this place,
then pick from what's nearby." That two-step shape exists because of
GBIF's point density described above: a raw nearby search inside a real
birding area routinely returns hundreds of points, ~90%+ of them one-off
personal checklist locations rather than real sites (Massachusetts: only
~8% of distinct points are real eBird hotspots). No server-side ranking
makes that honestly resolvable to a single "the" hotspot for a place, so
the UI shows the search's own most-active-first ordering (see "bbolt
schema" above) and lets the person pick.

**Data source: GeoNames, not a live geocoder.** Place names come from
`places.bolt`, built offline from GeoNames' `cities500` gazetteer dump
(every place with population > 500 or that's a seat of local government,
~185K rows worldwide) — the same "download a dataset once, build a local
file, no upstream call at request time" shape as the GBIF pipeline above.
It's licensed CC BY 4.0 (commercial use is fine with attribution), needs no
API key, and keeps place search working even if the deployed server's
outbound network access is ever restricted further. `allCountries.zip`
(~12M rows, mostly geographic features nobody searches for by name) isn't
used — `cities500` covers what a search box actually needs at a fraction of
the size.

### bbolt schema

`places.bolt` — a separate, much smaller (tens of MB) sibling of
`hotspots.bolt`, opened as its own mmap-backed handle. Two buckets:

- **`place_by_name`**: key is the normalized (lowercased, trimmed) name
  followed by a NUL separator and the GeoNames numeric ID, so a prefix scan
  over the bucket's ordered keys is a name search, and colliding names
  (there are many "San Jose"s worldwide) each keep their own entry. The
  value is `{lat, lng, population, countryCode, displayName}`.
- **`place_meta`**: a single count key, mirroring `hotspot_meta`'s reason
  for existing.

Most places are indexed once, under their GeoNames `asciiname`. GeoNames'
own transliteration doesn't always match a simple accent-strip, though — its
`asciiname` for Zürich is "Zuerich" (the German convention), not "Zurich" —
so a place also gets a second index entry under a diacritic-folded reading
of its display name (`é`→`e`, `ü`→`u`, etc.) whenever that differs from the
`asciiname` key, covering the everyday English-keyboard spelling as well.
Names outside that fold table's coverage (non-Latin scripts) are left as
`asciiname` alone.

### Search

`PlaceStore.Search` (`server/places_data.go`) seeks to the query's
normalized-name prefix and walks forward while keys still match, capped at
a few thousand scanned candidates so a very common prefix can't make one
request scan without bound. Candidates are then sorted by population,
most-populous first, and truncated to the response limit — ranking
prominence within whatever the prefix scan actually found, not a true
"biggest place matching this prefix anywhere" guarantee once the scan cap
is hit.

### Wiring

`GET /api/places?q=` (`handlePlaceSearch` in `main.go`) shares the hotspot
search's rate limiter — same class of endpoint, a single local `bbolt`
lookup with no upstream fan-out. On the frontend, typing into the search
view's text input debounces into that endpoint; picking a suggestion sets
the (now hidden, no longer user-facing) lat/lng fields and runs the
existing `/api/hotspots` nearby search, whose full result stays client-side
in `state.hotspotsFull`.

Markers aren't just dumped on the map from that full result — the current
map viewport is the filter. `renderVisibleHotspots` (`app.js`) shows only
the busiest `HOTSPOT_MARKERS_DEFAULT` hotspots that fall inside
`map.getBounds()`, and is wired to Leaflet's `moveend` event, so panning or
zooming re-filters live rather than needing a manual "show more" step. This
keeps whatever's on screen from being dominated by GBIF's noise without a
second network round-trip — everything it filters is already in
`state.hotspotsFull` from the one `/api/hotspots` call.

## Learn mode and species images

Learn is a full-screen, swipeable deck of a hotspot's species — pure
browsing, no scoring or spaced repetition. The frontend fetches
`GET /api/hotspots/{locId}/species?mode=popularity` (the same endpoint and
ordering Browse's popularity mode uses) and drives its own card, dot-index,
and drag/press/arrow-key navigation client-side (`static/app.js`'s `learn`
object) — there's no separate session endpoint or server-side state for it.

Each card shows up to `maxImagesPerSpecies` (4) images, sourced from two
upstreams and cached to disk. Wikimedia Commons (`server/wikimedia.go`)
contributes at most one: the Wikipedia infobox photo, filtered to
redistributable licenses and to the file's own Commons categories (a
distribution map or a statue's photo can be named anything, but ends up
categorized as "... distribution maps" or "Statues of ..." regardless, so
checking categories catches what a filename can't). It's the one Commons
image trusted without a human rechecking it — everything else in a
species' Commons category is just a free-text tag any contributor can add,
with nothing enforcing that it actually depicts the species; real species
categories have turned up entirely unrelated birds this way. iNaturalist
(`server/inaturalist.go`) supplies the rest, for every species — research
-grade observations (each tied to a specific community-identified sighting,
a guarantee a category tag never had), community-vote-ordered, filtered to
the same license set, one photo per observation so a single photographer's
observation can't crowd out a species' image set. `images.go`'s
`ResolveImages` orchestrates the two; `cache.go` downloads, resizes
(`imageresize.go`, capped to `maxImageWidth`/1600px regardless of source),
and caches the results under `server/cache/<bucket>/`, sharded into
`cacheBucketCount` (256) hash-based subdirectories so one directory never
has to hold every species' files directly.

**Fetching is two-tier**, so a live request is never stuck behind a whole
species' worth of image fetching. A Browse/Learn request for a hotspot's
species list (`handleHotspotSpecies`) calls `EnsureFetchedAsync` per
species, which dispatches into one of two independent background queues
rather than blocking the request:
- **First image** (`imageFetchConcurrency`, 8 concurrent): a species with no
  cached image yet gets just its first one fetched here — cheap, one source
  lookup and one download, so a hotspot full of never-seen species starts
  showing *something* for every card as fast as possible.
- **Top-up** (`topUpConcurrency`, 3 concurrent, its own semaphore): once a
  species has its first image, it's handed to this smaller, lower-priority
  queue to fetch the rest (up to `maxImagesPerSpecies`) — several source
  lookups and downloads, deliberately not competing with other species'
  first-image fetches for the same 8 hot-path slots. A `.topped` marker
  records that a species' top-up was attempted (whether or not it reached
  the full count), so a species that naturally has fewer than
  `maxImagesPerSpecies` available isn't re-queued on every later request.

`EnsureFetched` (no `Async`) is the synchronous version of the same two
steps, for offline batch tools (`warmcache`, `migrateimages`) that want a
species fully resolved before moving to the next one rather than firing
into the background.

An existing cache built before per-species multi-image support (one image
directly under `server/cache/`, no buckets) is upgraded automatically, every
time the server starts (`migrateOldCacheLayout` in `server/migrateimages.go`,
called from `main()` right after the cache opens, before the server starts
listening): each already-downloaded image is moved into its bucket — a
rename, no re-fetching, no network call at all — leaving that species
exactly as if it had just gotten its first image live, so the existing
background top-up queue picks up the rest once it's actually viewed. This is
pure local disk I/O, so it finishes in well under a second even for
hundreds of species — the deployment's own readiness probe is the only
"maintenance window" this needs; nothing else has to wait for it. Idempotent
and silent once there's nothing old-format left: a species whose bucket
already has its first image is left alone (its now-redundant old flat file
is just removed), and a cache with no old-format entries at all returns
immediately without logging anything.

Every rename/removal is logged twice — via the normal logger (`kubectl
logs` etc.) and appended to `<CACHE_DIR>/migration.log`, timestamped — so a
migration stays auditable, and reversible by hand, even after pod logs have
rotated away or the pod that ran it is gone.

`go run . migrateimages` is the same logic run as a one-off CLI mode,
for when you'd rather eagerly top every migrated species up toward
`maxImagesPerSpecies` from both sources right away (including species that
used to carry a permanent "no image found" marker from the Wikimedia-only
era, since iNaturalist may now find something) instead of waiting for
organic traffic to view them.

The cache is otherwise fetch-once: a species that's already fully resolved
is never revisited by ordinary traffic, so a sourcing policy change (like
Wikimedia dropping its Commons-category walk) only affects species fetched
afterward. `go run . revalidateimages` (`server/revalidateimages.go`) is the
one-off catch-up for that — it walks every species already in the cache and
drops any cached Commons image that isn't (or is no longer) that species'
current Wikipedia infobox photo, regardless of whether the dropped photo was
individually fine, then tops back up from iNaturalist to replace it (existing
iNaturalist images are always left alone — not in scope for this policy).
Slots are positional (`<slug>-N.jpg`), so a species with a dropped slot has
every one of its slots rewritten rather than just the changed one.

## Accounts & persistence

`server/userstore.go` reconciles its SQLite schema at startup, additively
and idempotently: it creates any missing tables and adds missing columns
via `ALTER TABLE`, without touching existing rows — so a database whose
`user_preferences` predates a later column (e.g. `secondary_language`)
repairs itself on the next startup, no migration step needed.

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
