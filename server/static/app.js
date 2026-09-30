"use strict";

// birdquiz server app. A hash-routed single page with four views: home,
// search, hotspot and settings. Hash routing survives a reload and is
// linkable without server rewrites. All user-visible text comes from
// i18n.json; this file holds translation keys, never literals.

const $ = (id) => document.getElementById(id);
const VALID_LANGS = ["ca", "cs", "da", "de", "en", "eo", "es", "fi", "fr", "hr", "it", "ja", "lt", "nb", "nl", "pl", "pt", "ru", "sk", "sv", "tr", "uk", "zh"];
const BROWSE_MODES = ["popularity", "category", "alphabetical"];
const VIEW_NAMES = ["home", "search", "hotspot", "credits", "compare", "contact", "settings"];
const SPECIES_CODE_RE = /^[A-Za-z0-9_-]+$/;
const LOC_ID_RE = /^-?\d+(\.\d+)?,-?\d+(\.\d+)?$/;

// ---- Account / UI state ---------------------------------------------------

const state = {
  language: "en",
  secondaryLanguage: "",
  favorites: [],
  email: "",
  // False for a visitor who has not signed in: they can browse everything but
  // cannot save hotspots, and their language choices live in localStorage.
  signedIn: false,
  // speciesCode -> name in the secondary language, for the current hotspot.
  secondaryNames: {},
  // The current hotspot's species in the primary language (Browse's current
  // sort order).
  species: [],
  // { locId, locName, lat, lng } — the hotspot the hotspot view is showing.
  hotspot: null,
  // Whether the search view has run its first hotspot search.
  searchLoaded: false,
  // Every hotspot from the last /api/hotspots search, most-active first —
  // hotspotMarkers (below) only ever holds a prefix of this, so "show more"
  // can reveal the rest without a second network round-trip.
  hotspotsFull: [],
};

let currentRoute = { name: "home", locId: "", speciesCode: "" };

// ---- i18n -----------------------------------------------------------------

let I18N = { en: {}, fr: {}, es: {} };

function t(key, vars) {
  let s = I18N[state.language] ? I18N[state.language][key] : undefined;
  if (s == null) s = I18N.en ? I18N.en[key] : undefined;
  if (s == null) return key;
  if (vars) {
    for (const k of Object.keys(vars)) s = s.split("{" + k + "}").join(String(vars[k]));
  }
  return s;
}

// tn resolves a pluralized key: callers define "<key>.one" and "<key>.other".
function tn(key, n, vars) {
  return t(key + (n === 1 ? ".one" : ".other"), Object.assign({ n }, vars || {}));
}

async function loadTranslations() {
  try {
    const res = await fetch("/i18n.json", { cache: "no-cache" });
    if (res.ok) return await res.json();
  } catch (e) {
    // A missing translation bundle falls back to keys; the app still runs.
  }
  return { en: {}, fr: {}, es: {} };
}

function applyI18n() {
  document.documentElement.lang = state.language;
  document.title = t("app.title");
  document.querySelectorAll("[data-i18n]").forEach((el) => {
    el.textContent = t(el.dataset.i18n);
  });
  document.querySelectorAll("[data-i18n-aria]").forEach((el) => {
    el.setAttribute("aria-label", t(el.dataset.i18nAria));
  });
  document.querySelectorAll("[data-i18n-placeholder]").forEach((el) => {
    el.setAttribute("placeholder", t(el.dataset.i18nPlaceholder));
  });
  updateSortLabel();
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

// Generic greyscale bird silhouette shown while a species' real photo is
// still being fetched (or if it never becomes available).
const PLACEHOLDER_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
  <rect width="100" height="100" fill="#e8e8e8"/>
  <g fill="#b5b5b5">
    <ellipse cx="45" cy="60" rx="28" ry="20"/>
    <circle cx="72" cy="42" r="14"/>
    <polygon points="84,42 96,38 84,48"/>
    <polygon points="20,55 4,50 20,68"/>
    <ellipse cx="40" cy="50" rx="14" ry="9" transform="rotate(-20 40 50)"/>
  </g>
  <circle cx="76" cy="38" r="2" fill="#e8e8e8"/>
</svg>`;
const PLACEHOLDER_URL = "data:image/svg+xml," + encodeURIComponent(PLACEHOLDER_SVG);

// Shown instead of PLACEHOLDER_SVG once the server has confirmed no
// freely-licensed photo exists for a species (sp.imageMissing) — same
// silhouette, marked with a red X so it doesn't read as "still loading".
const MISSING_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
  <rect width="100" height="100" fill="#e8e8e8"/>
  <g fill="#cbcbcb">
    <ellipse cx="45" cy="60" rx="28" ry="20"/>
    <circle cx="72" cy="42" r="14"/>
    <polygon points="84,42 96,38 84,48"/>
    <polygon points="20,55 4,50 20,68"/>
    <ellipse cx="40" cy="50" rx="14" ry="9" transform="rotate(-20 40 50)"/>
  </g>
  <circle cx="76" cy="38" r="2" fill="#e8e8e8"/>
  <line x1="10" y1="10" x2="90" y2="90" stroke="#c33" stroke-width="9" stroke-linecap="round"/>
  <line x1="90" y1="10" x2="10" y2="90" stroke="#c33" stroke-width="9" stroke-linecap="round"/>
</svg>`;
const MISSING_URL = "data:image/svg+xml," + encodeURIComponent(MISSING_SVG);

// Images are fetched server-side in the background (see /api/hotspots/.../species),
// so a card's imageUrl often 404s at first. Preload off-DOM and only swap the
// visible <img> once it actually loads, retrying for a couple of minutes —
// long enough to cover a big hotspot's background-fetch queue — before
// giving up and leaving the placeholder in place.
function preloadAndSwap(imgEl, url, attempt = 0) {
  const MAX_ATTEMPTS = 40;
  const RETRY_MS = 3000;
  const probe = new Image();
  probe.onload = () => { imgEl.src = url; };
  probe.onerror = () => {
    if (attempt < MAX_ATTEMPTS) setTimeout(() => preloadAndSwap(imgEl, url, attempt + 1), RETRY_MS);
  };
  probe.src = url;
}

function secondaryNameFor(speciesCode) {
  return state.secondaryNames[speciesCode] || "";
}

// ---- Status lines ---------------------------------------------------------

function setStatus(msg) { $("status").textContent = msg; }
function setHotspotStatus(msg) { $("hotspotStatus").textContent = msg; }
function showSettingsHint(msg) {
  const hint = $("settingsHint");
  hint.textContent = msg;
  hint.hidden = false;
}

// ---- Routing --------------------------------------------------------------

function parseRoute() {
  const raw = location.hash.replace(/^#\/?/, "");
  const parts = raw.split("/").filter(Boolean);
  const name = parts[0] || "home";
  if (name === "hotspot") {
    const locId = parts[1] ? decodeURIComponent(parts[1]) : "";
    const code = parts[2] === "bird" && parts[3] ? decodeURIComponent(parts[3]) : "";
    return {
      name: parts[2] === "credits" ? "credits" : "hotspot",
      locId,
      speciesCode: SPECIES_CODE_RE.test(code) ? code : "",
    };
  }
  if (name === "compare") {
    return {
      name,
      locId: parts[1] ? decodeURIComponent(parts[1]) : "",
      locIdB: parts[2] ? decodeURIComponent(parts[2]) : "",
      speciesCode: "",
    };
  }
  if (VIEW_NAMES.indexOf(name) !== -1) return { name, locId: "", speciesCode: "" };
  return { name: "home", locId: "", speciesCode: "" };
}

function navigate(hash) {
  if (location.hash === hash) renderRoute();
  else location.hash = hash;
}

function updateNav(name) {
  document.querySelectorAll(".app-nav a[data-nav]").forEach((a) => {
    if (a.dataset.nav === name) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  });
}

async function renderRoute() {
  const route = parseRoute();
  currentRoute = route;
  for (const name of VIEW_NAMES) $("view-" + name).hidden = name !== route.name;
  updateNav(route.name === "compare" ? "home" : route.name === "contact" ? "settings" : route.name);

  if (route.name === "home") await renderHome();
  else if (route.name === "search") showSearch();
  else if (route.name === "hotspot") await openHotspot(route.locId, route.speciesCode);
  else if (route.name === "credits") await openCredits(route.locId);
  else if (route.name === "compare") await openCompare(route.locId, route.locIdB);
  else if (route.name === "contact") await renderContact();
  else if (route.name === "settings") await renderSettings();
}

async function onHashChange() {
  const next = parseRoute();
  if (learn.active && !(next.name === "hotspot" && next.locId === learn.locId && next.speciesCode)) {
    learn.finish();
  }
  await renderRoute();
}

// ---- Account --------------------------------------------------------------

function syncProfile(p) {
  if (VALID_LANGS.includes(p.language)) state.language = p.language;
  state.secondaryLanguage = VALID_LANGS.includes(p.secondaryLanguage) ? p.secondaryLanguage : "";
  state.favorites = Array.isArray(p.favorites) ? p.favorites : [];
  state.email = p.email || "";
  $("userEmail").textContent = state.email;
}

const GUEST_LANG_KEY = "birdquiz.language";
const GUEST_SECONDARY_KEY = "birdquiz.secondaryLanguage";

function guestStored(key) {
  try { return localStorage.getItem(key) || ""; } catch (e) { return ""; }
}

function guestStore(key, value) {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch (e) {
    // Private mode: the choice simply lasts until the page is closed.
  }
}

// applyGuestProfile gives a visitor who isn't signed in their language from
// localStorage, else the browser's, else English.
function applyGuestProfile() {
  const stored = guestStored(GUEST_LANG_KEY);
  const browser = (navigator.language || "").slice(0, 2).toLowerCase();
  state.language = VALID_LANGS.includes(stored) ? stored : (VALID_LANGS.includes(browser) ? browser : "en");
  const secondary = guestStored(GUEST_SECONDARY_KEY);
  state.secondaryLanguage = VALID_LANGS.includes(secondary) ? secondary : "";
  state.favorites = [];
  state.email = "";
}

// updateAccountChrome shows the signed-in email, or a Sign in link for guests.
function updateAccountChrome() {
  $("userEmail").textContent = state.email;
  $("signInLink").hidden = state.signedIn;
  const next = "/" + location.hash;
  const href = "/login?next=" + encodeURIComponent(next);
  for (const id of ["signInLink", "homeSignIn", "settingsSignIn"]) $(id).href = href;
}

async function initAccount() {
  try {
    const res = await fetch("/api/me");
    if (res.ok) {
      state.signedIn = true;
      syncProfile(await res.json());
    } else if (res.status === 401) {
      state.signedIn = false;
      applyGuestProfile();
    }
  } catch (e) {
    // Open/dev mode already works without this; keep the defaults.
  }
  updateAccountChrome();
}

// ---- Home -----------------------------------------------------------------

// homeEditing toggles whether homeFavorites renders remove controls (iOS
// Reminders/Notes-style "Edit" mode) instead of a permanent remove button
// next to every single row.
let homeEditing = false;

function renderHomeFavorites() {
  const list = $("homeFavorites");
  list.innerHTML = "";
  for (const f of state.favorites) {
    const li = document.createElement("li");
    li.className = "hotspot-item" + (homeEditing ? " editing" : "");
    const a = document.createElement("a");
    a.className = "hotspot-link";
    a.href = "#/hotspot/" + encodeURIComponent(f.locId);
    a.textContent = f.locName || f.locId;
    li.appendChild(a);
    if (homeEditing) {
      const remove = document.createElement("button");
      remove.className = "remove-btn";
      remove.type = "button";
      remove.setAttribute("aria-label", t("home.unbookmark"));
      remove.textContent = "−";
      remove.addEventListener("click", async () => {
        try {
          await deleteFavorite(f.locId);
        } catch (e) {
          showSettingsHint(t("search.bookmarkError"));
          return;
        }
        renderHomeFavorites();
      });
      li.appendChild(remove);
    } else {
      const chev = document.createElement("span");
      chev.className = "chev";
      chev.setAttribute("aria-hidden", "true");
      chev.textContent = "›";
      li.appendChild(chev);
    }
    list.appendChild(li);
  }
  $("homeNoFavorites").hidden = state.favorites.length > 0;
  const editToggle = $("homeEditToggle");
  editToggle.hidden = state.favorites.length === 0;
  editToggle.textContent = homeEditing ? t("home.editDone") : t("home.edit");
}

async function renderHome() {
  homeEditing = false;
  $("homeGuest").hidden = true;
  if (!state.signedIn) {
    $("homeGuest").hidden = false;
    $("homeFirstRun").hidden = true;
    $("homeHotspotsPanel").hidden = true;
    $("homeGreetingPanel").hidden = true;
    $("homeCompareLink").hidden = true;
    return;
  }
  let profile;
  try {
    const res = await fetch("/api/me");
    if (!res.ok) throw new Error("load");
    profile = await res.json();
  } catch (e) {
    $("homeFirstRun").hidden = true;
    $("homeHotspotsPanel").hidden = false;
    $("homeGreetingPanel").hidden = false;
    $("homeFavorites").innerHTML = "";
    $("homeNoFavorites").hidden = true;
    return;
  }

  syncProfile(profile);
  const firstRun = state.favorites.length === 0;

  const homeCompare = $("homeCompareLink");
  homeCompare.hidden = state.favorites.length < 2;
  if (!homeCompare.hidden) homeCompare.href = compareHash(state.favorites[0].locId, state.favorites[1].locId);

  $("homeFirstRun").hidden = !firstRun;
  $("homeHotspotsPanel").hidden = firstRun;
  $("homeGreetingPanel").hidden = firstRun;
  renderHomeFavorites();
}

// ---- Search ---------------------------------------------------------------

let map = null;
let userMarker = null;
let hotspotMarkers = [];

function showSearch() {
  if (map) setTimeout(() => map.invalidateSize(), 0);
  if (!state.searchLoaded) {
    state.searchLoaded = true;
    findHotspots();
  }
}

function initMap() {
  map = L.map("map").setView([parseFloat($("lat").value), parseFloat($("lng").value)], 12);
  L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: t("search.mapAttribution"),
  }).addTo(map);
  // Keeps the plotted markers in sync with whatever's actually in view —
  // see renderVisibleHotspots.
  map.on("moveend", renderVisibleHotspots);
}

function clearHotspotMarkers() {
  for (const m of hotspotMarkers) map.removeLayer(m);
  hotspotMarkers = [];
}

// The whole popup is one link to the hotspot page (no buttons); the chevron
// is the tap affordance. Bookmarking lives on the hotspot page's star.
function popupHTML(h) {
  const count = h.totalCount || 0;
  return `
    <a class="popup-link" href="#/hotspot/${encodeURIComponent(h.locId)}" data-locid="${escapeHtml(h.locId)}">
      <span class="popup-text">
        <strong class="popup-title">${escapeHtml(h.locName)}</strong>
        <span class="popup-count">${escapeHtml(tn("search.observations", count, { n: count.toLocaleString() }))}</span>
      </span>
      <span class="popup-chev" aria-hidden="true">›</span>
    </a>`;
}

const MIN_MARKER_RADIUS = 5;
const MAX_MARKER_RADIUS = 20;

// Observation counts are heavily right-skewed (a handful of hotspots run
// into the tens of thousands, most are far smaller), so a log scale spreads
// that out much better than sqrt does. Scaled relative to the busiest
// hotspot in *this* search rather than a fixed global max, so the full size
// range is always used whether the area's counts run into the tens or the
// tens-of-thousands — the tradeoff is that the same hotspot can render at a
// different size depending on what else is nearby in the current search.
function markerRadius(totalCount, maxTotalCount) {
  if (maxTotalCount <= 0) return MIN_MARKER_RADIUS;
  const t2 = Math.log1p(totalCount || 0) / Math.log1p(maxTotalCount);
  return MIN_MARKER_RADIUS + t2 * (MAX_MARKER_RADIUS - MIN_MARKER_RADIUS);
}

// HOTSPOT_MARKERS_DEFAULT is how many markers are plotted at once. GBIF's
// points are dense and uncurated — a busy area can return hundreds of
// hotspots, most of them one-off personal checklist locations rather than
// real birding sites — so rather than dump all of them on the map, the
// current map viewport is the filter: renderVisibleHotspots (wired to the
// map's moveend event in initMap) always shows just the busiest few among
// whatever's currently in view. Pan or zoom and it updates live — no
// separate "show more" control needed.
const HOTSPOT_MARKERS_DEFAULT = 5;

async function findHotspots() {
  const lat = parseFloat($("lat").value), lng = parseFloat($("lng").value);
  if (Number.isNaN(lat) || Number.isNaN(lng)) { setStatus(t("search.status.enterCoords")); return; }

  setStatus(t("search.status.loading"));
  let hotspots;
  try {
    const res = await fetch(`/api/hotspots?lat=${lat}&lng=${lng}`);
    if (!res.ok) { setStatus(t("search.status.error", { status: res.status })); return; }
    hotspots = await res.json();
  } catch (e) {
    setStatus(t("search.status.error", { status: "?" }));
    return;
  }

  if (userMarker) map.removeLayer(userMarker);
  // autoPan: false — a popup that pans the map to fit itself fires moveend,
  // which renderVisibleHotspots treats as "the user moved the map" and
  // rebuilds every marker, destroying the one whose popup just opened. See
  // the identical note on the hotspot markers below.
  userMarker = L.marker([lat, lng]).addTo(map).bindPopup(t("search.youAreHere"), { autoPan: false });

  state.hotspotsFull = hotspots;
  // Deliberately doesn't move the map — every caller (selectPlace,
  // useLocation, searchHere, the initial default view) already set a
  // sensible view before calling findHotspots, and re-fitting to the whole
  // (dense, uncurated — see ARCHITECTURE.md) result set would zoom out to
  // fit hundreds of points just to frame the 5 actually being shown.
  // renderVisibleHotspots derives what to plot from whatever view this is.
  renderVisibleHotspots();
  setStatus(tn("search.status.found", hotspots.length));
}

// renderVisibleHotspots shows the busiest HOTSPOT_MARKERS_DEFAULT hotspots
// that fall inside the map's *current* viewport — wired to Leaflet's
// moveend event (initMap), so panning or zooming re-filters live instead of
// needing a manual reveal. Marker size is still scaled against the full
// search result's busiest hotspot (state.hotspotsFull), not just what's in
// view, so a marker's size stays stable as it comes in and out of frame.
function renderVisibleHotspots() {
  if (!map) return;
  clearHotspotMarkers();
  if (state.hotspotsFull.length === 0) return;

  const bounds = map.getBounds();
  const visible = state.hotspotsFull
    .filter((h) => bounds.contains([h.lat, h.lng]))
    .sort((a, b) => (b.totalCount || 0) - (a.totalCount || 0))
    .slice(0, HOTSPOT_MARKERS_DEFAULT);

  const maxTotalCount = Math.max(0, ...state.hotspotsFull.map((h) => h.totalCount || 0));
  for (const h of visible) {
    const marker = L.circleMarker([h.lat, h.lng], {
      radius: markerRadius(h.totalCount, maxTotalCount),
      color: "#007aff",
      fillColor: "#007aff",
      fillOpacity: 0.6,
    }).addTo(map).bindPopup(popupHTML(h), { autoPan: false });
    hotspotMarkers.push(marker);
  }
}

// ---- Place search -----------------------------------------------------

// Typing a place name is the front door for "what's around here" — it
// replaces raw lat/lng entry (still present as hidden #lat/#lng inputs,
// unchanged by the rest of the search flow) with a prefix search against
// places.bolt (server/places_data.go), a GeoNames-derived gazetteer built
// entirely offline. Picking a suggestion sets those hidden fields and runs
// the same findHotspots() as the map/geolocation paths.
let placeSearchTimer = null;
let placeResults = [];

function wirePlaceSearch() {
  const input = $("placeQuery");
  input.addEventListener("input", () => {
    const q = input.value.trim();
    clearTimeout(placeSearchTimer);
    if (q.length < 2) { hidePlaceSuggestions(); return; }
    placeSearchTimer = setTimeout(() => runPlaceSearch(q), 300);
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Escape") { hidePlaceSuggestions(); return; }
    if (e.key === "Enter" && placeResults.length > 0 && !$("placeSuggestions").hidden) {
      e.preventDefault();
      selectPlace(placeResults[0]);
    }
  });
  $("placeSuggestions").addEventListener("click", (e) => {
    const li = e.target.closest(".place-suggestion");
    if (!li) return;
    const p = placeResults[Number(li.dataset.index)];
    if (p) selectPlace(p);
  });
  document.addEventListener("click", (e) => {
    if (!e.target.closest(".place-search")) hidePlaceSuggestions();
  });
}

async function runPlaceSearch(q) {
  let results;
  try {
    const res = await fetch(`/api/places?q=${encodeURIComponent(q)}`);
    if (!res.ok) { hidePlaceSuggestions(); return; }
    results = await res.json();
  } catch (e) {
    hidePlaceSuggestions();
    return;
  }
  renderPlaceSuggestions(results || []);
}

function renderPlaceSuggestions(results) {
  placeResults = results;
  const list = $("placeSuggestions");
  if (results.length === 0) {
    list.innerHTML = `<li class="place-suggestion-empty">${t("search.placeNoResults")}</li>`;
    list.hidden = false;
    return;
  }
  list.innerHTML = results.map((p, i) => `
    <li class="place-suggestion" data-index="${i}">
      <span>${escapeHtml(p.name)}</span>
      <span class="place-suggestion-country">${escapeHtml(p.countryCode)}</span>
    </li>`).join("");
  list.hidden = false;
}

function hidePlaceSuggestions() {
  const list = $("placeSuggestions");
  list.hidden = true;
  list.innerHTML = "";
}

function selectPlace(p) {
  $("placeQuery").value = p.name;
  $("lat").value = p.lat.toFixed(6);
  $("lng").value = p.lng.toFixed(6);
  hidePlaceSuggestions();
  map.setView([p.lat, p.lng], 12);
  findHotspots();
}

// ---- Favorites ------------------------------------------------------------

async function putFavorite(fav) {
  const res = await fetch(`/api/me/favorites/${encodeURIComponent(fav.locId)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ locName: fav.locName || "", lat: fav.lat || 0, lng: fav.lng || 0 }),
  });
  if (!res.ok) throw new Error(String(res.status));
  const existing = state.favorites.find((f) => f.locId === fav.locId);
  if (existing) {
    if (fav.locName) existing.locName = fav.locName;
    if (fav.lat || fav.lng) { existing.lat = fav.lat; existing.lng = fav.lng; }
  } else {
    state.favorites.push({ locId: fav.locId, locName: fav.locName || "", lat: fav.lat || 0, lng: fav.lng || 0 });
  }
}

async function deleteFavorite(locId) {
  const res = await fetch(`/api/me/favorites/${encodeURIComponent(locId)}`, { method: "DELETE" });
  if (!res.ok) throw new Error(String(res.status));
  state.favorites = state.favorites.filter((f) => f.locId !== locId);
}

// Popup content is injected by Leaflet outside our control, so use event
// delegation instead of binding a listener per marker.
document.addEventListener("click", async (e) => {
  const open = e.target.closest(".popup-link");
  if (!open) return;
  e.preventDefault();
  navigate("#/hotspot/" + encodeURIComponent(open.dataset.locid));
});

// ---- Hotspot + species browsing ------------------------------------------

function currentBrowseMode() {
  const r = document.querySelector('input[name="mode"]:checked');
  return r ? r.value : "category";
}

// The sort menu (#sortBtn/#sortMenu) is the visible control; the original
// #modeToggle radios (now hidden) stay the actual source of truth so every
// existing consumer of currentBrowseMode()/the radios' change event and the
// ?mode= deep-link keeps working unchanged.
function updateSortLabel() {
  const label = $("sortBtnLabel");
  if (!label) return;
  const mode = currentBrowseMode();
  label.textContent = t("browse." + mode);
  for (const li of document.querySelectorAll("#sortMenu li[data-value]")) {
    li.setAttribute("aria-selected", String(li.dataset.value === mode));
  }
}

function wireSortMenu() {
  const btn = $("sortBtn");
  const menu = $("sortMenu");
  const close = () => { menu.hidden = true; btn.setAttribute("aria-expanded", "false"); };
  btn.addEventListener("click", () => {
    const opening = menu.hidden;
    menu.hidden = !opening;
    btn.setAttribute("aria-expanded", String(opening));
  });
  menu.addEventListener("click", (e) => {
    const li = e.target.closest("li[data-value]");
    if (!li) return;
    const radio = document.querySelector(`input[name="mode"][value="${li.dataset.value}"]`);
    if (radio && !radio.checked) {
      radio.checked = true;
      radio.dispatchEvent(new Event("change"));
    }
    updateSortLabel();
    close();
  });
  document.addEventListener("click", (e) => {
    if (!menu.hidden && !e.target.closest(".sort-control")) close();
  });
  updateSortLabel();
}

function updateBookmarkButton() {
  if (!state.hotspot) return;
  const saved = state.favorites.some((f) => f.locId === state.hotspot.locId);
  const btn = $("bookmarkBtn");
  // Icon-only: filled star when saved (via [aria-pressed], see style.css),
  // outline otherwise. aria-label carries the same info textContent used to.
  btn.setAttribute("aria-pressed", saved ? "true" : "false");
  btn.setAttribute("aria-label", saved ? t("hotspot.bookmarked") : t(state.signedIn ? "hotspot.bookmark" : "hotspot.bookmarkSignIn"));
}

// ensureHotspot makes state.hotspot describe locId, fetching its name and
// position unless it's already the current hotspot. Returns false (with the
// hotspot status line set) if the lookup fails.
async function ensureHotspot(locId) {
  if (state.hotspot && state.hotspot.locId === locId) return true;
  state.hotspot = { locId, locName: "", lat: 0, lng: 0 };
  state.species = [];
  state.secondaryNames = {};
  $("hotspotName").textContent = locId;
  setHotspotStatus(t("hotspot.loading"));
  try {
    const res = await fetch(`/api/hotspots/${encodeURIComponent(locId)}`);
    if (!res.ok) { setHotspotStatus(t("hotspot.error")); return false; }
    const h = await res.json();
    state.hotspot = { locId, locName: h.locName || locId, lat: h.lat, lng: h.lng };
  } catch (e) {
    setHotspotStatus(t("hotspot.error"));
    return false;
  }
  return true;
}

function creditsHash(locId) { return "#/hotspot/" + encodeURIComponent(locId) + "/credits"; }

function birdHash(locId, speciesCode) {
  return "#/hotspot/" + encodeURIComponent(locId) + "/bird/" + encodeURIComponent(speciesCode);
}

// A shared bird link (#/hotspot/<locId>/bird/<code>) opens the hotspot with
// Learn already on that bird; the species list loads behind it in parallel.
async function openHotspot(locId, speciesCode) {
  if (!LOC_ID_RE.test(locId)) { navigate("#/home"); return; }
  if (!(await ensureHotspot(locId))) return;

  $("hotspotName").textContent = state.hotspot.locName || state.hotspot.locId;
  $("hotspotCreditsLink").href = creditsHash(locId);
  $("hotspotCompareLink").href = compareHash(locId, "");
  updateBookmarkButton();
  const browse = loadSpecies();
  if (speciesCode) await loadAndStartLearn(speciesCode);
  await browse;
}

// ---- Contact --------------------------------------------------------------

let contactEmail;

async function renderContact() {
  if (contactEmail === undefined) {
    try {
      const res = await fetch("/api/contact");
      contactEmail = res.ok ? (await res.json()).email || "" : "";
    } catch (e) {
      contactEmail = "";
    }
  }
  $("contactEmailRow").hidden = !contactEmail;
  if (contactEmail) {
    const a = $("contactEmail");
    a.textContent = contactEmail;
    a.href = "mailto:" + contactEmail;
  }
}

// ---- Photo credits --------------------------------------------------------

// safeHref returns url only if it's an https link, so a license or source URL
// taken from Commons metadata can never become a script-running href.
function safeHref(url) {
  return typeof url === "string" && /^https:\/\//i.test(url) ? url : "";
}

function creditLink(text, url) {
  const href = safeHref(url);
  if (!href) return document.createTextNode(text);
  const a = document.createElement("a");
  a.href = href;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  a.textContent = text;
  return a;
}

function renderCreditPhoto(img) {
  const li = document.createElement("li");
  const title = String(img.title || "").replace(/\.[A-Za-z0-9]+$/, "");
  li.appendChild(document.createTextNode(t("credits.photoBy", { title, author: img.author || "?" })));
  if (img.license) {
    li.appendChild(document.createTextNode(" · "));
    li.appendChild(creditLink(img.license, img.licenseUrl));
  }
  li.appendChild(document.createTextNode(" · "));
  li.appendChild(creditLink(t("credits.source"), img.sourceUrl));
  return li;
}

async function openCredits(locId) {
  if (!LOC_ID_RE.test(locId)) { navigate("#/home"); return; }
  const stale = () => currentRoute.name !== "credits" || currentRoute.locId !== locId;
  window.scrollTo(0, 0);
  $("creditsBack").href = "#/hotspot/" + encodeURIComponent(locId);
  $("creditsHotspot").textContent = "";
  $("creditsList").innerHTML = "";
  $("creditsStatus").textContent = t("credits.loading");

  const loaded = await ensureHotspot(locId);
  if (stale()) return;
  if (!loaded) { $("creditsStatus").textContent = t("hotspot.error"); return; }
  $("creditsHotspot").textContent = state.hotspot.locName;

  let species;
  try {
    const res = await fetch(`/api/hotspots/${encodeURIComponent(locId)}/credits?lang=${encodeURIComponent(state.language)}`);
    if (!res.ok) throw new Error(String(res.status));
    species = await res.json();
  } catch (e) {
    if (!stale()) $("creditsStatus").textContent = t("credits.error");
    return;
  }
  if (stale()) return;
  $("creditsStatus").textContent = species.length ? "" : t("credits.none");

  const list = $("creditsList");
  for (const sp of species) {
    const li = document.createElement("li");
    li.className = "credit-species";
    const name = document.createElement("div");
    name.className = "credit-name";
    name.textContent = sp.comName;
    const sci = document.createElement("span");
    sci.className = "credit-sci";
    sci.textContent = sp.sciName;
    name.append(" ", sci);
    const photos = document.createElement("ul");
    photos.className = "credit-photos";
    for (const img of sp.images) photos.appendChild(renderCreditPhoto(img));
    li.append(name, photos);
    list.appendChild(li);
  }
}

async function loadSecondaryNames(locId) {
  state.secondaryNames = {};
  if (!state.secondaryLanguage || state.secondaryLanguage === state.language) return;
  try {
    const res = await fetch(`/api/hotspots/${encodeURIComponent(locId)}/species?lang=${state.secondaryLanguage}&mode=category`);
    if (!res.ok) return;
    const species = await res.json();
    for (const sp of species) state.secondaryNames[sp.speciesCode] = sp.comName;
  } catch (e) {
    // A missing subtitle is not worth surfacing as an error.
  }
}

function speciesCard(sp) {
  const card = document.createElement("a");
  card.className = "card";
  card.href = birdHash(state.hotspot.locId, sp.speciesCode);
  card.dataset.code = sp.speciesCode;
  card.innerHTML = `
    <img src="${sp.imageMissing ? MISSING_URL : PLACEHOLDER_URL}" alt="${escapeHtml(sp.comName)}">
    <div class="card-body">
      <div class="com">${escapeHtml(sp.comName)}</div>
      ${secondaryNameFor(sp.speciesCode) ? `<div class="sub">${escapeHtml(secondaryNameFor(sp.speciesCode))}</div>` : ""}
      <div class="sci">${escapeHtml(sp.sciName)}</div>
    </div>
  `;
  // imageMissing means the server already confirmed no photo exists — no
  // point polling a URL that will never 200.
  if (sp.imageUrl && !sp.imageMissing) preloadAndSwap(card.querySelector("img"), sp.imageUrl);
  return card;
}

async function loadSpecies() {
  const hs = state.hotspot;
  if (!hs) return;
  const lang = state.language;
  const mode = currentBrowseMode();
  const groups = $("groups");
  groups.innerHTML = "";

  // Fetch the secondary-language names first (cached after the first load) so
  // the cards below can render their subtitles immediately.
  await loadSecondaryNames(hs.locId);

  let species;
  if (mode === "popularity" || mode === "alphabetical") {
    setHotspotStatus(mode === "popularity"
      ? t("browse.loadingPopularity", { name: hs.locName })
      : t("browse.loading", { name: hs.locName }));
    try {
      const res = await fetch(`/api/hotspots/${encodeURIComponent(hs.locId)}/species?lang=${lang}&mode=${mode}`);
      if (!res.ok) { setHotspotStatus(t("browse.error", { status: res.status })); return; }
      species = await res.json();
    } catch (e) {
      setHotspotStatus(t("browse.error", { status: "?" }));
      return;
    }
    state.species = species;
    const grid = document.createElement("div");
    grid.className = "family-grid";
    groups.appendChild(grid);
    for (const sp of species) grid.appendChild(speciesCard(sp));
    const key = mode === "popularity" ? "browse.loadedPopularity" : "browse.loadedAlphabetical";
    setHotspotStatus(tn(key, species.length, { name: hs.locName }));
    return;
  }

  setHotspotStatus(t("browse.loadingCategory", { name: hs.locName }));
  try {
    const res = await fetch(`/api/hotspots/${encodeURIComponent(hs.locId)}/species?lang=${lang}&mode=category`);
    if (!res.ok) { setHotspotStatus(t("browse.error", { status: res.status })); return; }
    species = await res.json();
  } catch (e) {
    setHotspotStatus(t("browse.error", { status: "?" }));
    return;
  }
  state.species = species;

  // Species arrive already grouped by family (server sends eBird's taxonomic
  // order), so a family heading just goes up whenever it changes.
  let currentFamily = null;
  let currentGrid = null;
  for (const sp of species) {
    const family = sp.family || sp.order || t("browse.otherFamily");
    if (family !== currentFamily) {
      currentFamily = family;
      const heading = document.createElement("h3");
      heading.className = "family-heading";
      heading.textContent = family;
      groups.appendChild(heading);
      currentGrid = document.createElement("div");
      currentGrid.className = "family-grid";
      groups.appendChild(currentGrid);
    }
    currentGrid.appendChild(speciesCard(sp));
  }
  setHotspotStatus(tn("browse.loadedCategory", species.length, { name: hs.locName }));
}

// ---- Settings -------------------------------------------------------------

function buildLanguageSelects() {
  const primary = $("primaryLang");
  primary.innerHTML = "";
  for (const code of VALID_LANGS) {
    const o = document.createElement("option");
    o.value = code;
    o.textContent = t("lang." + code);
    if (code === state.language) o.selected = true;
    primary.appendChild(o);
  }

  const secondary = $("secondaryLang");
  secondary.innerHTML = "";
  const none = document.createElement("option");
  none.value = "";
  none.textContent = t("lang.none");
  secondary.appendChild(none);
  for (const code of VALID_LANGS) {
    const o = document.createElement("option");
    o.value = code;
    o.textContent = t("lang." + code);
    if (code === state.secondaryLanguage) o.selected = true;
    secondary.appendChild(o);
  }
}

async function renderSettings() {
  $("settingsHint").hidden = true;
  $("logoutBtn").hidden = !state.signedIn;
  $("settingsSignIn").hidden = state.signedIn;
  $("settingsGuestHint").hidden = state.signedIn;
  buildLanguageSelects();
  if (!state.signedIn) return;
  try {
    const res = await fetch("/api/me");
    if (res.ok) syncProfile(await res.json());
  } catch (e) {
    showSettingsHint(t("settings.loadError"));
  }
  buildLanguageSelects();
}

async function onPrimaryLanguageChange() {
  const lang = $("primaryLang").value;
  if (!VALID_LANGS.includes(lang)) return;
  if (state.signedIn) {
    try {
      const res = await fetch("/api/me/language", {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ language: lang }),
      });
      if (!res.ok) throw new Error(String(res.status));
    } catch (e) {
      showSettingsHint(t("settings.errorPrimary"));
      return;
    }
  } else {
    guestStore(GUEST_LANG_KEY, lang);
  }
  state.language = lang;
  applyI18n();
  buildLanguageSelects();
  if (currentRoute.name === "home") await renderHome();
  else if (currentRoute.name === "hotspot") await loadSpecies();
  else if (currentRoute.name === "settings") await renderSettings();
}

async function onSecondaryLanguageChange() {
  const lang = $("secondaryLang").value;
  if (lang !== "" && !VALID_LANGS.includes(lang)) return;
  if (state.signedIn) {
    try {
      const res = await fetch("/api/me/secondary-language", {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ language: lang }),
      });
      if (!res.ok) throw new Error(String(res.status));
    } catch (e) {
      showSettingsHint(t("settings.errorSecondary"));
      return;
    }
  } else {
    guestStore(GUEST_SECONDARY_KEY, lang);
  }
  state.secondaryLanguage = lang;
  if (currentRoute.name === "hotspot") await loadSpecies();
}

async function onLogout() {
  try {
    await fetch("/auth/logout", { method: "POST" });
  } catch (e) {
    // Either way, send the browser back to the (now signed-out) site.
  }
  window.location = "/";
}

// ---- Learn ------------------------------------------------------------

// shareLink offers a link through the native share sheet where there is one,
// otherwise copies it. Resolves true only when it fell back to copying (the
// caller confirms that itself — the share sheet is its own confirmation).
async function shareLink(title, text, url) {
  if (navigator.share) {
    try {
      await navigator.share({ title, text, url });
      return false;
    } catch (e) {
      if (e && e.name === "AbortError") return false;
    }
  }
  return copyText(url);
}

function shareBird(item) {
  const place = item.locName || (state.hotspot ? state.hotspot.locName : "");
  return shareLink(item.comName, t("learn.shareText", { name: item.comName, place }), location.origin + location.pathname + birdHash(item.locId || learn.locId, item.speciesCode));
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch (e) {
    // navigator.clipboard is unavailable outside HTTPS/localhost; fall back to
    // the legacy selection-based copy, which works on a user gesture anywhere.
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.cssText = "position:fixed;top:0;left:0;opacity:0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand("copy"); } catch (err) { ok = false; }
    ta.remove();
    return ok;
  }
}

// A Learn card's image slides: index 0 shares imageMissing's "the server
// already confirmed this" signal with Browse's cards (see preloadAndSwap),
// so it gets the same generous retry budget. A secondary slide (1-3) has no
// such signal — the species may simply not have that many images cached —
// so it gives up much sooner and is skipped when cycling instead of
// polling a URL that may never 200.
const LEARN_PRIMARY_RETRIES = 40;
const LEARN_SECONDARY_RETRIES = 5;
const LEARN_RETRY_MS = 3000;
const LEARN_DRAG_THRESHOLD = 80;

function learnLoadSlide(imgEl, url, maxAttempts, { onLoad, onGiveUp }, attempt = 0) {
  const probe = new Image();
  probe.onload = () => { imgEl.src = url; imgEl.dataset.loaded = "1"; if (onLoad) onLoad(); };
  probe.onerror = () => {
    if (attempt < maxAttempts) setTimeout(() => learnLoadSlide(imgEl, url, maxAttempts, { onLoad, onGiveUp }, attempt + 1), LEARN_RETRY_MS);
    else if (onGiveUp) onGiveUp();
  };
  probe.src = url;
}

// learn drives the full-screen card deck: cards is the hotspot's species in
// popularity order (see startLearn), and index just moves through it — no
// scoring, no server round-trip per card. namesVisible is a per-session
// display toggle, not account data, so it isn't persisted anywhere.
const learn = {
  active: false,
  locId: "",
  cards: [],
  index: 0,
  done: false,
  namesVisible: true,
  keyHandler: null,

  // quiet decks (started from Compare) leave the address bar alone: their
  // cards come from two hotspots, so no single bird URL describes them.
  quiet: false,

  start(cards, locId, index = 0, opts = {}) {
    this.quiet = !!opts.quiet;
    this.cards = cards.slice();
    this.locId = locId;
    this.index = index;
    this.done = false;
    this.active = true;
    $("learnOverlay").hidden = false;
    $("learnCreditsLink").href = creditsHash(locId);
    this.keyHandler = (e) => this.onKeyDown(e);
    document.addEventListener("keydown", this.keyHandler);
    this.render();
  },

  finish() {
    this.active = false;
    $("learnOverlay").hidden = true;
    // Closing a shared-bird view drops the bird from the URL so a reload (or
    // re-sharing the page) lands on the plain hotspot. replaceState doesn't
    // fire hashchange; the check skips the case where finish() runs because
    // the user already navigated somewhere else.
    const route = parseRoute();
    if (!this.quiet && route.name === "hotspot" && route.locId === this.locId && route.speciesCode) {
      history.replaceState(null, "", "#/hotspot/" + encodeURIComponent(this.locId));
      currentRoute = Object.assign({}, route, { speciesCode: "" });
    }
    if (this.keyHandler) document.removeEventListener("keydown", this.keyHandler);
    this.keyHandler = null;
  },

  onKeyDown(e) {
    if (e.key === "ArrowRight") this.go(1);
    else if (e.key === "ArrowLeft") this.go(-1);
    else if (e.key === "Escape") this.finish();
  },

  // go advances (delta > 0) or goes back (delta < 0) one card. Past either
  // end of the deck it's a no-op (a small bounce on the last card) rather
  // than wrapping — closing is always one tap away via the top bar.
  go(delta) {
    if (this.done) {
      if (delta < 0) { this.done = false; this.render(); }
      return;
    }
    const next = this.index + delta;
    if (next < 0) { this.bounce(); return; }
    if (next >= this.cards.length) { this.done = true; this.render(); return; }
    this.index = next;
    this.render();
  },

  bounce() {
    const card = $("learnBody").querySelector(".learn-card");
    if (!card) return;
    card.classList.remove("learn-bounce");
    void card.offsetWidth; // restart the animation on repeated presses
    card.classList.add("learn-bounce");
  },

  toggleNames() {
    this.namesVisible = !this.namesVisible;
    const names = $("learnBody").querySelector(".learn-names");
    if (names) names.hidden = !this.namesVisible;
    const btn = $("learnToggleNames");
    btn.setAttribute("aria-pressed", String(this.namesVisible));
    btn.setAttribute("aria-label", t(this.namesVisible ? "learn.hideNames" : "learn.showNames"));
  },

  // syncURL keeps the address bar on the bird being shown (or the plain
  // hotspot on the final screen) so it can always be copied. replaceState, so
  // swiping through birds adds no history entries and fires no hashchange.
  syncURL() {
    if (this.quiet) return;
    const code = this.done ? "" : this.cards[this.index].speciesCode;
    const hash = code ? birdHash(this.locId, code) : "#/hotspot/" + encodeURIComponent(this.locId);
    if (location.hash !== hash) history.replaceState(null, "", hash);
    currentRoute = Object.assign({}, currentRoute, { speciesCode: code });
  },

  render() {
    this.syncURL();
    const body = $("learnBody");
    body.innerHTML = "";

    if (this.done) {
      $("learnProgress").textContent = "";
      const wrap = document.createElement("div");
      wrap.className = "learn-done";
      const heading = document.createElement("p");
      heading.textContent = t("learn.done");
      const close = document.createElement("button");
      close.type = "button";
      close.className = "btn btn-primary";
      close.textContent = t("learn.close");
      close.addEventListener("click", () => this.finish());
      wrap.append(heading, close);
      body.appendChild(wrap);
      return;
    }

    const item = this.cards[this.index];
    $("learnCreditsLink").href = creditsHash(item.locId || this.locId);
    $("learnProgress").textContent = t("learn.progress", { i: this.index + 1, n: this.cards.length });

    const card = document.createElement("div");
    card.className = "learn-card";

    const slidesWrap = document.createElement("div");
    slidesWrap.className = "learn-slides";
    const urls = item.imageMissing ? [] : (item.imageUrls && item.imageUrls.length ? item.imageUrls : (item.imageUrl ? [item.imageUrl] : []));
    const slides = [];
    if (urls.length === 0) {
      const img = document.createElement("img");
      img.className = "learn-image learn-image-active";
      img.alt = "";
      img.src = item.imageMissing ? MISSING_URL : PLACEHOLDER_URL;
      slidesWrap.appendChild(img);
    } else {
      urls.forEach((url, i) => {
        const img = document.createElement("img");
        img.className = "learn-image" + (i === 0 ? " learn-image-active" : "");
        img.alt = "";
        img.src = PLACEHOLDER_URL;
        slidesWrap.appendChild(img);
        slides.push(img);
      });
    }
    card.appendChild(slidesWrap);

    // Photos within one bird are cycled only by the next-photo button on the
    // image (never by swipe or timer), so the horizontal drag stays
    // unambiguous: it always moves through the deck of birds. Only slides
    // whose photo has actually loaded take part in the cycle, so the dots and
    // the button appear once there is more than one real photo to show.
    const dots = document.createElement("div");
    dots.className = "learn-dots";
    slides.forEach((_, i) => {
      const dot = document.createElement("span");
      dot.className = "learn-dot" + (i === 0 ? " active" : "");
      dot.hidden = true;
      dots.appendChild(dot);
    });
    slidesWrap.appendChild(dots);

    const nextBtn = document.createElement("button");
    nextBtn.type = "button";
    nextBtn.className = "learn-next-photo";
    nextBtn.hidden = true;
    nextBtn.setAttribute("aria-label", t("learn.nextPhoto"));
    nextBtn.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="7" width="14" height="14" rx="2"/><path d="M7 7V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2h-2"/></svg>`;

    let shown = 0;
    const isLoaded = (i) => slides[i].dataset.loaded === "1";
    const refreshControls = () => {
      const loaded = slides.filter((_, i) => isLoaded(i)).length;
      slides.forEach((_, i) => { dots.children[i].hidden = loaded < 2 || !isLoaded(i); });
      nextBtn.hidden = loaded < 2;
    };
    const showSlide = (next) => {
      slides[shown].classList.remove("learn-image-active");
      dots.children[shown].classList.remove("active");
      slides[next].classList.add("learn-image-active");
      dots.children[next].classList.add("active");
      shown = next;
    };
    nextBtn.addEventListener("click", () => {
      let next = shown;
      do {
        next = (next + 1) % slides.length;
      } while (!isLoaded(next) && next !== shown);
      if (next !== shown) showSlide(next);
    });
    slidesWrap.appendChild(nextBtn);

    slides.forEach((img, i) => {
      learnLoadSlide(img, urls[i], i === 0 ? LEARN_PRIMARY_RETRIES : LEARN_SECONDARY_RETRIES, {
        onLoad: refreshControls,
        onGiveUp: () => { if (shown === i) showSlide(0); },
      });
    });

    const shareBtn = document.createElement("button");
    shareBtn.type = "button";
    shareBtn.className = "learn-share";
    shareBtn.setAttribute("aria-label", t("learn.share"));
    shareBtn.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 15V3"/><path d="m7 8 5-5 5 5"/><path d="M5 12v7a2 2 0 0 0 2 2h14"/><path d="M21 12v7"/></svg>`;
    const toast = document.createElement("div");
    toast.className = "learn-toast";
    toast.hidden = true;
    toast.setAttribute("role", "status");
    shareBtn.addEventListener("click", async () => {
      const copied = await shareBird(item);
      if (!copied) return;
      toast.textContent = t("share.linkCopied");
      toast.hidden = false;
      clearTimeout(toast.hideTimer);
      toast.hideTimer = setTimeout(() => { toast.hidden = true; }, 2000);
    });
    const ebird = document.createElement("a");
    ebird.className = "learn-ebird";
    ebird.href = `https://ebird.org/species/${encodeURIComponent(item.speciesCode)}`;
    ebird.target = "_blank";
    ebird.rel = "noopener noreferrer";
    ebird.setAttribute("aria-label", t("learn.ebird"));
    ebird.innerHTML = `<span>eBird</span><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/></svg>`;
    slidesWrap.append(ebird, shareBtn, toast);

    const names = document.createElement("div");
    names.className = "learn-names";
    names.hidden = !this.namesVisible;
    names.innerHTML =
      `<div class="learn-com">${escapeHtml(item.comName)}</div>` +
      (secondaryNameFor(item.speciesCode) ? `<div class="learn-sub">${escapeHtml(secondaryNameFor(item.speciesCode))}</div>` : "") +
      (item.sciName ? `<div class="learn-sci">${escapeHtml(item.sciName)}</div>` : "");
    card.appendChild(names);

    body.appendChild(card);
    this.wireDrag(card);
  },

  // wireDrag lets the card be dragged left/right with the pointer, snapping
  // back short of LEARN_DRAG_THRESHOLD or flying off-screen and advancing/
  // going back past it — dragging left advances (matching the common
  // swipe-left-for-next photo-gallery convention), dragging right goes back.
  wireDrag(card) {
    let startX = 0, dx = 0, dragging = false;

    const onDown = (e) => {
      if (e.target.closest(".learn-next-photo, .learn-share, .learn-ebird")) return;
      dragging = true;
      startX = e.clientX;
      card.setPointerCapture(e.pointerId);
    };
    const onMove = (e) => {
      if (!dragging) return;
      dx = e.clientX - startX;
      card.style.transform = `translateX(${dx}px)`;
    };
    const onUp = () => {
      if (!dragging) return;
      dragging = false;
      card.style.transition = "transform .2s ease";
      if (Math.abs(dx) > LEARN_DRAG_THRESHOLD) {
        const delta = dx < 0 ? 1 : -1;
        card.style.transform = `translateX(${dx < 0 ? "-120%" : "120%"})`;
        setTimeout(() => this.go(delta), 180);
      } else {
        card.style.transform = "";
      }
      dx = 0;
    };

    card.addEventListener("pointerdown", onDown);
    card.addEventListener("pointermove", onMove);
    card.addEventListener("pointerup", onUp);
    card.addEventListener("pointercancel", onUp);
  },
};

async function startLearn() {
  if (!state.hotspot) return;
  const btn = $("learnStart");
  btn.disabled = true;
  try {
    await loadAndStartLearn();
  } finally {
    btn.disabled = false;
  }
}

async function loadAndStartLearn(speciesCode) {
  setHotspotStatus(t("learn.building"));
  let species;
  try {
    const res = await fetch(`/api/hotspots/${encodeURIComponent(state.hotspot.locId)}/species?lang=${encodeURIComponent(state.language)}&mode=popularity`);
    if (!res.ok) { setHotspotStatus(t("learn.error", { status: res.status })); return; }
    species = await res.json();
  } catch (e) {
    setHotspotStatus(t("learn.error", { status: "?" }));
    return;
  }
  if (!species || species.length === 0) {
    setHotspotStatus(t("learn.none"));
    return;
  }
  let index = 0;
  if (speciesCode) {
    index = species.findIndex((sp) => sp.speciesCode === speciesCode);
    if (index < 0) {
      setHotspotStatus(t("learn.birdNotFound"));
      history.replaceState(null, "", "#/hotspot/" + encodeURIComponent(state.hotspot.locId));
      currentRoute = Object.assign({}, currentRoute, { speciesCode: "" });
      return;
    }
  }
  await loadSecondaryNames(state.hotspot.locId);
  setHotspotStatus("");
  learn.start(species, state.hotspot.locId, index);
}

// ---- Wiring ---------------------------------------------------------------

function wireEvents() {
  wirePlaceSearch();
  wireSortMenu();
  $("homeEditToggle").addEventListener("click", () => {
    homeEditing = !homeEditing;
    renderHomeFavorites();
  });
  $("searchHere").addEventListener("click", () => {
    const center = map.getCenter();
    $("lat").value = center.lat.toFixed(6);
    $("lng").value = center.lng.toFixed(6);
    findHotspots();
  });
  $("useLocation").addEventListener("click", () => {
    if (!navigator.geolocation) { setStatus(t("search.geoUnsupported")); return; }
    setStatus(t("search.geoRequesting"));
    navigator.geolocation.getCurrentPosition(
      (pos) => {
        $("lat").value = pos.coords.latitude.toFixed(6);
        $("lng").value = pos.coords.longitude.toFixed(6);
        map.setView([pos.coords.latitude, pos.coords.longitude], 13);
        findHotspots();
      },
      (err) => {
        setStatus(t("search.geoError", { message: err.message }));
      },
      { enableHighAccuracy: true, timeout: 10000 }
    );
  });

  $("shareHotspotBtn").addEventListener("click", async () => {
    if (!state.hotspot) return;
    const h = state.hotspot;
    const name = h.locName || h.locId;
    const copied = await shareLink(name, t("hotspot.shareText", { place: name }), location.origin + location.pathname + "#/hotspot/" + encodeURIComponent(h.locId));
    if (copied) setHotspotStatus(t("share.linkCopied"));
  });

  $("bookmarkBtn").addEventListener("click", async () => {
    if (!state.hotspot) return;
    if (!state.signedIn) {
      window.location = "/login?next=" + encodeURIComponent("/" + location.hash);
      return;
    }
    const saved = state.favorites.some((f) => f.locId === state.hotspot.locId);
    const btn = $("bookmarkBtn");
    btn.disabled = true;
    try {
      if (saved) await deleteFavorite(state.hotspot.locId);
      else await putFavorite(state.hotspot);
      updateBookmarkButton();
    } catch (e) {
      setHotspotStatus(t("hotspot.bookmarkError"));
    } finally {
      btn.disabled = false;
    }
  });

  for (const radio of document.querySelectorAll('input[name="mode"]')) {
    radio.addEventListener("change", () => { if (state.hotspot) loadSpecies(); });
  }

  $("learnStart").addEventListener("click", startLearn);
  // A plain click on a bird opens Learn on it; modified clicks fall through
  // to the link (its href is the shareable bird URL).
  $("groups").addEventListener("click", (e) => {
    const card = e.target.closest("a.card");
    if (!card || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    loadAndStartLearn(card.dataset.code);
  });
  $("learnClose").addEventListener("click", () => learn.finish());
  $("learnPrev").addEventListener("click", () => learn.go(-1));
  $("learnNext").addEventListener("click", () => learn.go(1));
  $("learnToggleNames").addEventListener("click", () => learn.toggleNames());

  $("primaryLang").addEventListener("change", onPrimaryLanguageChange);
  $("secondaryLang").addEventListener("change", onSecondaryLanguageChange);
  $("logoutBtn").addEventListener("click", onLogout);
}

// ---- Boot -----------------------------------------------------------------

async function init() {
  I18N = await loadTranslations();
  await initAccount();

  // The pre-existing deep link /?locId=…&lang=…&mode=… keeps working: lang
  // overrides the account preference for this load, mode picks the browse
  // ordering, and locId lands on that hotspot's view.
  const params = new URLSearchParams(location.search);
  const qLang = params.get("lang");
  if (VALID_LANGS.includes(qLang)) state.language = qLang;
  const qMode = params.get("mode");
  if (BROWSE_MODES.includes(qMode)) {
    const radio = document.querySelector(`input[name="mode"][value="${qMode}"]`);
    if (radio) radio.checked = true;
  }

  applyI18n();
  initMap();
  wireEvents();

  const locId = params.get("locId");
  if (locId && /^-?\d+(\.\d+)?,-?\d+(\.\d+)?$/.test(locId)) {
    history.replaceState(null, "", "#/hotspot/" + encodeURIComponent(locId));
  } else if (!location.hash) {
    history.replaceState(null, "", "#/home");
  }

  window.addEventListener("hashchange", () => { updateAccountChrome(); onHashChange(); });
  await renderRoute();
  setStatus(t("search.status.initial"));
}

init();
