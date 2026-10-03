"use strict";

// birdsnearby server app. A hash-routed single page: home, search, area,
// compare, credits, about and settings. An "area" is a place from the gazetteer
// (key p<id>) or a custom circle (key c<lat>,<lng>,<km>); the species shown are
// pooled over it by the server. Hash routing survives a reload and is linkable
// without server rewrites. All user-visible text comes from i18n.json; this
// file holds translation keys, never literals.

const $ = (id) => document.getElementById(id);
const VALID_LANGS = ["ca", "cs", "da", "de", "en", "eo", "es", "fi", "fr", "hr", "it", "ja", "lt", "nb", "nl", "pl", "pt", "ru", "sk", "sv", "tr", "uk", "zh"];
const BROWSE_MODES = ["popularity", "category", "alphabetical", "seasonality"];
const DEFAULT_BROWSE_MODE = "popularity";
const VIEW_NAMES = ["home", "search", "range", "area", "credits", "compare", "about", "settings"];
const SPECIES_CODE_RE = /^[A-Za-z0-9_-]+$/;
const AREA_KEY_RE = /^(p[1-9]\d*|c-?\d+\.\d{3},-?\d+\.\d{3},\d+\.\d)$/;
const MIN_RADIUS_KM = 1;
const MAX_RADIUS_KM = 500;
const DEFAULT_RADIUS_KM = 5;

// ---- Account / UI state ---------------------------------------------------

const state = {
  language: "en",
  secondaryLanguage: "",
  favorites: [],
  email: "",
  // False for a visitor who has not signed in: they can browse everything but
  // cannot save places, and their language choices live in localStorage.
  signedIn: false,
  // speciesCode -> name in the secondary language, for the current area.
  secondaryNames: {},
  // The current area's species in the primary language (Browse's current
  // sort order).
  species: [],
  // The area the area view is showing: /api/places/{key}'s AreaInfo.
  area: null,
  // Calendar month the species lists are limited to (1-12, all years pooled),
  // or 0 for the whole year.
  month: 0,
  // True when the selection is "this month" rather than a fixed month.
  monthIsCurrent: false,
};

let currentRoute = { name: "home", key: "", speciesCode: "" };

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
  updateMonthOptions();
  updateRangeMonthOptions();
}

function monthName(m) {
  const name = new Intl.DateTimeFormat(state.language, { month: "long", timeZone: "UTC" }).format(new Date(Date.UTC(2000, m - 1, 1)));
  return name.charAt(0).toLocaleUpperCase(state.language) + name.slice(1);
}

// seasonChart draws a species' 12 monthly bars (January first, heights 0..100
// relative to its own best month) as a tiny inline SVG. A month with no
// records is a faint baseline, so gaps read as gaps.
function seasonChart(bars, name) {
  const H = 22, W = 12, LABEL = 9;
  const peak = bars.indexOf(Math.max(...bars)) + 1; // -1 (no data) never wins
  const cols = bars.map((v, i) => {
    const h = v > 0 ? Math.max(1, Math.round((v / 100) * H)) : 1;
    const month = v < 0 ? t("browse.noDataMonth", { month: monthName(i + 1) }) : monthName(i + 1);
    const initial = escapeHtml(Array.from(month)[0].toUpperCase());
    return `<g><title>${escapeHtml(month)}</title>` +
      (v < 0
        ? ""
        : `<rect x="${i * W + 1}" y="${H - h}" width="${W - 2}" height="${h}"${v ? "" : ' opacity="0.25"'}/>`) +
      `<text x="${i * W + W / 2}" y="${H + LABEL}" text-anchor="middle">${initial}</text></g>`;
  }).join("");
  return `<svg class="season-chart" viewBox="0 0 ${12 * W} ${H + LABEL + 1}" role="img" aria-label="${escapeHtml(t("browse.peaksIn", { name, month: monthName(peak) }))}">${cols}</svg>`;
}

function currentMonth() {
  return new Date().getMonth() + 1;
}

// "All year", "This month (<name>)", then the twelve months, named in the
// UI language. "This month" is its own entry so the choice reads as
// "now" rather than as a fixed month.
function updateMonthOptions() {
  const sel = $("monthSelect");
  if (!sel) return;
  sel.innerHTML = "";
  const add = (value, text) => {
    const opt = document.createElement("option");
    opt.value = value;
    opt.textContent = text;
    sel.appendChild(opt);
  };
  add("0", t("browse.allYear"));
  add("now", t("browse.thisMonth", { month: monthName(currentMonth()) }));
  for (let m = 1; m <= 12; m++) add(String(m), monthName(m));
  sel.value = state.monthIsCurrent ? "now" : String(state.month);
}

// The query suffix that limits /species to the selected month.
function monthParam() {
  return effectiveMonth() ? `&month=${effectiveMonth()}` : "";
}

// The Seasons view looks at the whole year, so the month selection doesn't
// apply to it (nor to the Learn deck and links opened from it).
function effectiveMonth() {
  return currentBrowseMode() === "seasonality" ? 0 : state.month;
}

function noneInMonthText(areaName) {
  return t("browse.noneInMonth", { name: areaName, month: monthName(state.month) });
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

// Images are fetched server-side in the background (see
// /api/places/{key}/species), so a card's imageUrl often 404s at first.
// Preload off-DOM and only swap the visible <img> once it actually loads,
// retrying for a couple of minutes — long enough to cover a big area's
// background-fetch queue — before
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
function setAreaStatus(msg) { $("areaStatus").textContent = msg; }
function showSettingsHint(msg) {
  const hint = $("settingsHint");
  hint.textContent = msg;
  hint.hidden = false;
}

// ---- Analytics ------------------------------------------------------------

// Umami is cookieless and anonymous; nothing about the signed-in account is
// ever sent to it. The script only loads when the server is configured with it.
async function initAnalytics() {
  try {
    const res = await fetch("/api/analytics");
    if (!res.ok) return;
    const { scriptUrl, websiteId } = await res.json();
    if (!scriptUrl || !websiteId) return;
    const s = document.createElement("script");
    s.defer = true;
    s.src = scriptUrl;
    s.dataset.websiteId = websiteId;
    s.dataset.autoTrack = "false";
    s.dataset.doNotTrack = "true";
    s.onload = trackPage;
    document.head.appendChild(s);
  } catch (e) { /* analytics are optional */ }
}

function trackPage() {
  if (!window.umami) return;
  window.umami.track((p) => ({ ...p, url: "/" + location.hash.replace(/^#\/?/, ""), title: currentRoute ? currentRoute.name : "" }));
}

function trackEvent(name, data) {
  if (window.umami) window.umami.track(name, data);
}

// ---- Routing --------------------------------------------------------------

function safeDecode(s) {
  try { return decodeURIComponent(s); } catch (e) { return ""; }
}

// Area keys keep their commas readable in the address bar.
function encodeKey(key) {
  return encodeURIComponent(key).replace(/%2C/g, ",");
}

function parseRoute() {
  const [path, query] = location.hash.replace(/^#\/?/, "").split("?");
  const parts = path.split("/").filter(Boolean);
  const q = new URLSearchParams(query || "");
  const qMonth = Number(q.get("month"));
  const month = Number.isInteger(qMonth) && qMonth >= 1 && qMonth <= 12 ? qMonth : 0;
  const sort = BROWSE_MODES.includes(q.get("sort")) ? q.get("sort") : "";
  const name = parts[0] || "home";
  if (name === "area") {
    const key = parts[1] ? safeDecode(parts[1]) : "";
    const code = parts[2] === "bird" && parts[3] ? safeDecode(parts[3]) : "";
    return {
      name: parts[4] === "credits" ? "credits" : "area",
      key,
      speciesCode: SPECIES_CODE_RE.test(code) ? code : "",
      month,
      sort,
    };
  }
  if (name === "compare") {
    return {
      name,
      key: parts[1] ? safeDecode(parts[1]) : "",
      keyB: parts[2] ? safeDecode(parts[2]) : "",
      speciesCode: "",
    };
  }
  if (name === "search") {
    const from = q.get("from") || "";
    return { name, key: "", speciesCode: "", from: AREA_KEY_RE.test(from) ? from : "" };
  }
  if (name === "range") {
    const code = parts[1] ? safeDecode(parts[1]) : "";
    const m = Number(q.get("m"));
    return {
      name, key: "",
      speciesCode: SPECIES_CODE_RE.test(code) ? code : "",
      rangeMonth: Number.isInteger(m) && m >= 1 && m <= 12 ? m : 0,
    };
  }
  if (VIEW_NAMES.indexOf(name) !== -1) return { name, key: "", speciesCode: "" };
  return { name: "home", key: "", speciesCode: "" };
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
  // A link's month and sort order override the selection; a link without
  // them keeps it.
  if (route.month && route.month !== state.month) {
    state.month = route.month;
    state.monthIsCurrent = false;
    updateMonthOptions();
  }
  if (route.sort && route.sort !== currentBrowseMode()) {
    document.querySelector(`input[name="mode"][value="${route.sort}"]`).checked = true;
    updateSortLabel();
  }
  trackPage();
  for (const name of VIEW_NAMES) $("view-" + name).hidden = name !== route.name;
  updateNav(route.name === "compare" ? "home" : route.name === "about" ? "settings" : route.name);

  if (route.name === "home") await renderHome();
  else if (route.name === "search") await showSearch(route.from);
  else if (route.name === "range") await showRange(route.speciesCode, route.rangeMonth);
  else if (route.name === "area") await openArea(route.key, route.speciesCode);
  else if (route.name === "credits") await openCredits(route.key, route.speciesCode);
  else if (route.name === "compare") await openCompare(route.key, route.keyB);
  else if (route.name === "about") await renderAbout();
  else if (route.name === "settings") await renderSettings();
}

async function onHashChange() {
  const next = parseRoute();
  if (learn.active && !(next.name === "area" && next.key === learn.areaKey && next.speciesCode)) {
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
  for (const id of ["signInLink", "settingsSignIn"]) $(id).href = href;
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
    li.className = "area-item" + (homeEditing ? " editing" : "");
    const a = document.createElement("a");
    a.className = "area-link";
    a.href = "#/area/" + encodeKey(f.key);
    const title = document.createElement("span");
    title.textContent = areaTitle(f);
    const sub = document.createElement("small");
    sub.className = "area-sub";
    sub.textContent = areaSubtitle(f);
    a.append(title, sub);
    li.appendChild(a);
    if (homeEditing) {
      const rename = document.createElement("button");
      rename.className = "rename-btn";
      rename.type = "button";
      rename.setAttribute("aria-label", t("home.rename"));
      rename.textContent = "✎";
      rename.addEventListener("click", async () => {
        try {
          if (await renameFavorite(f.key)) renderHomeFavorites();
        } catch (e) {
          showSettingsHint(t("search.bookmarkError"));
        }
      });
      li.appendChild(rename);
      const remove = document.createElement("button");
      remove.className = "remove-btn";
      remove.type = "button";
      remove.setAttribute("aria-label", t("home.unbookmark"));
      remove.textContent = "−";
      remove.addEventListener("click", async () => {
        try {
          await deleteFavorite(f.key);
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
  if (!state.signedIn) {
    $("homeFirstRun").hidden = true;
    $("homeAreasPanel").hidden = true;
    return;
  }
  let profile;
  try {
    const res = await fetch("/api/me");
    if (!res.ok) throw new Error("load");
    profile = await res.json();
  } catch (e) {
    $("homeFirstRun").hidden = true;
    $("homeAreasPanel").hidden = false;
    $("homeFavorites").innerHTML = "";
    $("homeNoFavorites").hidden = true;
    return;
  }

  syncProfile(profile);
  const firstRun = state.favorites.length === 0;

  $("homeFirstRun").hidden = !firstRun;
  $("homeAreasPanel").hidden = firstRun;
  renderHomeFavorites();
}

// ---- Area labels ------------------------------------------------------------

// A place has a name; a custom circle is "Near <locality>" (the server names
// the closest town or village), or its coordinates in the open sea. Favorites
// and AreaInfo share these fields: key, name, kind, region, country, lat, lng,
// radiusKm.
function areaTitle(a) {
  const saved = state.favorites.find((f) => f.key === a.key);
  return (saved && saved.customName) || defaultTitle(a);
}

// defaultTitle is the label an area has before the user renames it.
function defaultTitle(a) {
  if (a.name && a.kind) return a.name;
  if (a.name) return t("area.near", { place: a.name });
  return `${a.lat.toFixed(3)}, ${a.lng.toFixed(3)}`;
}

// "City · Spain", "Region", "Custom area · Galicia · Spain · 5 km radius". A
// country or a region spanning several countries carries no country. With
// detail (the area page), a place without an outline also states the radius
// its species are pooled over.
function areaSubtitle(a, detail) {
  const parts = [];
  if (a.kind) parts.push(t("kind." + a.kind));
  else parts.push(t("area.custom"));
  if (a.region) parts.push(a.region);
  if (a.country) parts.push(a.country);
  if ((!a.kind || (detail && !a.polygon)) && a.radiusKm) parts.push(t("area.radiusKm", { km: Number(a.radiusKm) }));
  return parts.join(" · ");
}

function customKey(lat, lng, km) {
  return `c${lat.toFixed(3)},${lng.toFixed(3)},${km.toFixed(1)}`;
}

// ---- Search ---------------------------------------------------------------

// The search view offers two ways in: typing a place name (a prefix search
// against places.bolt, server/places_data.go) or choosing any centre on the
// map and a radius, which opens a custom circle.
let map = null;
let centreMarker = null;
let radiusCircle = null;
let customCentre = null;

// Picking a place stages it on the map before anything is browsed. A place
// with a polygon is shown by its outline and cannot be edited; any other
// (a town, a village, a custom circle) gets a draggable centre and the radius
// slider, and an edit turns it into a custom circle. Save and "Show birds"
// both act on stageTarget().
let outlineLayer = null;
let stage = null; // {area, polygon, moved, resized}

async function showSearch(from) {
  if (!map) return;
  map.invalidateSize();
  setTimeout(() => map.invalidateSize(), 0);
  if (!from) { leaveStage(); return; }
  let a = state.area && state.area.key === from ? state.area : null;
  if (!a) {
    try {
      const res = await fetch(`/api/places/${encodeKey(from)}`);
      if (!res.ok) { leaveStage(); return; }
      a = await res.json();
    } catch (e) { leaveStage(); return; }
  }
  let rings = null;
  if (a.polygon) {
    try {
      const res = await fetch(`/api/places/${encodeKey(from)}/outline`);
      if (res.ok) rings = (await res.json()).rings;
    } catch (e) { /* falls back to the editable circle */ }
  }
  if (!currentRoute || currentRoute.name !== "search" || currentRoute.from !== from) return;
  enterStage(a, rings);
}

function clearOutline() {
  if (outlineLayer) { outlineLayer.remove(); outlineLayer = null; }
}

function enterStage(a, rings) {
  clearOutline();
  stage = { area: a, polygon: !!rings, moved: false, resized: false };
  if (rings) {
    if (centreMarker) { centreMarker.remove(); centreMarker = null; }
    if (radiusCircle) { radiusCircle.remove(); radiusCircle = null; }
    customCentre = null;
    outlineLayer = L.polygon(rings, { color: "#007aff", weight: 2, fillOpacity: 0.15, interactive: false }).addTo(map);
    map.fitBounds(outlineLayer.getBounds(), { padding: [16, 16] });
  } else {
    $("customRadius").value = kmToSlider(Math.min(MAX_RADIUS_KM, Math.max(MIN_RADIUS_KM, a.radiusKm || DEFAULT_RADIUS_KM)));
    showCustomRadius();
    setCustomCentre(a.lat, a.lng, true);
  }
  renderStage();
}

// useCircle drops a polygon place's outline as the pooled area: the outline
// stays as a faint reference and the place's centre and radius become an
// editable circle.
function useCircle() {
  if (!stage || !stage.polygon) return;
  const a = stage.area;
  stage.polygon = false;
  stage.resized = true;
  if (outlineLayer) outlineLayer.setStyle({ color: "#8e8e93", weight: 2, dashArray: "6 4", fillOpacity: 0.05 });
  $("customRadius").value = kmToSlider(Math.min(MAX_RADIUS_KM, Math.max(MIN_RADIUS_KM, a.radiusKm || DEFAULT_RADIUS_KM)));
  showCustomRadius();
  setCustomCentre(a.lat, a.lng, false);
}

function leaveStage() {
  clearOutline();
  stage = null;
  renderStage();
}

// stageTarget is the area key Save and "Show birds" act on: the staged place
// itself until it is moved or resized, then the custom circle on the map.
function stageTarget() {
  if (stage && stage.polygon) return stage.area.key;
  if (!customCentre) return null;
  if (stage && !stage.moved && !stage.resized) return stage.area.key;
  const lng = ((customCentre.lng + 540) % 360) - 180;
  return customKey(customCentre.lat, lng, customRadius());
}

function renderStage() {
  const head = $("stageHead");
  head.hidden = !stage || stage.moved;
  if (!head.hidden) {
    $("stageName").textContent = areaTitle(stage.area);
    const shown = stage.resized ? { ...stage.area, radiusKm: customRadius() } : stage.area;
    $("stageSub").textContent = areaSubtitle(shown, true);
  }
  const fixed = !!stage && stage.polygon;
  $("customRadiusLabel").hidden = fixed;
  $("useCircle").hidden = !fixed;
  $("mapHint").hidden = fixed;
  $("polygonHint").hidden = !fixed;
  const key = stageTarget();
  $("customGo").disabled = !key;
  const star = $("stageSave");
  const saved = !!key && state.favorites.some((f) => f.key === key);
  star.disabled = !key;
  star.setAttribute("aria-pressed", saved ? "true" : "false");
  star.setAttribute("aria-label", saved ? t("area.bookmarked") : t(state.signedIn ? "area.bookmark" : "area.bookmarkSignIn"));
}

function initMap() {
  map = L.map("map").setView([40, 0], 3);
  L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: t("search.mapAttribution"),
  }).addTo(map);
  map.on("click", (e) => {
    if (stage && stage.polygon) return;
    if (stage) stage.moved = true;
    setCustomCentre(e.latlng.lat, e.latlng.lng, false);
  });
}

// The radius slider is logarithmic (1 km to 500 km) and snaps to 0.1 km
// below 10 km, 1 km below 100 km and 5 km above, so the readout stays tidy.
const RADIUS_SLIDER_MAX = 1000;

function sliderToKm(v) {
  const raw = MIN_RADIUS_KM * Math.pow(MAX_RADIUS_KM / MIN_RADIUS_KM, v / RADIUS_SLIDER_MAX);
  const step = raw < 10 ? 0.1 : raw < 100 ? 1 : 5;
  const km = Math.round(raw / step) * step;
  return Math.min(MAX_RADIUS_KM, Math.max(MIN_RADIUS_KM, Number(km.toFixed(1))));
}

function kmToSlider(km) {
  return Math.round((Math.log(km / MIN_RADIUS_KM) / Math.log(MAX_RADIUS_KM / MIN_RADIUS_KM)) * RADIUS_SLIDER_MAX);
}

function customRadius() {
  const v = parseFloat($("customRadius").value);
  return Number.isNaN(v) ? DEFAULT_RADIUS_KM : sliderToKm(v);
}

function showCustomRadius() {
  $("customRadiusValue").textContent = `${customRadius()} km`;
}

function drawCustomArea(fit) {
  if (!customCentre) return;
  const ll = [customCentre.lat, customCentre.lng];
  if (!centreMarker) centreMarker = L.marker(ll).addTo(map);
  else centreMarker.setLatLng(ll);
  if (!radiusCircle) radiusCircle = L.circle(ll, { radius: customRadius() * 1000, color: "#007aff", weight: 2, fillOpacity: 0.15 }).addTo(map);
  else radiusCircle.setLatLng(ll).setRadius(customRadius() * 1000);
  if (fit) map.fitBounds(radiusCircle.getBounds(), { maxZoom: 14 });
  renderStage();
}

function setCustomCentre(lat, lng, fit) {
  customCentre = { lat, lng };
  drawCustomArea(fit);
}

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
  if ($("placeQuery").value.trim() !== q) return;
  renderPlaceSuggestions(results || []);
}

function renderPlaceSuggestions(results) {
  placeResults = results;
  const list = $("placeSuggestions");
  if (results.length === 0) {
    list.innerHTML = `<li class="place-suggestion-empty">${escapeHtml(t("search.placeNoResults"))}</li>`;
    list.hidden = false;
    return;
  }
  list.innerHTML = results.map((p, i) => `
    <li class="place-suggestion" data-index="${i}">
      <span>${escapeHtml(p.name)}</span>
      <span class="place-suggestion-country">${escapeHtml([t("kind." + p.kind), p.country].filter(Boolean).join(" · "))}</span>
    </li>`).join("");
  list.hidden = false;
}

function hidePlaceSuggestions() {
  const list = $("placeSuggestions");
  list.hidden = true;
  list.innerHTML = "";
}

function selectPlace(p) {
  $("placeQuery").value = "";
  hidePlaceSuggestions();
  navigate("#/search?from=p" + p.id);
}

// ---- Species range map ----------------------------------------------------

// The Atlas tab draws one species' frequency over the world, one translucent
// rectangle per 1-degree cell; a darker cell is a more frequent species there.
let rangeMap = null;
let rangeLayer = null;
let rangeRequest = 0;
let rangeMonth = 0;
let rangeFitted = "";
let speciesSearchTimer = null;
let speciesResults = [];
const RANGE_COLOR = "#d7263d";

function initRangeMap() {
  if (rangeMap) return;
  rangeMap = L.map("rangeMap", { worldCopyJump: true, minZoom: 1 }).setView([25, 10], 2);
  L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: t("search.mapAttribution"),
  }).addTo(rangeMap);
  rangeLayer = L.layerGroup().addTo(rangeMap);
}

function updateRangeMonthOptions() {
  const sel = $("rangeMonth");
  if (!sel) return;
  sel.innerHTML = "";
  const add = (value, text) => {
    const opt = document.createElement("option");
    opt.value = value;
    opt.textContent = text;
    sel.appendChild(opt);
  };
  add("0", t("browse.allYear"));
  for (let m = 1; m <= 12; m++) add(String(m), monthName(m));
  sel.value = String(rangeMonth);
}

function rangeHash(code, month) {
  return "#/range" + (code ? "/" + encodeURIComponent(code) : "") + (code && month ? "?m=" + month : "");
}

function clearRange() {
  rangeRequest++;
  rangeFitted = "";
  if (rangeLayer) rangeLayer.clearLayers();
  $("rangeHead").hidden = true;
  $("rangeLegend").hidden = true;
  $("rangeHint").hidden = false;
  $("rangeMonth").disabled = true;
  $("rangeStatus").textContent = "";
}

async function showRange(code, month) {
  initRangeMap();
  rangeMap.invalidateSize();
  setTimeout(() => rangeMap.invalidateSize(), 0);
  rangeMonth = month;
  updateRangeMonthOptions();
  if (!code) { clearRange(); return; }
  const req = ++rangeRequest;
  $("rangeHint").hidden = true;
  $("rangeMonth").disabled = false;
  $("rangeStatus").textContent = t("range.loading");
  let data;
  try {
    const res = await fetch(`/api/species/${encodeURIComponent(code)}/range?lang=${state.language}${month ? "&month=" + month : ""}`);
    if (!res.ok) throw new Error(String(res.status));
    data = await res.json();
  } catch (e) {
    if (req === rangeRequest) $("rangeStatus").textContent = t("range.error");
    return;
  }
  if (req !== rangeRequest) return;
  $("rangeName").textContent = data.comName;
  $("rangeSci").textContent = data.sciName;
  $("rangeHead").hidden = false;
  rangeLayer.clearLayers();
  const renderer = L.canvas({ padding: 0.5 });
  for (const [lat, lng, pct] of data.cells) {
    L.rectangle([[lat, lng], [lat + 1, lng + 1]], {
      renderer, stroke: false, interactive: false,
      fillColor: RANGE_COLOR, fillOpacity: 0.15 + 0.75 * (pct / data.max),
    }).addTo(rangeLayer);
  }
  $("rangeLegend").hidden = data.cells.length === 0;
  if (data.cells.length > 0 && rangeFitted !== code) {
    rangeFitted = code;
    fitRange(data.cells);
  }
  $("rangeStatus").textContent = data.cells.length === 0 ? t("range.noRecords") : "";
}

// fitRange frames the bulk of a species' range. The 2nd to 98th percentile of
// cell latitudes and longitudes ignores stray vagrant records, and a range
// that still spans most of the globe (or wraps the antimeridian) keeps the
// world view.
function fitRange(cells) {
  const pick = (idx, q) => {
    const v = cells.map((c) => c[idx]).sort((a, b) => a - b);
    return v[Math.min(v.length - 1, Math.floor(q * v.length))];
  };
  const south = pick(0, 0.02), north = pick(0, 0.98) + 1;
  const west = pick(1, 0.02), east = pick(1, 0.98) + 1;
  if (east - west > 240) { rangeMap.setView([25, 10], 2); return; }
  rangeMap.fitBounds([[south, west], [north, east]], { padding: [20, 20], maxZoom: 6 });
}

function wireSpeciesSearch() {
  const input = $("speciesQuery");
  const list = $("speciesSuggestions");
  const hide = () => { list.hidden = true; list.innerHTML = ""; };
  const pick = (sp) => {
    input.value = "";
    hide();
    navigate(rangeHash(sp.speciesCode, rangeMonth));
  };
  input.addEventListener("input", () => {
    const q = input.value.trim();
    clearTimeout(speciesSearchTimer);
    if (q.length < 2) { hide(); return; }
    speciesSearchTimer = setTimeout(async () => {
      let results;
      try {
        const res = await fetch(`/api/species?q=${encodeURIComponent(q)}&lang=${state.language}`);
        if (!res.ok) { hide(); return; }
        results = await res.json();
      } catch (e) { hide(); return; }
      if (input.value.trim() !== q) return;
      speciesResults = results || [];
      list.innerHTML = speciesResults.length === 0
        ? `<li class="place-suggestion-empty">${escapeHtml(t("range.noMatches"))}</li>`
        : speciesResults.map((sp, i) => `
          <li class="place-suggestion" data-index="${i}">
            <span>${escapeHtml(sp.comName)}</span>
            <span class="place-suggestion-country">${escapeHtml(sp.sciName)}</span>
          </li>`).join("");
      list.hidden = false;
    }, 250);
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Escape") hide();
    if (e.key === "Enter" && speciesResults.length > 0 && !list.hidden) {
      e.preventDefault();
      pick(speciesResults[0]);
    }
  });
  list.addEventListener("click", (e) => {
    const li = e.target.closest(".place-suggestion");
    const sp = li && speciesResults[Number(li.dataset.index)];
    if (sp) pick(sp);
  });
  document.addEventListener("click", (e) => {
    if (!e.target.closest("#view-range .place-search")) hide();
  });
  $("rangeMonth").addEventListener("change", (e) => {
    const code = parseRoute().speciesCode;
    if (code) navigate(rangeHash(code, Number(e.target.value)));
  });
}

// ---- Favorites ------------------------------------------------------------

// putFavorite saves an area; the server describes it from its own data.
async function putFavorite(area) {
  const res = await fetch(`/api/me/favorites/${encodeKey(area.key)}`, { method: "PUT" });
  if (!res.ok) throw new Error(String(res.status));
  if (!state.favorites.some((f) => f.key === area.key)) {
    state.favorites.push({
      key: area.key, name: area.name || "",
      kind: area.kind || "", region: area.region || "", country: area.country || "", lat: area.lat, lng: area.lng, radiusKm: area.radiusKm,
    });
  }
}

// renameFavorite asks for a new label for a saved area and stores it; an empty
// answer restores the place's own name. Returns whether anything changed.
async function renameFavorite(key) {
  const fav = state.favorites.find((f) => f.key === key);
  if (!fav) return false;
  const answer = window.prompt(t("area.renamePrompt"), areaTitle(fav));
  if (answer === null) return false;
  const name = answer.trim().replace(/\s+/g, " ");
  await putFavoriteName(key, name === defaultTitle(fav) ? "" : name);
  return true;
}

async function putFavoriteName(key, name) {
  const res = await fetch(`/api/me/favorites/${encodeKey(key)}/name`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  });
  if (!res.ok) throw new Error(String(res.status));
  const fav = state.favorites.find((f) => f.key === key);
  if (fav) fav.customName = name;
}

async function deleteFavorite(key) {
  const res = await fetch(`/api/me/favorites/${encodeKey(key)}`, { method: "DELETE" });
  if (!res.ok) throw new Error(String(res.status));
  state.favorites = state.favorites.filter((f) => f.key !== key);
}

// ---- Area + species browsing ------------------------------------------

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

// The compare link only appears when this area has a saved one to be
// compared with.
function updateCompareLink() {
  if (!state.area) return;
  const link = $("areaCompareLink");
  link.hidden = !state.favorites.some((f) => f.key !== state.area.key);
  link.href = compareHash(state.area.key, "");
}

function updateBookmarkButton() {
  if (!state.area) return;
  updateCompareLink();
  const saved = state.favorites.some((f) => f.key === state.area.key);
  const btn = $("bookmarkBtn");
  // Icon-only: filled star when saved (via [aria-pressed], see style.css),
  // outline otherwise. aria-label carries the same info textContent used to.
  btn.setAttribute("aria-pressed", saved ? "true" : "false");
  $("renameAreaBtn").hidden = !saved;
  btn.setAttribute("aria-label", saved ? t("area.bookmarked") : t(state.signedIn ? "area.bookmark" : "area.bookmarkSignIn"));
}

function renderAreaHead() {
  $("areaName").textContent = areaTitle(state.area);
  $("areaSub").textContent = areaSubtitle(state.area, true);
  $("areaMapLink").href = "#/search?from=" + encodeKey(state.area.key);
}

// ensureArea makes state.area describe key, fetching its details unless it's
// already the current area. Returns false (with the area status line set) if
// the lookup fails.
async function ensureArea(key) {
  if (state.area && state.area.key === key) return true;
  state.area = null;
  state.species = [];
  state.secondaryNames = {};
  $("areaName").textContent = "";
  $("areaSub").textContent = "";
  setAreaStatus(t("area.loading"));
  try {
    const res = await fetch(`/api/places/${encodeKey(key)}`);
    if (!res.ok) { setAreaStatus(t("area.error")); return false; }
    state.area = await res.json();
  } catch (e) {
    setAreaStatus(t("area.error"));
    return false;
  }
  return true;
}

// The selected month and sort order ride in the hash so shared area and
// Learn links open on the same view. The default order is left out.
function routeQuery() {
  const q = [];
  if (effectiveMonth()) q.push("month=" + effectiveMonth());
  if (currentBrowseMode() !== DEFAULT_BROWSE_MODE) q.push("sort=" + currentBrowseMode());
  return q.length ? "?" + q.join("&") : "";
}

function areaHash(key) { return "#/area/" + encodeKey(key) + routeQuery(); }

function birdHash(key, speciesCode) {
  return "#/area/" + encodeKey(key) + "/bird/" + encodeURIComponent(speciesCode) + routeQuery();
}

function creditsHash(key, speciesCode) {
  return "#/area/" + encodeKey(key) + "/bird/" + encodeURIComponent(speciesCode) + "/credits" + routeQuery();
}

// A shared bird link (#/area/<key>/bird/<code>) opens the area with Learn
// already on that bird; the species list loads behind it in parallel.
async function openArea(key, speciesCode) {
  if (!AREA_KEY_RE.test(key)) { navigate("#/home"); return; }
  if (!(await ensureArea(key))) return;

  renderAreaHead();
  updateBookmarkButton();
  const browse = loadSpecies();
  if (speciesCode) await loadAndStartLearn(speciesCode);
  await browse;
}

// ---- About --------------------------------------------------------------

let contactEnabled;

async function renderAbout() {
  if (contactEnabled === undefined) {
    try {
      const res = await fetch("/api/contact");
      contactEnabled = res.ok && Boolean((await res.json()).enabled);
    } catch (e) {
      contactEnabled = false;
    }
  }
  $("aboutContact").hidden = !contactEnabled;
  if (!contactEnabled) return;
  $("aboutContactSignIn").hidden = state.signedIn;
  $("aboutContactForm").hidden = !state.signedIn;
  $("contactStatus").textContent = "";
  if (!state.signedIn) {
    $("aboutContactLogin").href = "/login?next=" + encodeURIComponent("/" + location.hash);
  }
}

async function onContactSubmit(e) {
  e.preventDefault();
  const box = $("contactMessage");
  const btn = $("contactSend");
  const status = $("contactStatus");
  const message = box.value.trim();
  if (!message) return;
  btn.disabled = true;
  status.textContent = t("about.contactSending");
  try {
    const res = await fetch("/api/contact", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message }),
    });
    if (res.ok) {
      box.value = "";
      status.textContent = t("about.contactSent");
    } else {
      status.textContent = t(res.status === 429 ? "about.contactRateLimited" : "about.contactError");
    }
  } catch (err) {
    status.textContent = t("about.contactError");
  } finally {
    btn.disabled = false;
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

async function openCredits(key, speciesCode) {
  if (!AREA_KEY_RE.test(key) || !speciesCode) { navigate("#/home"); return; }
  const stale = () => currentRoute.name !== "credits" || currentRoute.key !== key || currentRoute.speciesCode !== speciesCode;
  window.scrollTo(0, 0);
  $("creditsBack").href = birdHash(key, speciesCode);
  $("creditsList").innerHTML = "";
  $("creditsStatus").textContent = t("credits.loading");

  const loaded = await ensureArea(key);
  if (stale()) return;
  if (!loaded) { $("creditsStatus").textContent = t("area.error"); return; }

  let species;
  try {
    const res = await fetch(`/api/places/${encodeKey(key)}/credits?species=${encodeURIComponent(speciesCode)}&lang=${encodeURIComponent(state.language)}`);
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

async function loadSecondaryNames(key) {
  state.secondaryNames = {};
  if (!state.secondaryLanguage || state.secondaryLanguage === state.language) return;
  try {
    const res = await fetch(`/api/places/${encodeKey(key)}/species?lang=${state.secondaryLanguage}&mode=category`);
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
  card.href = birdHash(state.area.key, sp.speciesCode);
  card.dataset.code = sp.speciesCode;
  card.innerHTML = `
    <img src="${sp.imageMissing ? MISSING_URL : PLACEHOLDER_URL}" alt="${escapeHtml(sp.comName)}">
    <div class="card-body">
      <div class="com">${escapeHtml(sp.comName)}</div>
      ${secondaryNameFor(sp.speciesCode) ? `<div class="sub">${escapeHtml(secondaryNameFor(sp.speciesCode))}</div>` : ""}
      <div class="sci">${escapeHtml(sp.sciName)}</div>
      ${sp.seasonBars ? seasonChart(sp.seasonBars, sp.comName) : ""}
    </div>
  `;
  // imageMissing means the server already confirmed no photo exists — no
  // point polling a URL that will never 200.
  if (sp.imageUrl && !sp.imageMissing) preloadAndSwap(card.querySelector("img"), sp.imageUrl);
  return card;
}

async function loadSpecies() {
  const area = state.area;
  if (!area) return;
  const name = areaTitle(area);
  const lang = state.language;
  const mode = currentBrowseMode();
  const groups = $("groups");
  groups.innerHTML = "";
  $("monthSelect").hidden = mode === "seasonality";

  // Fetch the secondary-language names first (cached after the first load) so
  // the cards below can render their subtitles immediately.
  await loadSecondaryNames(area.key);

  let species;
  if (mode === "popularity" || mode === "alphabetical") {
    setAreaStatus(mode === "popularity"
      ? t("browse.loadingPopularity", { name })
      : t("browse.loading", { name }));
    try {
      const res = await fetch(`/api/places/${encodeKey(area.key)}/species?lang=${lang}&mode=${mode}${monthParam()}`);
      if (!res.ok) { setAreaStatus(t("browse.error", { status: res.status })); return; }
      species = await res.json();
    } catch (e) {
      setAreaStatus(t("browse.error", { status: "?" }));
      return;
    }
    state.species = species;
    const grid = document.createElement("div");
    grid.className = "family-grid";
    groups.appendChild(grid);
    for (const sp of species) grid.appendChild(speciesCard(sp));
    const key = mode === "popularity" ? "browse.loadedPopularity" : "browse.loadedAlphabetical";
    setAreaStatus(species.length === 0 && state.month ? noneInMonthText(name) : tn(key, species.length, { name }));
    return;
  }

  if (mode === "seasonality") {
    setAreaStatus(t("browse.loading", { name }));
    try {
      const res = await fetch(`/api/places/${encodeKey(area.key)}/species?lang=${lang}&mode=seasonality`);
      if (!res.ok) { setAreaStatus(t("browse.error", { status: res.status })); return; }
      species = await res.json();
    } catch (e) {
      setAreaStatus(t("browse.error", { status: "?" }));
      return;
    }
    state.species = species;
    // Species arrive grouped (year-round, seasonal, occasional); a heading
    // goes up whenever the group changes. An area with too little data
    // comes back without groups, as one plain list.
    let current = null;
    let grid = null;
    for (const sp of species) {
      if (!grid || sp.season !== current) {
        current = sp.season;
        if (current) {
          const heading = document.createElement("h3");
          heading.className = "family-heading";
          heading.textContent = t("browse.season." + current);
          groups.appendChild(heading);
        }
        grid = document.createElement("div");
        grid.className = "family-grid";
        groups.appendChild(grid);
      }
      grid.appendChild(speciesCard(sp));
    }
    setAreaStatus(tn("browse.loadedSeasonality", species.length, { name }));
    return;
  }

  setAreaStatus(t("browse.loadingCategory", { name }));
  try {
    const res = await fetch(`/api/places/${encodeKey(area.key)}/species?lang=${lang}&mode=category${monthParam()}`);
    if (!res.ok) { setAreaStatus(t("browse.error", { status: res.status })); return; }
    species = await res.json();
  } catch (e) {
    setAreaStatus(t("browse.error", { status: "?" }));
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
  setAreaStatus(species.length === 0 && state.month ? noneInMonthText(name) : tn("browse.loadedCategory", species.length, { name }));
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
  else if (currentRoute.name === "area") await loadSpecies();
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
  if (currentRoute.name === "area") await loadSpecies();
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
  const place = item.areaName || (state.area ? areaTitle(state.area) : "");
  return shareLink(item.comName, t("learn.shareText", { name: item.comName, place }), location.origin + location.pathname + birdHash(item.areaKey || learn.areaKey, item.speciesCode));
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

// learn drives the full-screen card deck: cards is the area's species in
// popularity order (see startLearn), and index just moves through it — no
// scoring, no server round-trip per card. namesVisible is a per-session
// display toggle, not account data, so it isn't persisted anywhere.
const learn = {
  active: false,
  areaKey: "",
  cards: [],
  index: 0,
  done: false,
  namesVisible: true,
  keyHandler: null,

  // quiet decks (started from Compare) leave the address bar alone: their
  // cards come from two areas, so no single bird URL describes them.
  quiet: false,

  start(cards, areaKey, index = 0, opts = {}) {
    this.quiet = !!opts.quiet;
    this.cards = cards.slice();
    this.areaKey = areaKey;
    this.index = index;
    this.done = false;
    this.active = true;
    $("learnOverlay").hidden = false;
    this.keyHandler = (e) => this.onKeyDown(e);
    document.addEventListener("keydown", this.keyHandler);
    this.render();
  },

  finish() {
    this.active = false;
    $("learnOverlay").hidden = true;
    // Closing a shared-bird view drops the bird from the URL so a reload (or
    // re-sharing the page) lands on the plain area. replaceState doesn't
    // fire hashchange; the check skips the case where finish() runs because
    // the user already navigated somewhere else.
    const route = parseRoute();
    if (!this.quiet && route.name === "area" && route.key === this.areaKey && route.speciesCode) {
      history.replaceState(null, "", areaHash(this.areaKey));
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
  // area on the final screen) so it can always be copied. replaceState, so
  // swiping through birds adds no history entries and fires no hashchange.
  syncURL() {
    if (this.quiet) return;
    const code = this.done ? "" : this.cards[this.index].speciesCode;
    const hash = code ? birdHash(this.areaKey, code) : areaHash(this.areaKey);
    if (location.hash !== hash) history.replaceState(null, "", hash);
    currentRoute = Object.assign({}, currentRoute, { speciesCode: code });
  },

  render() {
    this.syncURL();
    const body = $("learnBody");
    body.innerHTML = "";

    $("learnCreditsLink").hidden = this.done;
    $("learnRangeLink").hidden = this.done;
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
    $("learnRangeLink").href = rangeHash(item.speciesCode, effectiveMonth());
    $("learnCreditsLink").href = creditsHash(item.areaKey || this.areaKey, item.speciesCode);
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
  if (!state.area) return;
  trackEvent("learn-start");
  const btn = $("learnStart");
  btn.disabled = true;
  try {
    await loadAndStartLearn();
  } finally {
    btn.disabled = false;
  }
}

async function loadAndStartLearn(speciesCode) {
  setAreaStatus(t("learn.building"));
  let species;
  try {
    const res = await fetch(`/api/places/${encodeKey(state.area.key)}/species?lang=${encodeURIComponent(state.language)}&mode=popularity${monthParam()}`);
    if (!res.ok) { setAreaStatus(t("learn.error", { status: res.status })); return; }
    species = await res.json();
  } catch (e) {
    setAreaStatus(t("learn.error", { status: "?" }));
    return;
  }
  if (!species || species.length === 0) {
    setAreaStatus(effectiveMonth() ? noneInMonthText(areaTitle(state.area)) : t("learn.none"));
    return;
  }
  let index = 0;
  if (speciesCode) {
    index = species.findIndex((sp) => sp.speciesCode === speciesCode);
    if (index < 0) {
      setAreaStatus(t("learn.birdNotFound"));
      history.replaceState(null, "", areaHash(state.area.key));
      currentRoute = Object.assign({}, currentRoute, { speciesCode: "" });
      return;
    }
  }
  await loadSecondaryNames(state.area.key);
  setAreaStatus("");
  learn.start(species, state.area.key, index);
}

// ---- Wiring ---------------------------------------------------------------

function wireEvents() {
  wirePlaceSearch();
  wireSpeciesSearch();
  wireSortMenu();
  $("homeEditToggle").addEventListener("click", () => {
    homeEditing = !homeEditing;
    renderHomeFavorites();
  });
  $("useLocation").addEventListener("click", () => {
    if (!navigator.geolocation) { setStatus(t("search.geoUnsupported")); return; }
    setStatus(t("search.geoRequesting"));
    navigator.geolocation.getCurrentPosition(
      (pos) => {
        setStatus("");
        leaveStage();
        setCustomCentre(pos.coords.latitude, pos.coords.longitude, true);
      },
      (err) => {
        setStatus(t("search.geoError", { message: err.message }));
      },
      { enableHighAccuracy: true, timeout: 10000 }
    );
  });

  $("customRadius").value = kmToSlider(DEFAULT_RADIUS_KM);
  showCustomRadius();
  $("customRadius").addEventListener("input", () => {
    if (stage) stage.resized = true;
    showCustomRadius();
    drawCustomArea(false);
  });
  $("customRadius").addEventListener("change", () => drawCustomArea(true));
  $("customForm").addEventListener("submit", (e) => {
    e.preventDefault();
    const key = stageTarget();
    if (key) navigate("#/area/" + encodeKey(key));
  });

  $("useCircle").addEventListener("click", useCircle);

  $("stageSave").addEventListener("click", async () => {
    const key = stageTarget();
    if (!key) return;
    if (!state.signedIn) {
      window.location = "/login?next=" + encodeURIComponent("/" + location.hash);
      return;
    }
    const btn = $("stageSave");
    btn.disabled = true;
    try {
      if (state.favorites.some((f) => f.key === key)) {
        await deleteFavorite(key);
      } else {
        const res = await fetch(`/api/places/${encodeKey(key)}`);
        if (!res.ok) throw new Error(String(res.status));
        await putFavorite(await res.json());
        trackEvent("bookmark");
        // A resized town keeps its name rather than becoming "Near <town>".
        if (stage && stage.area.kind && stage.area.name && key !== stage.area.key && !stage.moved) {
          await putFavoriteName(key, stage.area.name);
        }
      }
    } catch (e) {
      setStatus(t("search.bookmarkError"));
    } finally {
      renderStage();
    }
  });

  $("shareAreaBtn").addEventListener("click", async () => {
    if (!state.area) return;
    const name = areaTitle(state.area);
    const copied = await shareLink(name, t("area.shareText", { place: name }), location.origin + location.pathname + areaHash(state.area.key));
    if (copied) setAreaStatus(t("share.linkCopied"));
    trackEvent("share-area");
  });

  $("renameAreaBtn").addEventListener("click", async () => {
    if (!state.area) return;
    try {
      if (await renameFavorite(state.area.key)) renderAreaHead();
    } catch (e) {
      setAreaStatus(t("area.bookmarkError"));
    }
  });

  $("bookmarkBtn").addEventListener("click", async () => {
    if (!state.area) return;
    if (!state.signedIn) {
      window.location = "/login?next=" + encodeURIComponent("/" + location.hash);
      return;
    }
    const saved = state.favorites.some((f) => f.key === state.area.key);
    const btn = $("bookmarkBtn");
    btn.disabled = true;
    try {
      if (saved) await deleteFavorite(state.area.key);
      else { await putFavorite(state.area); trackEvent("bookmark"); }
      updateBookmarkButton();
    } catch (e) {
      setAreaStatus(t("area.bookmarkError"));
    } finally {
      btn.disabled = false;
    }
  });

  for (const radio of document.querySelectorAll('input[name="mode"]')) {
    radio.addEventListener("change", () => {
      if (parseRoute().name === "area") history.replaceState(null, "", location.hash.split("?")[0] + routeQuery());
      if (state.area) loadSpecies();
    });
  }

  $("monthSelect").addEventListener("change", (e) => {
    state.monthIsCurrent = e.target.value === "now";
    state.month = state.monthIsCurrent ? currentMonth() : Number(e.target.value);
    if (parseRoute().name === "area") history.replaceState(null, "", location.hash.split("?")[0] + routeQuery());
    if (state.area) loadSpecies();
  });

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
  $("aboutContactForm").addEventListener("submit", onContactSubmit);
}

// ---- Boot -----------------------------------------------------------------

async function init() {
  I18N = await loadTranslations();
  await initAccount();
  initAnalytics();

  // ?lang=… overrides the account preference for this load and ?mode=… picks
  // the browse ordering.
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

  if (!location.hash) history.replaceState(null, "", "#/home");

  window.addEventListener("hashchange", () => { updateAccountChrome(); onHashChange(); });
  await renderRoute();
}

init();
