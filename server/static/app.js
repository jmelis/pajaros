"use strict";

// pajaros server app. A hash-routed single page with five views: home, search,
// hotspot, mastered and settings. Hash routing survives a reload and is
// linkable without server rewrites. All user-visible text comes from
// i18n.json; this file holds translation keys, never literals.

const $ = (id) => document.getElementById(id);
const VALID_LANGS = ["en", "fr", "es"];
const BROWSE_MODES = ["popularity", "category", "alphabetical"];
const VIEW_NAMES = ["home", "search", "hotspot", "mastered", "settings"];

// ---- Account / UI state ---------------------------------------------------

const state = {
  language: "es",
  secondaryLanguage: "",
  starRewards: false,
  stars: 0,
  favorites: [],
  // speciesCode -> name in the secondary language, for the current hotspot.
  secondaryNames: {},
  // The current hotspot's species in the primary language, used to build
  // multiple-choice options when a recall card is requeued after a wrong answer.
  species: [],
  // { locId, locName, lat, lng } — the hotspot the hotspot view is showing.
  hotspot: null,
  // Whether the search view has run its first hotspot search.
  searchLoaded: false,
};

let currentRoute = { name: "home", locId: "" };

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
    return { name: "hotspot", locId: parts[1] ? decodeURIComponent(parts[1]) : "" };
  }
  if (VIEW_NAMES.indexOf(name) !== -1) return { name, locId: "" };
  return { name: "home", locId: "" };
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
  updateNav(route.name);

  if (route.name === "home") await renderHome();
  else if (route.name === "search") showSearch();
  else if (route.name === "hotspot") await openHotspot(route.locId);
  else if (route.name === "mastered") await renderMastered();
  else if (route.name === "settings") await renderSettings();
}

async function onHashChange() {
  const next = parseRoute();
  if (quiz.active && !(next.name === "hotspot" && next.locId === quiz.locId)) {
    if (!confirm(t("quiz.leave"))) {
      // Restore the hotspot URL without firing another hashchange.
      history.replaceState(null, "", "#/hotspot/" + encodeURIComponent(quiz.locId));
      return;
    }
    quiz.finish();
  }
  await renderRoute();
}

// ---- Account --------------------------------------------------------------

function syncProfile(p) {
  if (VALID_LANGS.includes(p.language)) state.language = p.language;
  state.secondaryLanguage = VALID_LANGS.includes(p.secondaryLanguage) ? p.secondaryLanguage : "";
  state.starRewards = !!p.starRewards;
  state.stars = p.stars || 0;
  state.favorites = Array.isArray(p.favorites) ? p.favorites : [];
}

async function initAccount() {
  try {
    const res = await fetch("/api/me");
    if (res.ok) syncProfile(await res.json());
  } catch (e) {
    // Open/dev mode already works without this; keep the defaults.
  }
}

// ---- Home -----------------------------------------------------------------

function renderProgress(el, st) {
  el.innerHTML =
    `<span class="stat"><strong>${st.speciesMastered || 0}</strong> ${t("stats.mastered")}</span>` +
    `<span class="stat"><strong>${st.speciesLearning || 0}</strong> ${t("stats.learning")}</span>` +
    `<span class="stat"><strong>${st.dueForReview || 0}</strong> ${t("stats.due")}</span>` +
    (state.starRewards ? `<span class="stat"><strong>⭐ ${st.totalStars || 0}</strong> ${t("stats.stars")}</span>` : "");
}

function renderHomeFavorites() {
  const list = $("homeFavorites");
  list.innerHTML = "";
  for (const f of state.favorites) {
    const li = document.createElement("li");
    li.className = "hotspot-item";
    const a = document.createElement("a");
    a.className = "hotspot-link";
    a.href = "#/hotspot/" + encodeURIComponent(f.locId);
    a.textContent = f.locName || f.locId;
    const remove = document.createElement("button");
    remove.className = "btn btn-secondary btn-sm";
    remove.type = "button";
    remove.textContent = t("home.unbookmark");
    remove.addEventListener("click", async () => {
      try {
        await deleteFavorite(f.locId);
      } catch (e) {
        showSettingsHint(t("search.bookmarkError"));
        return;
      }
      renderHomeFavorites();
      $("homeNoFavorites").hidden = state.favorites.length > 0;
    });
    li.append(a, remove);
    list.appendChild(li);
  }
  $("homeNoFavorites").hidden = state.favorites.length > 0;
}

async function renderHome() {
  let profile, stats;
  try {
    const [pRes, stRes] = await Promise.all([fetch("/api/me"), fetch("/api/me/progress")]);
    if (!pRes.ok || !stRes.ok) throw new Error("load");
    profile = await pRes.json();
    stats = await stRes.json();
  } catch (e) {
    $("homeFirstRun").hidden = true;
    $("homeHotspotsPanel").hidden = false;
    $("homeProgressPanel").hidden = false;
    $("homeFavorites").innerHTML = "";
    $("homeNoFavorites").hidden = true;
    $("homeProgress").innerHTML = `<span class="stat">${t("home.loadFailed")}</span>`;
    return;
  }

  syncProfile(profile);
  const hasFavorites = state.favorites.length > 0;
  const hasProgress = (stats.speciesMastered || 0) + (stats.speciesLearning || 0) + (stats.totalStars || 0) > 0;
  const firstRun = !hasFavorites && !hasProgress;

  $("homeFirstRun").hidden = !firstRun;
  $("homeHotspotsPanel").hidden = firstRun;
  $("homeProgressPanel").hidden = firstRun;
  renderHomeFavorites();
  renderProgress($("homeProgress"), stats);
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
}

function clearHotspotMarkers() {
  for (const m of hotspotMarkers) map.removeLayer(m);
  hotspotMarkers = [];
}

function popupHTML(h, withStats) {
  const saved = state.favorites.some((f) => f.locId === h.locId);
  const stats = withStats
    ? `<br>${tn("search.checklists", h.numChecklistsAllTime || 0)} · ${tn("search.speciesCount", h.numSpeciesAllTime || 0)}`
    : "";
  return `
    <strong class="popup-title">${escapeHtml(h.locName)}</strong>${stats}
    <div class="popup-actions">
      <button class="popup-btn open-hotspot-btn" type="button" data-locid="${h.locId}" data-locname="${escapeHtml(h.locName)}" data-lat="${h.lat}" data-lng="${h.lng}">${t("search.open")}</button>
      <button class="popup-btn popup-btn-secondary bookmark-hotspot-btn" type="button" data-locid="${h.locId}" data-locname="${escapeHtml(h.locName)}" data-lat="${h.lat}" data-lng="${h.lng}"${saved ? " disabled" : ""}>${saved ? t("search.bookmarked") : t("search.bookmark")}</button>
    </div>`;
}

const MIN_MARKER_RADIUS = 5;
const MAX_MARKER_RADIUS = 20;

// Checklist counts are heavily right-skewed (a handful of hotspots run into
// the tens of thousands, most are far smaller), so a log scale spreads that
// out much better than sqrt does. Scaled relative to the busiest hotspot in
// *this* search rather than a fixed global max, so the full size range is
// always used whether the area's counts run into the tens or the
// tens-of-thousands — the tradeoff is that the same hotspot can render at a
// different size depending on what else is nearby in the current search.
function markerRadius(checklists, maxChecklists) {
  if (maxChecklists <= 0) return MIN_MARKER_RADIUS;
  const t2 = Math.log1p(checklists || 0) / Math.log1p(maxChecklists);
  return MIN_MARKER_RADIUS + t2 * (MAX_MARKER_RADIUS - MIN_MARKER_RADIUS);
}

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

  clearHotspotMarkers();
  if (userMarker) map.removeLayer(userMarker);
  userMarker = L.marker([lat, lng]).addTo(map).bindPopup(t("search.youAreHere"));

  const bounds = L.latLngBounds([[lat, lng]]);
  const maxChecklists = Math.max(0, ...hotspots.map((h) => h.numChecklistsAllTime || 0));
  for (const h of hotspots) {
    const marker = L.circleMarker([h.lat, h.lng], {
      radius: markerRadius(h.numChecklistsAllTime, maxChecklists),
      color: "#2a7d4f",
      fillColor: "#2a7d4f",
      fillOpacity: 0.6,
    }).addTo(map).bindPopup(popupHTML(h, true));
    hotspotMarkers.push(marker);
    bounds.extend([h.lat, h.lng]);
  }
  if (hotspots.length > 0) map.fitBounds(bounds, { padding: [30, 30] });
  setStatus(tn("search.status.found", hotspots.length));
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
  const open = e.target.closest(".open-hotspot-btn");
  if (open) {
    navigate("#/hotspot/" + encodeURIComponent(open.dataset.locid));
    return;
  }
  const bookmark = e.target.closest(".bookmark-hotspot-btn");
  if (!bookmark) return;

  const fav = {
    locId: bookmark.dataset.locid,
    locName: bookmark.dataset.locname,
    lat: parseFloat(bookmark.dataset.lat) || 0,
    lng: parseFloat(bookmark.dataset.lng) || 0,
  };
  const original = bookmark.textContent;
  bookmark.disabled = true;
  bookmark.textContent = t("search.bookmarked");
  try {
    await putFavorite(fav);
  } catch (err) {
    bookmark.disabled = false;
    bookmark.textContent = original;
    setStatus(t("search.bookmarkError"));
  }
});

// ---- Hotspot + species browsing ------------------------------------------

function currentBrowseMode() {
  const r = document.querySelector('input[name="mode"]:checked');
  return r ? r.value : "category";
}

function currentHotspotMode() {
  const r = document.querySelector('input[name="hotspotMode"]:checked');
  return r ? r.value : "browse";
}

function applyHotspotMode() {
  const quizMode = currentHotspotMode() === "quiz";
  $("browseControls").hidden = quizMode;
  $("quizControls").hidden = !quizMode;
}

function updateBookmarkButton() {
  if (!state.hotspot) return;
  const saved = state.favorites.some((f) => f.locId === state.hotspot.locId);
  const btn = $("bookmarkBtn");
  btn.textContent = saved ? t("hotspot.bookmarked") : t("hotspot.bookmark");
  btn.setAttribute("aria-pressed", saved ? "true" : "false");
}

async function openHotspot(locId) {
  if (!/^L\d+$/.test(locId)) { navigate("#/home"); return; }

  if (!state.hotspot || state.hotspot.locId !== locId) {
    state.hotspot = { locId, locName: "", lat: 0, lng: 0 };
    state.species = [];
    state.secondaryNames = {};
    $("hotspotName").textContent = locId;
    setHotspotStatus(t("hotspot.loading"));
    try {
      const res = await fetch(`/api/hotspots/${encodeURIComponent(locId)}`);
      if (!res.ok) { setHotspotStatus(t("hotspot.error")); return; }
      const h = await res.json();
      state.hotspot = { locId, locName: h.locName || locId, lat: h.lat, lng: h.lng };
    } catch (e) {
      setHotspotStatus(t("hotspot.error"));
      return;
    }
  }

  $("hotspotName").textContent = state.hotspot.locName || state.hotspot.locId;
  updateBookmarkButton();
  applyHotspotMode();
  await loadSpecies();
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
  card.href = `https://ebird.org/species/${encodeURIComponent(sp.speciesCode)}`;
  card.target = "_blank";
  card.rel = "noopener noreferrer";
  card.innerHTML = `
    <img src="${sp.imageMissing ? MISSING_URL : PLACEHOLDER_URL}" alt="${escapeHtml(sp.comName)}">
    <div class="card-body">
      <div class="com">${escapeHtml(sp.comName)}</div>
      ${secondaryNameFor(sp.speciesCode) ? `<div class="sub">${escapeHtml(secondaryNameFor(sp.speciesCode))}</div>` : ""}
      <div class="sci">${escapeHtml(sp.sciName)}</div>
      ${sp.nearbyCount != null ? `<div class="nearby">${escapeHtml(tn("browse.nearbyReports", sp.nearbyCount))}</div>` : ""}
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

// ---- Mastered -------------------------------------------------------------

function masteredCard(b) {
  const card = document.createElement("a");
  card.className = "card";
  card.href = `https://ebird.org/species/${encodeURIComponent(b.speciesCode)}`;
  card.target = "_blank";
  card.rel = "noopener noreferrer";

  const img = document.createElement("img");
  img.alt = b.comName;
  img.src = b.imageMissing ? MISSING_URL : PLACEHOLDER_URL;
  card.appendChild(img);

  const body = document.createElement("div");
  body.className = "card-body";
  const com = document.createElement("div");
  com.className = "com";
  com.textContent = b.comName;
  body.appendChild(com);
  if (b.secondaryName) {
    const sub = document.createElement("div");
    sub.className = "sub";
    sub.textContent = b.secondaryName;
    body.appendChild(sub);
  }
  if (b.sciName) {
    const sci = document.createElement("div");
    sci.className = "sci";
    sci.textContent = b.sciName;
    body.appendChild(sci);
  }
  const stats = document.createElement("div");
  stats.className = "mastered-stats";
  for (const text of [t("mastered.box", { box: b.box }), tn("mastered.seen", b.seenCount), tn("mastered.correct", b.correctCount)]) {
    const span = document.createElement("span");
    span.textContent = text;
    stats.appendChild(span);
  }
  body.appendChild(stats);
  card.appendChild(body);

  if (b.imageUrl && !b.imageMissing) preloadAndSwap(img, b.imageUrl);
  return card;
}

async function renderMastered() {
  $("masteredGrid").innerHTML = "";
  $("masteredEmpty").hidden = true;
  $("masteredStatus").textContent = t("mastered.loading");

  let birds;
  try {
    const res = await fetch(`/api/me/mastered?lang=${encodeURIComponent(state.language)}`);
    if (!res.ok) { $("masteredStatus").textContent = t("mastered.error"); return; }
    birds = await res.json();
  } catch (e) {
    $("masteredStatus").textContent = t("mastered.error");
    return;
  }
  $("masteredStatus").textContent = "";
  if (!birds || birds.length === 0) { $("masteredEmpty").hidden = false; return; }
  for (const b of birds) $("masteredGrid").appendChild(masteredCard(b));
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
  buildLanguageSelects();
  $("starRewards").checked = state.starRewards;
  try {
    const [pRes, stRes] = await Promise.all([fetch("/api/me"), fetch("/api/me/progress")]);
    if (pRes.ok) syncProfile(await pRes.json());
    if (stRes.ok) renderProgress($("settingsStats"), await stRes.json());
    else $("settingsStats").innerHTML = "";
  } catch (e) {
    showSettingsHint(t("settings.loadError"));
  }
  buildLanguageSelects();
  $("starRewards").checked = state.starRewards;
}

async function onPrimaryLanguageChange() {
  const lang = $("primaryLang").value;
  if (!VALID_LANGS.includes(lang)) return;
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
  state.language = lang;
  applyI18n();
  buildLanguageSelects();
  if (currentRoute.name === "home") await renderHome();
  else if (currentRoute.name === "mastered") await renderMastered();
  else if (currentRoute.name === "hotspot") await loadSpecies();
}

async function onSecondaryLanguageChange() {
  const lang = $("secondaryLang").value;
  if (lang !== "" && !VALID_LANGS.includes(lang)) return;
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
  state.secondaryLanguage = lang;
  if (currentRoute.name === "hotspot") await loadSpecies();
}

async function onStarRewardsChange() {
  const on = $("starRewards").checked;
  try {
    const res = await fetch("/api/me/star-rewards", {
      method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ starRewards: on }),
    });
    if (!res.ok) throw new Error(String(res.status));
  } catch (e) {
    $("starRewards").checked = !on;
    showSettingsHint(t("settings.errorStarRewards"));
    return;
  }
  state.starRewards = on;
  updateQuizStars();
  if (currentRoute.name === "home") await renderHome();
}

async function onResetProgress() {
  if (!confirm(t("settings.confirmReset"))) return;
  try {
    const res = await fetch("/api/me/progress", { method: "DELETE" });
    if (!res.ok) throw new Error(String(res.status));
  } catch (e) {
    showSettingsHint(t("settings.errorReset"));
    return;
  }
  state.stars = 0;
  await renderSettings();
}

async function onLogout() {
  try {
    await fetch("/auth/logout", { method: "POST" });
  } catch (e) {
    // Either way, send the browser to the sign-in page.
  }
  window.location = "/login";
}

// ---- Quiz -----------------------------------------------------------------

// The star counter is repainted the moment an answer is scored (see answer),
// not only when the next question renders, so it moves immediately — including
// on a session's last question.
function updateQuizStars() {
  const el = $("quizStars");
  if (!state.starRewards) { el.hidden = true; el.textContent = ""; return; }
  el.hidden = false;
  el.textContent = t("quiz.stars", { n: state.stars });
}

// The star burst is part of the star-rewards flourish only.
function starBurst() {
  if (!state.starRewards) return;
  const el = document.createElement("div");
  el.className = "star-burst";
  el.textContent = "★";
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 900);
}

// multipleChoiceChoices builds a multiple-choice option set for a card being
// re-presented after a wrong answer reset it to box 1. Recall items carry no
// server-built choices, so distractors are drawn from the current hotspot's
// species (the same "any other species here" source the server falls back to).
function multipleChoiceChoices(item) {
  const choices = [{ speciesCode: item.speciesCode, comName: item.comName }];
  const seen = new Set([item.speciesCode]);
  for (const sp of state.species) {
    if (seen.has(sp.speciesCode)) continue;
    seen.add(sp.speciesCode);
    choices.push({ speciesCode: sp.speciesCode, comName: sp.comName });
    if (choices.length === 4) break;
  }
  for (let i = choices.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [choices[i], choices[j]] = [choices[j], choices[i]];
  }
  return choices;
}

// requeuedItem returns a copy of item whose question type matches box, the
// card's box *after* the answer: box 2+ is recall, boxes 0-1 are multiple
// choice. A card reset to box 1 keeps its existing choices when it has them
// and otherwise gets a fresh set.
function requeuedItem(item, box) {
  const next = Object.assign({}, item);
  next.questionType = box >= 2 ? "recall" : "multipleChoice";
  if (next.questionType === "recall") {
    delete next.choices;
  } else if (!next.choices || next.choices.length < 2) {
    next.choices = multipleChoiceChoices(item);
  }
  return next;
}

const quiz = {
  active: false,
  locId: "",
  queue: [],
  index: 0,
  answered: 0,
  correct: 0,
  starsEarned: 0,
  mastered: false,

  start(items, locId) {
    this.queue = items.slice();
    this.locId = locId;
    this.index = 0;
    this.answered = 0;
    this.correct = 0;
    this.starsEarned = 0;
    this.mastered = false;
    this.active = true;
    $("quizOverlay").hidden = false;
    updateQuizStars();
    this.render();
  },

  finish() {
    this.active = false;
    $("quizOverlay").hidden = true;
  },

  render() {
    const body = $("quizBody");
    body.innerHTML = "";
    if (this.index >= this.queue.length) { this.renderSummary(); return; }
    const item = this.queue[this.index];

    $("quizProgress").textContent = t("quiz.question", { i: this.index + 1, n: this.queue.length });
    updateQuizStars();

    const img = document.createElement("img");
    img.className = "quiz-image";
    img.alt = "";
    img.src = item.imageMissing ? MISSING_URL : PLACEHOLDER_URL;
    body.appendChild(img);
    if (item.imageUrl && !item.imageMissing) preloadAndSwap(img, item.imageUrl);

    if (item.questionType === "multipleChoice") {
      const prompt = document.createElement("p");
      prompt.className = "quiz-prompt";
      prompt.textContent = t("quiz.which");
      body.appendChild(prompt);
      const choices = document.createElement("div");
      choices.className = "quiz-choices";
      for (const c of item.choices || []) {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "btn btn-secondary quiz-choice";
        btn.textContent = c.comName;
        btn.dataset.speciesCode = c.speciesCode;
        btn.addEventListener("click", () => this.answer(item, c.speciesCode === item.speciesCode, btn));
        choices.appendChild(btn);
      }
      body.appendChild(choices);
      return;
    }

    // Recall: photo only, then reveal, then self-grade.
    const showBtn = document.createElement("button");
    showBtn.type = "button";
    showBtn.className = "btn btn-primary quiz-show";
    showBtn.textContent = t("quiz.show");

    const reveal = document.createElement("div");
    reveal.className = "quiz-reveal";
    reveal.hidden = true;
    reveal.innerHTML =
      `<div class="quiz-reveal-name">${escapeHtml(item.comName)}</div>` +
      (secondaryNameFor(item.speciesCode) ? `<div class="quiz-reveal-sub">${escapeHtml(secondaryNameFor(item.speciesCode))}</div>` : "") +
      (item.sciName ? `<div class="quiz-reveal-sci">${escapeHtml(item.sciName)}</div>` : "");

    const grade = document.createElement("div");
    grade.className = "quiz-grade";
    grade.hidden = true;
    const rightBtn = document.createElement("button");
    rightBtn.type = "button";
    rightBtn.className = "btn btn-primary";
    rightBtn.textContent = t("quiz.right");
    rightBtn.addEventListener("click", () => this.answer(item, true));
    const wrongBtn = document.createElement("button");
    wrongBtn.type = "button";
    wrongBtn.className = "btn btn-secondary";
    wrongBtn.textContent = t("quiz.wrong");
    wrongBtn.addEventListener("click", () => this.answer(item, false));
    grade.append(rightBtn, wrongBtn);

    showBtn.addEventListener("click", () => {
      reveal.hidden = false;
      showBtn.hidden = true;
      grade.hidden = false;
    });

    body.append(showBtn, reveal, grade);
  },

  async answer(item, correct, chosenBtn) {
    const buttons = $("quizBody").querySelectorAll("button");
    for (const b of buttons) b.disabled = true;

    // Multiple choice needs an explicit right/wrong reveal: mark the correct
    // option green always, and the tapped option red when it was the wrong
    // one, so a mistake is visible instead of silently moving on.
    if (item.questionType === "multipleChoice") {
      for (const b of buttons) {
        if (b.dataset.speciesCode === item.speciesCode) b.classList.add("quiz-choice-correct");
        else if (b === chosenBtn) b.classList.add("quiz-choice-incorrect");
      }
    }

    let answer = null;
    try {
      const res = await fetch(`/api/me/progress/${encodeURIComponent(item.speciesCode)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ correct }),
      });
      if (res.ok) answer = await res.json();
    } catch (e) {
      // A scoring failure shouldn't strand the quiz; keep going.
    }

    if (answer) {
      state.stars = answer.totalStars;
      this.starsEarned += answer.starsEarned;
      if (answer.newlyMastered) this.mastered = true;
    }
    // Repaint the counter immediately, before the next question renders.
    updateQuizStars();

    this.answered++;
    if (correct) {
      this.correct++;
      starBurst();
    } else {
      // Reinsert a handful of questions later instead of right after itself,
      // as a fresh item whose format matches the card's reset (box 1) state.
      // A failed answer call leaves the box unknown; a wrong answer resets to
      // box 1 regardless, so fall back to that.
      const requeued = requeuedItem(item, answer ? answer.box : 1);
      const pos = this.index + 4;
      if (pos >= this.queue.length) this.queue.push(requeued);
      else this.queue.splice(pos, 0, requeued);
    }
    this.index++;
    // Multiple choice just colored the buttons above; give that a moment to
    // register before it's replaced by the next question. Recall already
    // showed its reveal before the self-grade tap, so it can advance at once.
    if (item.questionType === "multipleChoice") setTimeout(() => this.render(), 1100);
    else this.render();
  },

  renderSummary() {
    $("quizProgress").textContent = t("quiz.complete");
    updateQuizStars();
    const body = $("quizBody");
    body.innerHTML = "";
    const pct = this.answered ? Math.round((this.correct / this.answered) * 100) : 0;

    const wrap = document.createElement("div");
    wrap.className = "quiz-summary";

    const heading = document.createElement("h2");
    heading.textContent = state.starRewards && this.mastered ? t("quiz.mastered") : t("quiz.complete");

    const score = document.createElement("p");
    score.className = "quiz-score";
    score.textContent = t("quiz.score", { correct: this.correct, answered: this.answered, pct });

    wrap.appendChild(heading);
    wrap.appendChild(score);
    if (state.starRewards) {
      const earned = document.createElement("p");
      earned.className = "quiz-earned";
      earned.textContent = t("quiz.earned", { n: this.starsEarned });
      wrap.appendChild(earned);
    }

    const actions = document.createElement("div");
    actions.className = "actions";
    const again = document.createElement("button");
    again.type = "button";
    again.className = "btn btn-primary";
    again.textContent = t("quiz.again");
    again.addEventListener("click", () => startQuiz());
    const done = document.createElement("button");
    done.type = "button";
    done.className = "btn btn-secondary";
    done.textContent = t("quiz.done");
    done.addEventListener("click", () => quiz.finish());
    actions.append(again, done);

    wrap.appendChild(actions);
    body.appendChild(wrap);
  },
};

async function startQuiz() {
  if (!state.hotspot) return;
  const count = $("quizCount").value;
  setHotspotStatus(t("quiz.building"));
  let session;
  try {
    const res = await fetch(`/api/hotspots/${encodeURIComponent(state.hotspot.locId)}/quiz?count=${encodeURIComponent(count)}&lang=${encodeURIComponent(state.language)}`);
    if (!res.ok) { setHotspotStatus(t("quiz.error", { status: res.status })); return; }
    session = await res.json();
  } catch (e) {
    setHotspotStatus(t("quiz.error", { status: "?" }));
    return;
  }
  if (!session.items || session.items.length === 0) {
    setHotspotStatus(t("quiz.none"));
    return;
  }
  setHotspotStatus("");
  quiz.start(session.items, state.hotspot.locId);
}

// ---- Wiring ---------------------------------------------------------------

function wireEvents() {
  $("findHotspots").addEventListener("click", findHotspots);
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

  $("bookmarkBtn").addEventListener("click", async () => {
    if (!state.hotspot) return;
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
  for (const radio of document.querySelectorAll('input[name="hotspotMode"]')) {
    radio.addEventListener("change", applyHotspotMode);
  }

  $("quizStart").addEventListener("click", startQuiz);
  $("quizClose").addEventListener("click", () => quiz.finish());

  $("primaryLang").addEventListener("change", onPrimaryLanguageChange);
  $("secondaryLang").addEventListener("change", onSecondaryLanguageChange);
  $("starRewards").addEventListener("change", onStarRewardsChange);
  $("resetProgress").addEventListener("click", onResetProgress);
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
  if (locId && /^L\d+$/.test(locId)) {
    history.replaceState(null, "", "#/hotspot/" + encodeURIComponent(locId));
  } else if (!location.hash) {
    history.replaceState(null, "", "#/home");
  }

  window.addEventListener("hashchange", onHashChange);
  await renderRoute();
  setStatus(t("search.status.initial"));
}

init();
