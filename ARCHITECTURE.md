# Architecture

How birdsnearby is built and why. For how to run it or refresh its data, see
`README.md`.

## Overview

birdsnearby is a Go server that serves a bird learning app: search any place
worldwide (a park, a town, a region, a country, or a circle drawn on the map),
browse the species seen there, and learn them in a full-screen
swipeable card deck (Learn mode). The frontend is a small vanilla-JS
single-page app embedded in the binary. Accounts and preferences live in
SQLite; species and place data live in separately distributed `bbolt` files,
built offline from a GBIF dataset and OpenStreetMap. The only upstream calls the
deployed server makes at request time are to Wikimedia, for species card
images.

## Stack

- **Server**: Go, standard library `net/http` with Go 1.22+ method/path
  routing (`server/main.go`).
- **Accounts & preferences**: SQLite via `modernc.org/sqlite` (pure Go, no
  cgo) — `server/userstore.go`.
- **Area, species & place data**: `bbolt`, an embedded ordered key-value store,
  mmap-backed — `server/area.go`, `server/species_store.go`,
  `server/places_data.go`. See "Areas and species data" and "Place search"
  below.
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

The site is readable by anyone: area, species, compare, credits and
static routes need no session. Only the account routes (`/api/me*`) require
sign-in — `Auth.gate` attaches the account to the request context when a
valid session cookie is present (and never blocks), and `Auth.require` /
`requireFunc` wrap each account route, answering 401 JSON to guests.
Saving a place is the one thing guests can't do; the frontend sends them
to `/login?next=…` when they tap the save star.

With **neither** flag set, the server runs in open/development mode: every
request acts as a single fixed account (`dev:local`), so the account routes
work locally without any login. `make server-open` forces this regardless of what's in the
environment; `make server` honors whatever's actually set.

A brand-new account's language preference is seeded from its signup IP's
geolocated country — Spanish-speaking, French-speaking, or (everything
else, including a lookup miss) English — via an embedded, offline-built
IP-to-country table (`server/geoip_data.go`; see "Areas and species data"
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

GET  /api/places                                 place-name search (q)
GET  /api/places/{key}                            area info: name, kind, country, centre, radius, whether it has a polygon
GET  /api/places/{key}/outline                     the area's polygon rings as [lat, lng] pairs, simplified for drawing (404 without a polygon)
GET  /api/places/{key}/species                     species list (category/popularity/alphabetical; optional month=1-12) — Learn's card deck too
GET  /api/places/{key}/credits                     photo credits for the area's species, or one with ?species=<code> (author, license, source)
GET  /api/species                                 species-name search (q, lang): common name in lang or English, or Latin; prefix and word-start matches
GET  /api/species/{code}/range                     world frequency map of a species (lang, optional month=1-12): 1° cells as [lat, lng, percent]
GET  /api/compare                                 two areas side by side (a, b, lang)
GET  /api/contact                                 whether the contact form is configured ({"enabled": bool})
GET  /api/analytics                               Umami script URL + site id (UMAMI_SCRIPT_URL, UMAMI_WEBSITE_ID; empty unless both set)

GET  /api/me                                      profile
GET  /PUT /api/me/language                        primary bird-name + UI language
GET  /PUT /api/me/secondary-language               optional subtitle language
GET  /api/me/favorites                            saved areas (capped at 100/account)
PUT/DELETE /api/me/favorites/{key}                 save/remove an area (described by the server; the PUT has no body)
PUT  /api/me/favorites/{key}/name                  {name}: the label the user gave a saved area ("" restores the default; max 80 chars)
POST /api/contact                                 email a message to the site owner (see "Contact form")

GET  /login, POST /auth/logout                    (only when a provider is enabled)

Everything above is public except `/api/me*` and `POST /api/contact`, which need a session.
GET  /auth/{google,apple}[/callback]               OAuth flow
```

Two independent rate-limit layers guard the area routes: a global
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

Hash-based routing (`#/home`, `#/search`, `#/area/<key>`,
`#/area/<key>/bird/<speciesCode>`, `#/area/<key>/bird/<speciesCode>/credits`,
`#/compare/<keyA>/<keyB>`, `#/about`, `#/settings`; `<key>` is an area key, see
"Area keys")
so every view survives a reload and is linkable without any server-side
routing. Area, bird and credits routes take optional `?month=1-12` and
`?sort=<popularity|category|alphabetical>` after the path (joined
with `&`); they set the month selector and sort menu (a link without them keeps
the current selection) and the app keeps them in the hash as the controls
change (the default sort is left out), so shared links open on the same view
and Learn links on the same month's birds. Navigation is a bottom tab bar (`<nav class="app-nav tab-bar">` in
`index.html`), the canonical iOS primary-navigation placement.

**Mobile layout.** The layout is designed phone-first with one breakpoint at
600px. The header scrolls away on narrow screens; the signed-in email lives in
Settings. Both map views (Search and Atlas) fill the viewport above the tab
bar (`body.map-screen`, `dvh` units, `viewport-fit=cover` with safe-area
insets) and never scroll the page: the Search view has a docked bottom panel
(place name, radius chips and slider, save star, "Browse birds"), a floating
"use my location" button and a one-line hint overlay that disappears after
the first tap; Atlas puts its month select and legend in a compact bar over
the bottom of the map. Tappable controls are at least 44px, form fields at
least 16px (no iOS focus zoom) and hover styles sit behind
`@media (hover: hover)`. Leaflet maps are created on first visit and
`invalidateSize()` runs when they are shown, resized or the device rotates.
Place and species autocompletes (`wireAutocomplete`) have a clear button,
44px rows, a dropdown sized to `visualViewport` and drop focus after a pick.

Secondary actions share one `<dialog id="sheet">` bottom sheet: opening it
pushes a history entry, so the Back gesture, the backdrop and Esc all close it.
A row's ⋯ on Home (Rename, Remove with an undo toast), the area's ⋯ (Rename,
Open on map, Share, Compare) and the rename form itself all render into it.
Sharing uses `navigator.share` where available and otherwise copies the link
and shows a toast.

Views: **Home** (saved places, each with a ⋯ menu; a welcome panel with the Find
places button stands in for the list when nothing is saved, and a "Birds near
me" button opens the area around the visitor), **Search** (place-name search, geolocation, and a map for
drawing a custom circle — see "Place search" below), **Area** (the place's name
with its kind and country, a save star and the ⋯ menu; one sticky row with the
month selector, a native sort select — popularity/category/alphabetical —
and the Learn button; a two-column photo grid whose category
sections have sticky headings with species counts; Learn's full-screen card
deck, see "Learn mode and species images" below),
**Atlas** (a species' frequency over the whole world — see "Species range
map" below), **Settings** (language, secondary language, sign out), and a small
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
`renderRoute` reports each view as a page (`/area/<key>`, `/compare/...`)
and `trackEvent` reports `bookmark`, `share-area` and `learn-start`.
Account details (email, user id) are never sent to it, which is what keeps
it outside cookie-consent requirements; the About page's privacy note
says so.

Guests (`state.signedIn === false`, set when `/api/me` answers 401) get the
same views with three differences: the header shows a "Sign in" link instead
of the email, Home shows only the welcome panel (no saved places; features behind sign-in prompt for it when used),
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

## Areas and species data

A place is a *region*, not a point: the species listed for it are pooled from
every GBIF observation location inside it. Parque del Retiro is a few hundred
metres across, Galicia a polygon, the Sahara a polygon the size of a country,
and a custom circle is whatever the visitor draws. All of it comes from files
built offline; nothing is fetched at request time.

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
  pooled) per species per location, so "which birds are typical here in
  October" is answerable. The dataset ends at the last complete year, so
  there is no "past few weeks" view, only a time-of-year one.
- **No eBird hotspot ID in the mirror.** Records carry `locality` (free
  text) and `decimalLatitude`/`decimalLongitude`, but no `locationID`, so
  there is no clean join key back to eBird's curated hotspots. The data is
  therefore kept as the distinct coordinates GBIF reports (about 20 million
  worldwide; most are one-off personal locations rather than birding sites)
  and *pooled by area*, which makes the noise irrelevant: a region's list is
  the union of everything recorded inside it.

### Build pipeline

Three offline steps (`server/cmd/gensnapshot`, commands in `README.md`), run on
the builder's machine:

1. `hotspots <gbif.zip>` streams the GBIF SQL download (`decimallatitude`,
   `decimallongitude`, `locality`, `species`, `month`, `n`, sorted by
   coordinates) into `hotspots_seasonal.bolt`, a per-coordinate intermediate.
   Each species (scientific name) becomes a `uint16` ID from the embedded
   taxonomy sorted by eBird `speciesCode`; the build tool and the server compute
   that table independently from the same committed taxonomy file.
2. `areas` regroups that intermediate into `seasonal_cells.bolt` (below).
3. `places` builds `places.bolt` from OpenStreetMap and Natural Earth (see
   "Place search").

Only `seasonal_cells.bolt` and `places.bolt` reach the server.

### `seasonal_cells.bolt`

A single mmap-backed `bbolt` file (about 1.2 GB), not `go:embed`'d or
committed (`server/.gitignore`): it is `scp`'d to the serving host and read from
`DATA_DIR`. Opening it loads nothing into RAM and each query pages in only what
it touches. Its `areas_meta` bucket labels the species encoding
(`species_format`, `seasonal-bits-1`) and the server refuses to open a file
whose label differs from the one it decodes.

Counts are additive, so one file serves areas of any size at four resolutions.
Every key derives from one integer, the 0.01° index of a coordinate
(`floor(deg/0.01)`); coarser cells are floor-divisions of it, so a point can
never fall in different cells at different levels through float rounding.

- **`points_by_cell`**: the raw points, keyed by their 0.01° cell. Used for
  areas up to 5 km radius or a small polygon, where cell edges would be
  visible.
- **`cells_0`, `cells_1`, `cells_2`**: every point's counts pre-summed into
  0.05°, 0.25° and 1° cells. A bigger area is answered from the finest level
  whose cell count within the area's bounding box stays under 3000 lookups, so
  a query reads at most a few thousand keys whatever the size of the area. A
  circle takes the cells whose centre is within the radius; a polygon takes
  those whose centre is inside it.

A value is the bit-packed blob of `server/internal/seasonal`, shared by the
build tool and the server so they cannot drift: for each species its exact
record count in each calendar month (January to December, years pooled). The
encoder writes each blob both ways and keeps the smaller — month-major (a
12-bit month mask, then per month a gamma-coded species count and species IDs as
Rice-coded gaps) or species-major (gap-coded IDs, each with a one-month shortcut
or a 12-bit month mask, then counts) — with Rice or Elias-gamma parameters
stored in the first byte. Bits rather than bytes because the values are tiny,
and a blob is only ever decoded whole. Every `Put` during the build is in
ascending key order with `FillPercent = 1.0`, which roughly halves the file
versus bbolt's default.

The `areas` step needs points in 0.01° cell order, so it first walks the
intermediate once to note each point's cell and key position, sorts those, and
then feeds the builder in order; a one-degree latitude band is complete when
the next band's first point arrives, which is when its coarser cells are
written.

### Species range map

The Atlas tab (`#/range/<speciesCode>?m=<month>`) shows where one species is
and how frequent it is. `RangeMapper` (`server/range.go`) scans the whole
`cells_2` bucket (about 22,000 1° cells, roughly half a second) for the
species' ID, so no extra data file is built. A cell's frequency is the
effort-adjusted rate used by the seasonality classifier: the species' count
over the mean count of the cell's three most-reported species plus 5 (the
smoothing keeps a thin cell with one sighting from looking dominant), clamped
to 100%, minimum 1%. Raw counts would only map where observers live. A month
restricts both the count and the effort to that calendar month. Results are
cached (64 species/month pairs) and at most two scans run at once; the
response is cacheable for a day when the language is explicit.

The page draws each cell as a translucent rectangle on a canvas layer over an
OpenStreetMap base; opacity runs from 15% to 90% in proportion to the cell's
percent of the species' own maximum, so a species that peaks at 20% still
shows its shape. Choosing a species frames the map on its range (the 2nd to
98th percentile of cell latitudes and longitudes, so stray vagrant records are
ignored; a range spanning most of the globe keeps the world view), while
changing the month keeps the current view. Species search matches normalized names by prefix or word
start in the interface language, English and Latin. Learn's ⋯ menu links
to a species' map ("Where else?") on the month the place is being browsed.

### Area keys

A key names the region whose species are listed. It is what the API, the
favorites and the URLs carry:

- `p<id>`: a place from `places.bolt`. Its polygon is used when it has one;
  otherwise a circle of the place's default radius around its centre (a town
  or village has a point and a radius derived from its population).
- `c<lat>,<lng>,<km>`: a custom circle. The key is canonical — latitude and
  longitude with three decimals (about 100 m), the radius with one decimal, 1
  to 500 km — so equal areas share one key and one cache entry; `validAreaKey`
  rejects any other spelling. The server names it from `places.bolt` (see
  "Describing a point"), so a custom circle reads "Near Santiago de Compostela ·
  Galicia · Spain" and falls back to its coordinates only where nothing is
  indexed (open sea). The 1 km floor exists because observations are sparse
  points, not a full grid: a circle under 1 km finds no data at most spots.

`server/area.go`'s `AreaResolver` turns a key into the area's description
(`Info`) and its pooled entries (`Entries`). One request asks for the same area
several times (species, popularity, seasonality), and pooling a large area reads
thousands of cells, so results sit in a 128-entry LRU.

`SpeciesStore` and `SpeciesResolver` sit on top of `AreaResolver`, implementing
the `areaSpeciesSource` interface `main.go` calls against — an interface purely
for testability, so tests can hand the server made-up species codes without
touching real data. Both files are opened read-only and shared by all requests.

**Seasons.** An area's species are classified as year-round, seasonal or
occasional (`server/seasonality.go`) from the same twelve monthly counts;
there is no extra stored data. Counts are
checklist records, so busy months inflate everything. Each month's observer
effort is approximated by the mean count of that month's three most-reported
species, and a species' rate is its count over that effort. A month whose
effort is under three records is "no data" (many areas have few checklists
and whole months with none): it counts neither as present nor absent. Each
card in `GET .../species` carries the species' twelve effort-adjusted monthly
rates (`seasonBars`, 0-100 relative to the species' own best month, January
first, -1 for a no-data month), which the frontend draws as a small inline
bar chart on every species card and on the Learn card; this is attached in
every sort order and always covers the whole year, whatever the month
selector says. An area with data in fewer than six months has no bars.

## Place search

The Search view finds a place by name, or takes a point and radius from the
map. Place names and outlines come from `places.bolt`, built offline from two
open sources, so search keeps working with no upstream call:

- **OpenStreetMap**, for local places: cities, towns and villages (as points),
  and administrative boundaries below country level, national parks, protected
  areas, nature reserves, parks, islands and deserts (as polygons, with small
  parks, reserves, islands and districts dropped by area). Geofabrik's regional
  extracts are filtered with `osmium` and reduced to candidate lines
  (`gensnapshot osm`), one file per extract.
- **Natural Earth**, for country outlines and big natural regions (the Sahara,
  Patagonia...) that OSM does not model as one clean polygon
  (`gensnapshot naturalearth`). Public domain.

`scripts/places_osm.sh <workdir>` runs the whole thing: it needs `osmium-tool`,
`curl` and `python3`, about 15 GB of free disk, and downloads about 95 GB over a
few hours. Each extract is downloaded, reduced and deleted, so only the small
candidate files accumulate and a re-run skips extracts already done.
`gensnapshot places` then merges the candidates (by ID across extracts), ranks
them, labels each with its containing country and writes the file.

### Place kinds and labels

Every place has a type: `city`, `country`, `region`, `natural`, `park`,
`reserve`, `island`, `desert`, `town`, `village`, `municipality`, `district`.
The type tells apart places that share a name and is shown in search results and
under an area's title. The builder's `placeTypes` and the server's
`placeTypeNames` list the same strings in the same order, because the type is
stored as an index.

Each place also carries the name of the country containing it; the UI shows
"Kind · Country". The country is empty for countries themselves and for places
spanning several (the Sahara), which are large enough to be known by name alone.

IDs are numeric and below 2^53 so JavaScript holds them exactly: an OSM area is
its OSM ID, an OSM node is `(1<<35) | id`, and a Natural Earth feature is
`(1<<40) + counter`.

### bbolt schema

`places.bolt` — a much smaller (tens to a few hundred MB) sibling of
`seasonal_cells.bolt`, opened as its own mmap-backed handle. Five buckets:

- **`place_by_name`**: key is the normalized (lowercased, accent-folded) name,
  a NUL separator and the 8-byte big-endian place ID, so a prefix scan over the
  ordered keys is a name search and colliding names (there are many "San
  Jose"s) each keep their own entry. A place is indexed under its name and
  aliases (official, English and Spanish names); regions and natural places also
  under each later word onwards, so "Doñana" finds "Parque Nacional de Doñana".
  The value is `{lat, lng, population, countryCode, type, radius, country, name}`.
- **`place_by_id`**: place ID → the same value. Backs `PlaceStore.ByID`, a single
  `Get`.
- **`place_shapes`**: place ID → the simplified polygon (a uvarint ring count,
  then per ring a vertex count and float32 `(lng, lat)` pairs), for places with
  an extent. Backs `PlaceStore.Shape`.
- **`place_by_cell`**: describes an arbitrary point (see "Describing a point").
- **`place_meta`**: the key count.

Search results are ranked by population. OSM often has none, so the builder
substitutes a default by type (a country above a region above a town above a
village, parks and deserts in between).

### Describing a point

A custom circle has no name, so `PlaceStore.Locate` (`server/places_data.go`)
describes its centre from `place_by_cell`: the world cut into half-degree cells,
the key being the cell's row and column. A cell's value lists, as uvarints of
`id<<4 | class`, the villages, towns and cities (class 0) inside it and the
country, municipality and region polygons whose bounding box overlaps it (a
region's class is 8 plus its OSM admin level, 3 to 7). `Locate` reads the
point's cell and its eight neighbours and answers:

- **Near**: the municipality polygon containing the point, else the closest
  locality within 60 km.
- **Region**: the broadest region polygon containing it, preferring admin
  level 4 (state, province, autonomous community), then 5, 6, 7 and 3.
- **Country**: the country polygon containing it, else the one the region or
  locality carries.

Each part may be empty. `AreaInfo` carries them as `name`, `region` and
`country`, and the favorites list re-describes saved custom circles the same
way, so the labels follow the data.

### Search

`PlaceStore.Search` (`server/places_data.go`) seeks to the query's normalized
prefix and walks forward while keys still match, capped at a few thousand
scanned candidates so a very common prefix can't make one request scan without
bound, then sorts by population and truncates. A place indexed under several
matching names is reported once. This ranks prominence within what the scan
found, not a guarantee of the biggest match anywhere once the cap is hit.

### Wiring

`GET /api/places?q=` (`handlePlaceSearch` in `main.go`) is rate limited like
the other area routes: a single local `bbolt` lookup with no upstream fan-out.
Typing in the Search view debounces into it; picking a suggestion stages the
place on the map (`#/search?from=<key>`) instead of opening it. A place with a
polygon is drawn by its outline, from `GET /api/places/{key}/outline` — the
stored rings Douglas-Peucker-simplified to 1/400 of the area's longest side
(`outline.go`), so Galicia is a few hundred vertices rather than thousands —
and is not editable, though "Use a circle instead" swaps it for a circle
at its centre and radius (the outline stays as a faint reference). Any other place (a town, a village, a custom circle) gets
a circle whose centre is set by tapping the map, with the radius slider (logarithmic, 1 to
500 km); the staged place's own key is used until the circle is moved or
resized, after which Save and "Show birds" act on the custom circle
(`c<lat>,<lng>,<km>`). A resized town keeps its name as the favorite's label.
A tap on the map without a staged place picks a custom centre the same way,
and "Use my location" sets it from the browser's geolocation. The area page
links back to its own stage with a pin button. Deep links (`#/area/<key>`)
skip the stage.

## Learn mode and species images

Learn is a full-screen, swipeable deck of an area's species — pure
browsing, no scoring or spaced repetition. The frontend fetches
`GET /api/places/{key}/species?mode=popularity` (the same endpoint and
ordering Browse's popularity mode uses, including the month selector's
`month=` value) and drives its own card, dot-index,
and drag/arrow-key/button navigation client-side (`static/app.js`'s
`learn` object) — there's no separate session endpoint or server-side state
for it. It's started by the Learn button on the area view. The card fits the
dynamic viewport with no scrolling (the page behind is locked) and has a top
bar with × on the left and, on the right, a names toggle (👁), Share and a ⋯ menu, and ‹, "i / n" and › at the bottom
within thumb reach. Horizontal drag moves between birds (pointer capture
starts only once a move is clearly horizontal, so taps still reach the card);
a bird's own photos are cycled only by tapping the photo (no timer, no swipe),
so the drag gesture is never ambiguous. Only photos that have actually
loaded join that cycle; the dots stay hidden until a second photo is ready.
With names hidden, tapping the names area reveals them for that card only.
The next card's first photo is preloaded.

Learn is deep-linkable: `#/area/<key>/bird/<speciesCode>` opens the
area's Learn deck on that bird, in the month's popularity order when the
link carries `?month=` (an unknown code falls back to the plain
area with a notice). Opening the deck pushes one history entry and moving
between cards only replaces it (`learn.syncURL()` uses `history.replaceState`),
so the address bar always holds the current bird's link and Back (or ×)
closes the deck over the area at the same scroll position. Closing restores
the plain area URL. Tapping a bird in the area's species grid
opens Learn at that bird (the grid cards are real links to the same URL, so
middle-click/copy-link still work). Share (top bar; Web Share API, falling back to copying the link) is an icon
next to the names toggle. The ⋯ menu, a bottom sheet titled with the bird,
holds "Where else?", an "eBird" link to the species' eBird page in a new tab
and "Photo credits". The species' twelve-month bar chart (`seasonBars`) sits
under the names.

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
species' worth of image fetching. A Browse/Learn request for an area's
species list (`handleAreaSpecies`) calls `EnsureFetchedAsync` per
species, which dispatches into one of two independent background queues
rather than blocking the request:
- **First image** (`imageFetchConcurrency`, 8 concurrent): a species with no
  cached image yet gets just its first one fetched here — cheap, one source
  lookup and one download, so an area full of never-seen species starts
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
the author, license and source. The Learn overlay's ⋯ menu has a "Photo credits"
entry, which follows the current card, to
`#/area/<key>/bird/<speciesCode>/credits`, a text-only page (no images)
listing that species' photos with each one's title, author, license link and
Commons link. `handleAreaCredits` (`server/credits.go`) builds it from the
species' cached image metadata (`?species=<code>` narrows the area's list
to one species), so it names exactly the photos the app has downloaded;
species with no cached photo are omitted. The frontend only links `https://` URLs, since license and
source URLs come from Commons metadata.

**Compare.** `#/compare/<a>/<b>` (linked only from an area's footer, as
"Compare with a saved place", which shows when the account has another saved
place) puts two areas side by side; `a` and `b` are area keys.
`GET /api/compare` (`server/compare.go`) reads only the local stores — no
image or Wikimedia work — and returns the union of both species lists with
each side's record count, popularity rank and share of that area's total
records, plus the log2 share ratio for species seen at both and per-family
shares. Counts are GBIF records, so areas are compared by *share* of their
own totals rather than raw counts (one may simply have more records);
`buildComparison` is the pure function holding that logic. A ratio is
`reliable` only when the species has at least 30 combined records, and the
frontend (`static/compare.js`) fades bars below 45. The view shows an overlap
verdict, a diverging chart of the species that set the two apart, the top
shared species with their ranks (flagging a move of 5 or more places), the
species seen at only one, and the family mix. Learn buttons open the normal
Learn deck on those species (a "quiet" deck: it never rewrites the URL, since
its cards come from two areas). Picker options are the two areas in play
plus the account's saved places.

## Accounts & persistence

`server/userstore.go` reconciles its SQLite schema at startup, additively
and idempotently: it creates any missing tables and adds missing columns
via `ALTER TABLE`, without touching existing rows — so a database whose
`user_preferences` predates a later column (e.g. `secondary_language`)
repairs itself on the next startup, no migration step needed. Favorites are stored as area keys
(`favorite_areas`: key, name, the user's own label, kind, country, centre, radius); the table that held
coordinate-keyed hotspot favorites is dropped at startup.

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
pods, each with a 3Gi memory limit; `seasonal_cells.bolt` (~1.2GB, mmap) is shared
through the page cache. Moving off a single-node hostPath (a PVC, or
spreading pods across nodes) would break both the cache `flock` and SQLite's
WAL sharing.

**Metrics.** The server exposes Prometheus metrics at `/metrics`
(`server/metrics.go`). VictoriaMetrics scrapes each replica separately, through
the headless `birdquiz-headless` Service and `dns_sd_configs`, because
scraping the load-balanced Service would alternate between pods' counters. The
Grafana dashboard therefore aggregates across instances: gauges describing
shared state (accounts, favorites and places loaded — every replica
reads the same files) use `max()`, per-process counters and gauges use
`sum()`, and the process panels (RSS, goroutines, CPU) show one series per
`instance`. A new panel needs the same treatment.
