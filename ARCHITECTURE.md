# Architecture

How birdsnearby is built and why. For how to run it or refresh its data, see
`README.md`.

## Overview

birdsnearby is a Go server that serves a bird learning app: search hotspots
worldwide, browse the species seen at one, and learn them in a full-screen
swipeable card deck (Learn mode). The frontend is a small vanilla-JS
single-page app embedded in the binary. Accounts and preferences live in
SQLite; hotspot and species-popularity data live in a separately distributed
`bbolt` file, built offline from a GBIF dataset. The only upstream calls the
deployed server makes at request time are to Wikimedia, for species card
images.

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
- **Images**: up to four per species, fetched from Wikimedia (Wikipedia
  infobox photo and Commons quality images) on demand and cached to disk,
  bucketed across subdirectories (`server/cache.go`, `server/wikimedia.go`,
  `server/images.go`). See "Learn mode and species
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

The site is readable by anyone: hotspot, species, compare, credits and
static routes need no session. Only the account routes (`/api/me*`) require
sign-in — `Auth.gate` attaches the account to the request context when a
valid session cookie is present (and never blocks), and `Auth.require` /
`requireFunc` wrap each account route, answering 401 JSON to guests.
Saving a hotspot is the one thing guests can't do; the frontend sends them
to `/login?next=…` when they tap the save star.

With **neither** flag set, the server runs in open/development mode: every
request acts as a single fixed account (`dev:local`), so the account routes
work locally without any login. `make server-open` forces this regardless of what's in the
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
preference, since it's shown before any preference is known. Signing out
returns to `/`, not the login page.

## Server endpoints

```
GET  /healthz                                    liveness/readiness, ungated

GET  /api/hotspots                               nearby search (lat, lng, dist)
GET  /api/hotspots/{locId}                        hotspot info
GET  /api/hotspots/{locId}/species                 species list (category/popularity/alphabetical/seasonality; optional month=1-12, ignored by seasonality) — Learn's card deck too
GET  /api/hotspots/{locId}/credits                 photo credits for the hotspot's species, or one with ?species=<code> (author, license, source)
GET  /api/compare                                 two hotspots side by side (a, b, lang)
GET  /api/contact                                 whether the contact form is configured ({"enabled": bool})
GET  /api/analytics                               Umami script URL + site id (UMAMI_SCRIPT_URL, UMAMI_WEBSITE_ID; empty unless both set)

GET  /api/me                                      profile
GET  /PUT /api/me/language                        primary bird-name + UI language
GET  /PUT /api/me/secondary-language               optional subtitle language
GET  /api/me/favorites                            saved hotspots (capped at 100/account)
PUT/DELETE /api/me/favorites/{locId}               save/remove a hotspot
POST /api/contact                                 email a message to the site owner (see "Contact form")

GET  /login, POST /auth/logout                    (only when a provider is enabled)

Everything above is public except `/api/me*` and `POST /api/contact`, which need a session.
GET  /auth/{google,apple}[/callback]               OAuth flow
```

Two independent rate-limit layers guard the hotspot routes: a global
per-upstream limiter (`server/ratelimit.go`, currently just
`WIKIMEDIA_RPS`) caps total outbound traffic across all clients, and a
keyed limiter (`server/ipratelimit.go`) caps how much of that
shared budget one client can burn — tighter for the detail views (species
lookups can fan out to Wikimedia, one request per unseen species) than for
search (a single local `bbolt` lookup, no upstream fan-out at all). The key is
the account id for signed-in requests and the client IP for guests and in
open mode (where every request shares the single fixed dev account, which
would otherwise rate-limit everyone collectively).

## Frontend

Four pieces served straight from `server/static/`, embedded in the binary,
no build step: `index.html` (markup), `style.css`, `app.js`, `i18n.json`
(en/es/fr UI strings).

Hash-based routing (`#/home`, `#/search`, `#/hotspot/<locId>`,
`#/hotspot/<locId>/bird/<speciesCode>`, `#/hotspot/<locId>/bird/<speciesCode>/credits`,
`#/compare/<locIdA>/<locIdB>`, `#/about`, `#/settings`)
so every view survives a reload and is linkable without any server-side
routing. Hotspot, bird and credits routes take optional `?month=1-12` and
`?sort=<popularity|category|alphabetical|seasonality>` after the path (joined
with `&`); they set the month selector and sort menu (a link without them keeps
the current selection) and the app keeps them in the hash as the controls
change (the default sort is left out), so shared links open on the same view
and Learn links on the same month's birds. Navigation is a bottom tab bar (`<nav class="app-nav tab-bar">` in
`index.html`), the canonical iOS primary-navigation placement. The old
deep-link shape (`/?locId=…&lang=…&mode=…`) still works and lands on that
hotspot's view.

Views: **Home** (saved hotspots, a search shortcut — a welcome screen for a
fresh account), **Search** (place-name search, geolocation, map, "search
this area" — see "Place-name search" below), **Hotspot** (info, save
toggle, explore — by popularity/category/alphabetical/seasons — or Learn's
full-screen card deck, see "Learn mode and species images" below),
**Settings** (language, secondary language, sign out), and a small
**About** page, linked only from Settings: a contact form (see "Contact
form"), every data source with its licence, and a short note on what an
account stores.

**Contact form.** Visitors reach the site owner without the owner's address
appearing anywhere in the page or source. A signed-in account posts a message
to `POST /api/contact` (`server/contact.go`); the server emails it through
Resend's HTTP API from `CONTACT_FROM` (an address on a Resend-verified domain)
to `CONTACT_TO`, with the account's own email as `Reply-To` so replying goes
straight to the sender. Requiring a session is what keeps bots out without a
captcha, and the endpoint adds a per-account limit (burst 3, 3 an hour,
per process like the other limiters), a 4000-character cap, and a
`Content-Type: application/json` requirement that a cross-site form post can't
satisfy. The feature is on only when `RESEND_API_KEY`, `CONTACT_FROM` and
`CONTACT_TO` are all set; otherwise `GET /api/contact` reports
`enabled: false` and the About page omits the section. Guests see a "sign in
to send a message" link in its place. Resend's error detail is logged, never
returned to the client.

**Analytics.** Usage is measured with a self-hosted Umami, which is
cookieless and anonymous. The frontend loads its script only when
`/api/analytics` returns both values, so the deployment decides whether
tracking exists. Auto-tracking is off because the app is hash-routed:
`renderRoute` reports each view as a page (`/hotspot/<locId>`, `/compare/...`)
and `trackEvent` reports `bookmark`, `share-hotspot` and `learn-start`.
Account details (email, user id) are never sent to it, which is what keeps
it outside cookie-consent requirements; the About page's privacy note
says so.

Guests (`state.signedIn === false`, set when `/api/me` answers 401) get the
same views with three differences: the header shows a "Sign in" link instead
of the email, Home shows only the Find button (no saved hotspots; features behind sign-in prompt for it when used),
and the language choices are stored in `localStorage` (falling back to the
browser language, then English) rather than on an account.

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
one). Common names come from two other sources, joined onto that structure
by scientific name: the **Multilingual IOC World Bird List** (species names)
and **GBIF** (species names IOC lacks, and all family names). eBird's own
per-locale common names aren't used anywhere: eBird/Clements' translated
checklist text carries redistribution restrictions its bare taxonomic
structure doesn't, so only the structure is eBird's.

**Species names: IOC first, GBIF as fallback.** The IOC list
(`worldbirdnames.org`, CC BY 3.0, attributed in Settings) is one
downloaded `.xlsx` with a single curated name per species in ~43 languages,
passed to `gensnapshot taxonomy` on the command line
(`cmd/gensnapshot/ioc.go`; `iocLangByColumn` maps its column headers to this
app's two-letter codes). For every (species, language) IOC has, that name
is used as is. It covers ~98% of eBird's species by scientific name; the
remainder are taxonomic splits/lumps IOC and eBird/Clements name
differently, and fall through to GBIF.

GBIF names are messier, which is why they're the fallback rather than the
primary: each species' vernacular names are pooled from many contributing
checklists that mix regional variants and disagree on spelling, so a
species can end up with a coin-flip between two names (Spanish for
*Branta canadensis* splits between "Ganso canadiense" and "Barnacla
canadiense"). Measured against eBird's `es_ES` names, IOC's Spanish names
agree on ~92% of species and GBIF's voted names on ~71%; the same ordering
holds in the other languages eBird also translates. IOC has one Spanish
variant (the Spain-leaning one) and no per-country variants, and neither
does GBIF, whose `species/search` records carry only a language code.

GBIF's `species/search` endpoint returns each taxon's vernacular names
inline, so building the name tables needs no bulk archive download — just
many small calls, one per family rather than one giant paginated walk:
paginating GBIF's ~14,600 bird species by a single global offset stalls
badly past roughly 10,000 results (its search backend's offset pagination
degrades hard at depth), but a family rarely has more than a few hundred
species, so paginating within each of Aves' ~488 families instead keeps
every query's offset shallow. `cmd/gensnapshot/taxonomy.go`:

1. Loads the IOC names, then fetches every GBIF family in Aves (with their
   own inline vernacular names — no separate per-family call needed).
2. For each, fetches its member species (with their vernacular names) and
   joins each one to an eBird species by scientific name — roughly 88% of
   eBird's species resolve this way; the rest are taxonomic splits/lumps
   or spelling differences between eBird/Clements and GBIF's backbone that
   a name join can't paper over.
3. Where several GBIF records offer different strings for the same
   (species, language) or (family, language) pair, one is picked
   deterministically: most-voted first (pooled across case and accent
   variants), then shortest, then alphabetical. An IOC name overrides this
   pick for its (species, language).
4. GBIF's vernacular names carry ISO 639-2/3 three-letter language codes;
   `iso6391ByGBIFCode` maps the ones this app cares about to the
   two-letter codes used everywhere else (`validLang`, the `lang` query
   param, `i18n.json`).

A species with no name in the requested language falls back to English,
then to its own scientific name (see `TaxonomyStore.Lookup`).

**Species names are gated by coverage; family names aren't.** A language
only becomes a bird-name option (`validLang`, computed from
`data/species_names.json.gz`'s own keys — see `mustComputeValidLang`) if
IOC and GBIF together name at least 80% of eBird's species in it; below
that a language's coverage drops off sharply (Serbian sits just under at
~73%; most other languages cover under 10%). That yields 23 languages:
`ca cs da de en eo es fi fr hr it ja lt nb nl pl pt ru sk sv tr uk zh`,
mirrored by `VALID_LANGS` in `static/app.js` (a test checks the two lists
and their `lang.*` labels agree). Family names come from GBIF alone and
have no such gate — GBIF's family-rank coverage doesn't track its
species-rank coverage closely enough to reuse the same cutoff (Spanish,
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
- **Records carry a month, so seasonality is queryable.** Every record has
  its observation date; the build keeps the calendar month (all years
  pooled) per species per hotspot, so "which birds are typical here in
  October" is answerable. The dataset ends at the last complete year, so
  there is no "past few weeks" view, only a time-of-year one.
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

A single `bbolt` file, `hotspots_seasonal.bolt` — mmap-backed, so opening it doesn't
load anything into RAM, and each lookup only pages in the data it touches.
Not `go:embed`'d or committed to git (low single-digit GB): it's `scp`'d to
the serving host and read from `HOTSPOTS_DATA_DIR`. The name changes whenever
the stored encoding does (`seasonal.FileName`), so a new file can be copied
next to the one the running server reads, the new server deployed, and the old
file deleted afterwards. Four buckets, all pure
key-value lookups (never scanned or joined — a relational layer buys
nothing here):

- **`species_by_hotspot`**: hotspot ID (`"lat,lng"`) → a bit-packed blob of
  every species' exact record count for each calendar month (January to
  December, years pooled). Backs `SpeciesStore.Lookup(id, month)`, where
  month 0 sums the year. The format lives in `server/internal/seasonal`,
  shared by the build tool and the server so the two cannot drift. Species
  are integer IDs, not name text; a hotspot averages about a dozen species,
  each seen in about 1.8 months. The encoder writes each hotspot both ways
  and keeps the smaller: month-major (a 12-bit month mask, then per month
  a gamma-coded species count and species IDs as Rice-coded gaps) or
  species-major (gap-coded IDs, each with a one-month shortcut or a 12-bit
  month mask, then counts). Counts are Rice or Elias-gamma coded with
  parameters chosen per hotspot and stored in the blob's first byte. Bits
  rather than bytes because the values are tiny (most counts are below 8,
  most ID gaps need under 11 bits), and a blob is only ever decoded whole.
  Per-hotspot key and page overhead (~35 bytes) is larger than the value
  itself, so this bucket is about the size an all-year list would be even
  though it holds twelve months.
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
- **`hotspot_meta`**: a couple of small fixed keys: the total hotspot
  count, so `HotspotStore.Len()` is a single lookup instead of a full
  bucket scan, and `species_format`, the label of the
  `species_by_hotspot` encoding. The server refuses to open a file whose
  label differs from the one it decodes. (The file name itself changes with
  the encoding, so a new file can sit beside the old one during a rollout.)

At global scale (~20 million hotspots) this lands in the low single-digit
GB. Every `Put` during the build happens in strictly ascending key order
with `FillPercent = 1.0` (bbolt's default `0.5` leaves half of each page
free for future random-order inserts, which never happens here — every key
is written exactly once and never touched again) — roughly halving on-disk
size versus the default.

### Build algorithm (`server/cmd/gensnapshot/hotspots.go`)

The input is a GBIF SQL Download: `decimallatitude`, `decimallongitude`,
`locality`, `species`, `month`, `n` (a row per hotspot, species and month),
`ORDER BY decimalLatitude, decimalLongitude`.
Because it's sorted, the build is a single streaming pass with memory
bounded well below total row or point count — no giant in-memory map of
the ~20 million worldwide hotspots at any point (species-level state) or
close to it (grid-level state):

1. Accumulate the *current* point's per-species monthly counts, locality
   counts, and running total (a row with no usable month is counted and
   skipped, and reported at the end). The moment `(lat, lng)` changes, the point is complete:
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
5. The total point count and the species format label are written to
   `hotspot_meta` at the end.

### Wiring

`main.go` opens `hotspots_seasonal.bolt` once, read-only, as a single shared
`*bbolt.DB` handle: `HotspotStore` answers `Info`/`Nearby`/`Len` from its
three buckets, `SpeciesStore` answers `Lookup(hotspotID, month)` from the sibling
bucket. `SpeciesResolver` sits on top of `SpeciesStore`, implementing the
`hotspotSpeciesSource` interface `main.go` calls against — an interface
purely for testability, so tests can hand the server made-up species codes
without touching real data. A hotspot's ID is
its `"lat,lng"` string, which is both its `bbolt` key (in every
hotspot-keyed bucket) and its API path segment (`/api/hotspots/{locId}`) —
no separate ID translation anywhere.

**Seasons.** `GET .../species?mode=seasonality` groups a hotspot's species
into year-round, seasonal and occasional (`server/seasonality.go`), from the
same twelve monthly counts; there is no extra stored data. Counts are
checklist records, so busy months inflate everything. Each month's observer
effort is approximated by the mean count of that month's three most-reported
species, and a species' rate is its count over that effort. A month whose
effort is under three records is "no data" (many hotspots have few checklists
and whole months with none): it counts neither as present nor absent. Occasional: at
most two records, or a best-month rate under 5%. Otherwise a month is
"present" when the rate is at least 25% of the species' best month; present
in at least 75% of the months that have data is year-round, fewer is
seasonal. Each card carries its group and its twelve effort-adjusted monthly
rates (`seasonBars`, 0-100 relative to the species' own best month, January
first, -1 for a no-data month), which the frontend draws as a small inline
bar chart on every species card; this is attached in every sort order, while
`mode=seasonality` also groups and orders by season. The Seasons view ignores
the month selector (the charts always cover the whole year), and a hotspot with data in fewer than six months comes back
unclassified, as a plain list.

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
`hotspots_seasonal.bolt`, opened as its own mmap-backed handle. Two buckets:

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
ordering Browse's popularity mode uses, including the month selector's
`month=` value) and drives its own card, dot-index,
and drag/arrow-key/top-bar-button navigation client-side (`static/app.js`'s
`learn` object) — there's no separate session endpoint or server-side state
for it. It's started by the Learn button on the hotspot view. Horizontal
drag and the top-bar arrows move between birds; a bird's own photos are
cycled only by the next-photo button in the image's bottom-right corner (no
timer, no swipe), so the drag gesture is never ambiguous. Only photos that
have actually loaded join that cycle; the button and dots stay hidden until a
second photo is ready.

Learn is deep-linkable: `#/hotspot/<locId>/bird/<speciesCode>` opens the
hotspot's Learn deck on that bird, in the month's popularity order when the
link carries `?month=` (an unknown code falls back to the plain
hotspot with a notice). While Learn is open the address bar always holds that
bird's link — `learn.syncURL()` rewrites it with `history.replaceState` on
every card change, so swiping adds no history entries — and closing Learn
restores the plain hotspot URL. Tapping a bird in the hotspot's species grid
opens Learn at that bird (the grid cards are real links to the same URL, so
middle-click/copy-link still work). Each card has a share button (Web Share
API, falling back to copying the link) and an "eBird" pill linking to the
species' eBird page in a new tab; the hotspot header has its own share button
for the hotspot link.

Each card shows up to `maxImagesPerSpecies` (4) images, all from Wikimedia
(`server/wikimedia.go`) and cached to disk. Only two human-curated sources
are used: the species' Wikipedia infobox photo, then the members of its
Commons `Category:Quality images of <taxon>`, largest first with at most two
per photographer. Uncurated Commons category files, iNaturalist and Openverse are not
used: their photos are too often poor (juveniles, distant or cropped
subjects, other species in frame), and a missing photo is preferred over a
poor one — so a species can legitimately
have fewer than four images, or none.

The taxon's Commons category is `Category:<sciName>` when it has files, else
the category Wikidata records for the taxon (property P373), which covers
names Commons files under a taxonomic synonym. Every candidate must also pass
these filters (`commonsFileInfo`):
- a redistributable license (CC0, CC BY, CC BY-SA, public domain);
- JPEG, at least 1000x650, aspect ratio between 0.7 and 2.3;
- no filename hint of a non-photo or non-adult subject (map, egg, nest,
  juvenile, illustration, sound, ...);
- none of the file's own Commons categories flags it (distribution maps,
  statues, captive/zoo, juveniles, flocks, "with other species", ...): a
  file's name can say anything, but its categories give away what it is;
- none of its categories names a *different* bird species (taken from the
  embedded taxonomy), which rejects multi-species photos and miscategorized
  files.
`images.go`'s `ResolveImages` is the entry point; `cache.go` downloads, resizes
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
steps, for offline batch tools (`warmcache`) that want a
species fully resolved before moving to the next one rather than firing
into the background.

**Several processes, one cache directory.** Replicas share `CACHE_DIR`, so the
filesystem is the coordination point. Before fetching a species (first image or
top-up) a process takes a non-blocking `flock` on `<bucket>/<slug>.lock`
(`tryLockSpecies`); if another process holds it, the fetch is skipped — that
process's result lands on disk and every replica serves it, and the frontend's
retry on the image URLs covers the wait. This is what keeps replicas from
spending the Wikimedia budget twice on the same species. The kernel releases the
lock when its holder exits, so a crashed pod leaves nothing stale; lock files
themselves are empty and stay in place. Files are written via a uniquely named
temp file and an atomic rename (`writeFileAtomic`), so concurrent writers never
share a temp file. The rate limiters (`WIKIMEDIA_RPS`, the per-account and
per-IP ones) are per process, so N replicas allow N times those rates.

The cache is otherwise fetch-once: a species that's already fully resolved
is never revisited by ordinary traffic, so a change of image sourcing policy
would only affect species fetched afterwards. To carry a policy change to
already-cached species, startup runs `migrateImagePolicy`
(`server/imagepolicy.go`, called from `main()` right after the cache opens): for every cached species it keeps the first image (the title
photo, which is the Wikipedia infobox photo where the species has one),
deletes the others, and clears the `.topped` and `.missing` markers. The
normal lazy path then refills the rest in the background the next time each
species is viewed — `EnsureFetchedAsync` queues a top-up for species that
still have a first image and a first-image fetch for those that had none — so
slots 2-4 (and formerly "no image" species) are resolved under
`ResolveImages`' current rules, at normal traffic's pace.

`<CACHE_DIR>/.image-policy` records the policy version (`imagePolicyVersion`)
the cache was last trimmed under; the migration runs only when it differs, so
ordinary restarts never discard refilled photos. A fresh cache just records
the version. Bumping the constant re-runs the trim on the next start. It is
pure local disk I/O, finishes in well under a second even for thousands of
species, and logs to `migration.log`.

**Photo credits.** Commons photos are CC BY / CC BY-SA, which require showing
the author, license and source. The Learn overlay's footer has an "Image credits"
link, which follows the current card, to
`#/hotspot/<locId>/bird/<speciesCode>/credits`, a text-only page (no images)
listing that species' photos with each one's title, author, license link and
Commons link. `handleHotspotCredits` (`server/credits.go`) builds it from the
species' cached image metadata (`?species=<code>` narrows the hotspot's list
to one species), so it names exactly the photos the app has downloaded;
species with no cached photo are omitted. The frontend only links `https://` URLs, since license and
source URLs come from Commons metadata.

**Compare.** `#/compare/<a>/<b>` (linked only from a hotspot's footer, as
"Compare with a saved hotspot", which shows when the account has another saved
hotspot) puts two hotspots side by side.
`GET /api/compare` (`server/compare.go`) reads only the local stores — no
image or Wikimedia work — and returns the union of both species lists with
each side's record count, popularity rank and share of that hotspot's total
records, plus the log2 share ratio for species seen at both and per-family
shares. Counts are GBIF records, so hotspots are compared by *share* of their
own totals rather than raw counts (one may simply have more records);
`buildComparison` is the pure function holding that logic. A ratio is
`reliable` only when the species has at least 30 combined records, and the
frontend (`static/compare.js`) fades bars below 45. The view shows an overlap
verdict, a diverging chart of the species that set the two apart, the top
shared species with their ranks (flagging a move of 5 or more places), the
species seen at only one, and the family mix. Learn buttons open the normal
Learn deck on those species (a "quiet" deck: it never rewrites the URL, since
its cards come from two hotspots). Picker options are the account's saved
hotspots plus the busiest hotspots near the first one.

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

**Rollouts and replicas.** On SIGTERM the server keeps serving for
`SHUTDOWN_DELAY` (default 5s) so the ingress drops the pod from the Service's
endpoints, then closes the listener and gives in-flight requests
`SHUTDOWN_TIMEOUT` (default 20s) to finish; the two must fit within the pod's
`terminationGracePeriodSeconds` (default 30s). With that, a `RollingUpdate`
(`maxUnavailable: 0`, `maxSurge: 1`) drops no requests. Replicas must run on
the same node, because `CACHE_DIR` and the SQLite database live on a hostPath;
SQLite runs in WAL mode with a busy timeout, which handles a few processes on
one host. Session and OAuth-state cookies are signed with `SESSION_SECRET`, so
any replica can verify them. See "Several processes, one cache directory"
above for the image cache.

**Replica count is coupled to other settings.** The Wikimedia rate limiter is
per process, so the manifest's `WIKIMEDIA_RPS` is the total budget (8/s)
divided by the number of replicas. `terminationGracePeriodSeconds` must exceed
`SHUTDOWN_DELAY` + `SHUTDOWN_TIMEOUT`. A rollout briefly runs `replicas + 1`
pods, each with a 3Gi memory limit; `hotspots_seasonal.bolt` (~5GB, mmap) is shared
through the page cache. Moving off a single-node hostPath (a PVC, or
spreading pods across nodes) would break both the cache `flock` and SQLite's
WAL sharing.

**Metrics.** The server exposes Prometheus metrics at `/metrics`
(`server/metrics.go`). VictoriaMetrics scrapes each replica separately, through
the headless `birdquiz-headless` Service and `dns_sd_configs`, because
scraping the load-balanced Service would alternate between pods' counters. The
Grafana dashboard therefore aggregates across instances: gauges describing
shared state (accounts, favorites, hotspots and places loaded — every replica
reads the same files) use `max()`, per-process counters and gauges use
`sum()`, and the process panels (RSS, goroutines, CPU) show one series per
`instance`. A new panel needs the same treatment.
