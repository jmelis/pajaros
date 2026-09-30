"use strict";

// Compare view: two hotspots side by side (#/compare/<a>/<b>). The server
// (GET /api/compare) sends the union of both species lists with counts,
// ranks and shares; everything shown here is derived from that. Loaded before
// app.js and relies on its globals ($, t, tn, state, learn, ...) only at call
// time.

const CMP_LOW_SAMPLE = 45; // combined records below which a ratio is only a hint
const CMP_RANK_SHIFT = 5; // rank move that earns a ▲/▼ chip
const CMP_REGULAR = 10; // records that make a one-hotspot species "regular"
const CMP_DIFF_EACH = 6; // differences shown per side
const CMP_RANK_ROWS = 12;
const CMP_ONLY_ROWS = 8;
const CMP_FAMILY_ROWS = 7;
const CMP_DOMAIN_LOG2 = 2; // difference bars span equal .. ×4
const CMP_NEARBY_KM = 10;
const CMP_NEARBY_MAX = 15;

const cmp = { data: null };

function compareHash(a, b) {
  return "#/compare" + (a ? "/" + encodeURIComponent(a) : "") + (b ? "/" + encodeURIComponent(b) : "");
}

function cmpPct(x) { return (x * 100).toFixed(1); }
function cmpNum(n) { return Number(n).toLocaleString(); }

function cmpOptions(candidates, extra, selected, placeholder) {
  const sel = document.createElement("select");
  if (placeholder) {
    const o = document.createElement("option");
    o.value = "";
    o.textContent = placeholder;
    o.disabled = true;
    o.selected = !selected;
    sel.appendChild(o);
  }
  const seen = new Set();
  const groups = { saved: [], nearby: [] };
  for (const c of extra.concat(candidates)) {
    if (seen.has(c.locId)) continue;
    seen.add(c.locId);
    groups[c.group].push(c);
  }
  const add = (parent, c) => {
    const o = document.createElement("option");
    o.value = c.locId;
    o.textContent = c.name || c.locId;
    o.selected = c.locId === selected;
    parent.appendChild(o);
  };
  for (const key of ["saved", "nearby"]) {
    if (groups[key].length === 0) continue;
    const g = document.createElement("optgroup");
    g.label = t("compare." + key);
    for (const c of groups[key]) add(g, c);
    sel.appendChild(g);
  }
  return sel;
}

function cmpSpot(cls, labelKey, sel, meta) {
  const box = document.createElement("div");
  box.className = "cmp-spot " + cls;
  const label = document.createElement("label");
  label.className = "cmp-meta";
  const sw = document.createElement("i");
  sw.className = "cmp-sw " + cls;
  label.append(sw, t(labelKey), " ");
  label.appendChild(sel);
  box.appendChild(label);
  if (meta) {
    const m = document.createElement("span");
    m.className = "cmp-meta cmp-num";
    m.textContent = meta;
    box.appendChild(m);
  }
  return box;
}

// renderComparePicker draws the two hotspot selectors. Either side may be
// null while the user is still choosing the second hotspot.
function renderComparePicker(a, b, candidates) {
  const picker = $("comparePicker");
  picker.innerHTML = "";
  const extra = [a, b].filter(Boolean).map((h) => ({ locId: h.locId, name: h.name, group: "saved" }));
  // The two in play are listed first under "Saved" only if actually saved.
  const savedIds = new Set(state.favorites.map((f) => f.locId));
  for (const e of extra) e.group = savedIds.has(e.locId) ? "saved" : "nearby";

  const selA = cmpOptions(candidates, extra, a && a.locId, a ? "" : t("compare.choose"));
  const selB = cmpOptions(candidates, extra, b && b.locId, b ? "" : t("compare.choose"));
  selA.setAttribute("aria-label", t("compare.hotspotA"));
  selB.setAttribute("aria-label", t("compare.hotspotB"));
  const meta = (h) => (h && h.species != null
    ? t("compare.meta", { species: cmpNum(h.species), obs: cmpNum(h.observations) })
    : "");

  const go = (na, nb) => navigate(compareHash(na, nb));
  selA.addEventListener("change", () => {
    const v = selA.value;
    go(v, b && b.locId === v ? a.locId : (b ? b.locId : ""));
  });
  selB.addEventListener("change", () => {
    const v = selB.value;
    go(a && a.locId === v ? b && b.locId : (a ? a.locId : ""), v);
  });

  const swap = document.createElement("button");
  swap.type = "button";
  swap.className = "cmp-swap";
  swap.textContent = "⇄";
  swap.setAttribute("aria-label", t("compare.swap"));
  swap.disabled = !(a && b);
  swap.addEventListener("click", () => go(b.locId, a.locId));

  picker.append(cmpSpot("a", "compare.hotspotA", selA, meta(a)), swap, cmpSpot("b", "compare.hotspotB", selB, meta(b)));
}

async function cmpFetchHotspot(locId) {
  try {
    const res = await fetch(`/api/hotspots/${encodeURIComponent(locId)}`);
    if (!res.ok) return null;
    const h = await res.json();
    return { locId, name: h.locName || locId, lat: h.lat, lng: h.lng };
  } catch (e) {
    return null;
  }
}

// cmpCandidates lists what can be picked: the account's saved hotspots plus
// the busiest ones near the first hotspot.
async function cmpCandidates(origin) {
  const out = state.favorites.map((f) => ({ locId: f.locId, name: f.locName || f.locId, group: "saved" }));
  if (origin && (origin.lat || origin.lng)) {
    try {
      const res = await fetch(`/api/hotspots?lat=${origin.lat}&lng=${origin.lng}&dist=${CMP_NEARBY_KM}`);
      if (res.ok) {
        const near = (await res.json()).slice(0, CMP_NEARBY_MAX);
        for (const h of near) out.push({ locId: h.locId, name: h.locName || h.locId, group: "nearby" });
      }
    } catch (e) {
      // Nearby suggestions are optional.
    }
  }
  return out;
}

async function openCompare(a, b) {
  const stale = () => currentRoute.name !== "compare" || currentRoute.locId !== a || currentRoute.locIdB !== b;
  window.scrollTo(0, 0);
  $("compareBody").innerHTML = "";
  $("comparePicker").innerHTML = "";
  $("compareDist").textContent = "";
  $("compareStatus").textContent = "";
  $("compareBack").href = a && LOC_ID_RE.test(a) ? "#/hotspot/" + encodeURIComponent(a) : "#/home";
  cmp.data = null;

  if (!LOC_ID_RE.test(a || "")) {
    if (state.favorites.length >= 2) {
      const hash = compareHash(state.favorites[0].locId, state.favorites[1].locId);
      history.replaceState(null, "", hash);
      await renderRoute();
    } else {
      $("compareStatus").textContent = t("compare.start");
    }
    return;
  }

  if (!b || !LOC_ID_RE.test(b) || a === b) {
    const origin = await cmpFetchHotspot(a);
    if (stale()) return;
    if (!origin) { $("compareStatus").textContent = t("hotspot.error"); return; }
    renderComparePicker(origin, null, await cmpCandidates(origin));
    if (stale()) return;
    $("compareStatus").textContent = t("compare.pickPrompt", { name: origin.name });
    return;
  }

  $("compareStatus").textContent = t("compare.loading");
  let data;
  try {
    const res = await fetch(`/api/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}&lang=${encodeURIComponent(state.language)}`);
    if (!res.ok) throw new Error(String(res.status));
    data = await res.json();
  } catch (e) {
    if (!stale()) $("compareStatus").textContent = t("compare.error");
    return;
  }
  if (stale()) return;
  cmp.data = data;
  $("compareStatus").textContent = "";

  renderComparePicker(data.a, data.b, state.favorites.map((f) => ({ locId: f.locId, name: f.locName || f.locId, group: "saved" })));
  $("compareDist").textContent = t("compare.apart", { km: data.distanceKm.toFixed(1) });
  renderCompareBody(data);

  const candidates = await cmpCandidates(data.a);
  if (!stale()) renderComparePicker(data.a, data.b, candidates);
}

// ---- Derived views --------------------------------------------------------

function cmpDifferences(data) {
  const shared = data.species.filter((s) => s.logRatio != null && s.reliable);
  const aSide = shared.filter((s) => s.logRatio > 0).sort((x, y) => y.logRatio - x.logRatio).slice(0, CMP_DIFF_EACH);
  const bSide = shared.filter((s) => s.logRatio < 0).sort((x, y) => x.logRatio - y.logRatio).slice(0, CMP_DIFF_EACH);
  return { aSide, bSide, ordered: aSide.concat(bSide.slice().reverse()) };
}

function cmpOnly(data, side) {
  const key = side === "a" ? "countA" : "countB";
  const other = side === "a" ? "countB" : "countA";
  const rank = side === "a" ? "rankA" : "rankB";
  return data.species
    .filter((s) => s[rank] > 0 && s[other] === 0)
    .sort((x, y) => y[key] - x[key] || x.comName.localeCompare(y.comName));
}

function cmpVerdict(data, diffs) {
  const union = data.shared + data.onlyA + data.onlyB;
  const pct = union ? Math.round((data.shared / union) * 100) : 0;
  const parts = [t("compare.verdict.overlap", { shared: data.shared, union, pct })];
  const names = (list) => list.slice(0, 3).map((s) => s.comName).join(", ");
  if (diffs.aSide.length) parts.push(t("compare.verdict.leans", { name: data.a.name, birds: names(diffs.aSide) }));
  if (diffs.bSide.length) parts.push(t("compare.verdict.leans", { name: data.b.name, birds: names(diffs.bSide) }));
  return parts.join(" ");
}

// ---- Sections -------------------------------------------------------------

function cmpSw(cls) { return `<i class="cmp-sw ${cls}"></i>`; }

function cmpSummary(data, diffs) {
  const union = data.shared + data.onlyA + data.onlyB;
  const pct = union ? Math.round((data.shared / union) * 100) : 0;
  const shared = data.species.filter((s) => s.rankA > 0 && s.rankB > 0);
  const near = shared.filter((s) => Math.abs(s.rankA - s.rankB) <= 3).length;
  const nearPct = shared.length ? Math.round((near / shared.length) * 100) : 0;
  const total = Math.max(union, 1);
  return `
  <section class="cmp-card">
    <p class="cmp-verdict">${escapeHtml(cmpVerdict(data, diffs))}</p>
    <div class="cmp-tiles">
      <div class="cmp-tile"><span class="big">${data.shared}<small>/ ${union}</small></span><span class="lbl">${escapeHtml(t("compare.tile.shared"))}</span></div>
      <div class="cmp-tile"><span class="big">${pct}%</span><span class="lbl">${escapeHtml(t("compare.tile.overlap"))}</span></div>
      <div class="cmp-tile"><span class="big">${nearPct}%</span><span class="lbl">${escapeHtml(t("compare.tile.rank"))}</span></div>
    </div>
    <div>
      <div class="cmp-ov" role="img" aria-label="${escapeHtml(t("compare.overlap.aria", { a: data.onlyA, nameA: data.a.name, s: data.shared, b: data.onlyB, nameB: data.b.name }))}">
        <span class="oa" style="flex:${data.onlyA || 0.0001}"></span><span class="os" style="flex:${data.shared || 0.0001}"></span><span class="ob" style="flex:${data.onlyB || 0.0001}"></span>
      </div>
      <div class="cmp-ovl cmp-num">
        <span><b>${data.onlyA}</b> ${escapeHtml(t("compare.onlyAt", { name: data.a.name }))}</span>
        <span><b>${data.shared}</b> ${escapeHtml(t("compare.inBoth"))}</span>
        <span><b>${data.onlyB}</b> ${escapeHtml(t("compare.onlyAt", { name: data.b.name }))}</span>
      </div>
    </div>
  </section>`;
}

function cmpDifferencesSection(data, diffs) {
  const ratio = Math.max(data.a.observations, data.b.observations) / Math.max(1, Math.min(data.a.observations, data.b.observations));
  const bigger = data.a.observations >= data.b.observations ? data.a.name : data.b.name;
  const volume = ratio >= 1.15 ? " " + t("compare.diff.volume", { name: bigger, ratio: ratio.toFixed(1) }) : "";

  let rows;
  if (diffs.ordered.length === 0) {
    rows = `<p class="cmp-sub">${escapeHtml(t("compare.diff.none"))}</p>`;
  } else {
    rows = diffs.ordered.map((s) => {
      const side = s.logRatio > 0 ? "a" : "b";
      const w = Math.min(Math.abs(s.logRatio), CMP_DOMAIN_LOG2) / CMP_DOMAIN_LOG2 * 50;
      const mult = "×" + Math.pow(2, Math.abs(s.logRatio)).toFixed(1);
      const faint = s.countA + s.countB < CMP_LOW_SAMPLE ? " thin" : "";
      const inside = w > 36;
      const multStyle = inside
        ? `${side === "a" ? "right" : "left"}:calc(50% + ${w}% - 38px);color:#fff`
        : `${side === "a" ? "right" : "left"}:calc(50% + ${w}% + 6px)`;
      return `
      <li class="cmp-drow">
        <div class="cmp-nm"><span>${escapeHtml(s.comName)}</span><span class="cmp-cnt">${cmpSw("a")}${cmpNum(s.countA)} vs ${cmpSw("b")}${cmpNum(s.countB)}</span></div>
        <div class="cmp-track"><span class="cmp-bar ${side}${faint}" style="width:${w.toFixed(1)}%"></span><span class="cmp-mult" style="${multStyle}">${mult}</span></div>
      </li>`;
    }).join("");
    rows = `
    <div class="cmp-daxis"><span style="left:0">×4</span><span style="left:25%">×2</span><span style="left:50%">${escapeHtml(t("compare.diff.equal"))}</span><span style="left:75%">×2</span><span style="left:100%">×4</span></div>
    <div class="cmp-dwrap"><div class="cmp-dgrid"><i style="left:25%"></i><i style="left:50%"></i><i style="left:75%"></i></div><ul class="cmp-dlist">${rows}</ul></div>
    <div class="cmp-key"><i></i>${escapeHtml(t("compare.diff.lowKey", { n: CMP_LOW_SAMPLE }))}</div>`;
  }
  return `
  <section class="cmp-card">
    <div><h2>${escapeHtml(t("compare.diff.title"))}</h2><p class="cmp-sub">${escapeHtml(t("compare.diff.intro") + volume)}</p></div>
    <div class="cmp-sidecap"><span>${cmpSw("a")}${escapeHtml(t("compare.diff.more", { name: data.a.name }))}</span><span>${escapeHtml(t("compare.diff.more", { name: data.b.name }))}${cmpSw("b")}</span></div>
    ${rows}
  </section>`;
}

function cmpRankSection(data) {
  const both = data.species
    .filter((s) => s.rankA > 0 && s.rankB > 0)
    .sort((x, y) => (y.shareA + y.shareB) - (x.shareA + x.shareB))
    .slice(0, CMP_RANK_ROWS);
  if (both.length === 0) return "";
  const maxShare = Math.max(...both.map((s) => Math.max(s.shareA, s.shareB)), 0.0001);
  const rows = both.map((s) => {
    const shift = s.rankB - s.rankA; // > 0: lower at B
    let chip = "";
    if (Math.abs(shift) >= CMP_RANK_SHIFT) {
      chip = `<span class="cmp-chip ${shift > 0 ? "dn" : "up"}">${shift > 0 ? "▼" : "▲"} ${Math.abs(shift)}</span>`;
    }
    const half = (side, share, rank, name) => `<span class="cmp-half ${side === "a" ? "l" : "r"}" title="${escapeHtml(t("compare.rank.share", { pct: cmpPct(share), name }))}"><span class="cmp-bar ${side}" style="width:${(share / maxShare * 100).toFixed(1)}%"></span><span class="cmp-pc">${cmpPct(share)}%</span></span>`;
    return `
    <li class="cmp-rrow">
      <div class="cmp-nm"><span>${escapeHtml(s.comName)}</span>${chip}</div>
      <div class="cmp-rbars"><span class="cmp-rk">#${s.rankA}</span>${half("a", s.shareA, s.rankA, data.a.name)}${half("b", s.shareB, s.rankB, data.b.name)}<span class="cmp-rk">#${s.rankB}</span></div>
    </li>`;
  }).join("");
  return `
  <section class="cmp-card">
    <div><h2>${escapeHtml(t("compare.rank.title"))}</h2><p class="cmp-sub">${escapeHtml(t("compare.rank.intro", { n: CMP_RANK_SHIFT, a: data.a.name, b: data.b.name }))}</p></div>
    <div class="cmp-rhead"><span>${escapeHtml(t("compare.rank.rank"))}</span><span>${cmpSw("a")}${escapeHtml(data.a.name)}</span><span>${escapeHtml(data.b.name)}</span><span>${escapeHtml(t("compare.rank.rank"))}</span></div>
    <ul class="cmp-rlist">${rows}</ul>
  </section>`;
}

function cmpOnlySection(data) {
  const column = (side, hotspot, list) => {
    const key = side === "a" ? "countA" : "countB";
    const items = list.slice(0, CMP_ONLY_ROWS).map((s) => `
      <li><span>${escapeHtml(s.comName)}${s[key] >= CMP_REGULAR ? `<span class="cmp-chip reg">${escapeHtml(t("compare.only.regular"))}</span>` : ""}</span><span class="fam">${escapeHtml(s.family)}</span><span class="n">${cmpNum(s[key])}</span></li>`).join("");
    return `<div>
      <h3><span>${cmpSw(side)}${escapeHtml(t("compare.only.name", { name: hotspot.name }))}</span><span>${escapeHtml(tn("compare.only.species", list.length))}</span></h3>
      ${items ? `<ul>${items}</ul>` : `<p class="cmp-sub">${escapeHtml(t("compare.only.none"))}</p>`}
    </div>`;
  };
  return `
  <section class="cmp-card cmp-only">
    <div><h2>${escapeHtml(t("compare.only.title"))}</h2><p class="cmp-sub">${escapeHtml(t("compare.only.intro", { n: CMP_REGULAR }))}</p></div>
    <div class="cmp-two">${column("a", data.a, cmpOnly(data, "a"))}${column("b", data.b, cmpOnly(data, "b"))}</div>
  </section>`;
}

function cmpFamilySection(data) {
  const fams = data.families.slice(0, CMP_FAMILY_ROWS);
  if (fams.length === 0) return "";
  const max = Math.max(...fams.map((f) => Math.max(f.shareA, f.shareB)), 0.0001);
  const rows = fams.map((f) => `
    <li class="cmp-frow"><span>${escapeHtml(f.family)}</span>
      <span class="cmp-fbars"><span class="cmp-bar a" style="width:${(f.shareA / max * 100).toFixed(1)}%"></span><span class="cmp-bar b" style="width:${(f.shareB / max * 100).toFixed(1)}%"></span></span>
      <span class="cmp-fnum"><span>${cmpPct(f.shareA)}%</span><span>${cmpPct(f.shareB)}%</span></span></li>`).join("");
  return `
  <section class="cmp-card">
    <div><h2>${escapeHtml(t("compare.family.title"))}</h2><p class="cmp-sub">${escapeHtml(t("compare.family.intro"))}</p></div>
    <ul class="cmp-flist">${rows}</ul>
  </section>`;
}

function renderCompareBody(data) {
  const diffs = cmpDifferences(data);
  const body = $("compareBody");
  const onlyA = cmpOnly(data, "a");
  const onlyB = cmpOnly(data, "b");
  body.innerHTML =
    cmpSummary(data, diffs) +
    cmpDifferencesSection(data, diffs) +
    cmpRankSection(data) +
    cmpOnlySection(data) +
    cmpFamilySection(data) +
    `<div class="cmp-ctas" id="compareCtas"></div>
     <p class="cmp-foot">${escapeHtml(t("compare.foot"))}</p>`;

  const ctas = $("compareCtas");
  const addCta = (cls, label, run) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn " + cls;
    btn.textContent = label;
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try { await run(); } finally { btn.disabled = false; }
    });
    ctas.appendChild(btn);
  };
  if (diffs.ordered.length > 0) {
    addCta("btn-primary", t("compare.learn.diff", { n: diffs.ordered.length }),
      () => cmpLearn(diffs.ordered.map((s) => s.code), [data.a, data.b]));
  }
  if (onlyA.length > 0) {
    addCta("btn-secondary", t("compare.learn.only", { n: onlyA.length, name: data.a.name }),
      () => cmpLearn(onlyA.map((s) => s.code), [data.a]));
  }
  if (onlyB.length > 0) {
    addCta("btn-secondary", t("compare.learn.only", { n: onlyB.length, name: data.b.name }),
      () => cmpLearn(onlyB.map((s) => s.code), [data.b]));
  }
}

// cmpLearn opens Learn on the given species codes, in that order, taking each
// card (photos, names) from the first listed hotspot that has the species.
async function cmpLearn(codes, hotspots) {
  const byCode = new Map();
  try {
    for (const h of hotspots) {
      const res = await fetch(`/api/hotspots/${encodeURIComponent(h.locId)}/species?lang=${encodeURIComponent(state.language)}&mode=popularity`);
      if (!res.ok) throw new Error(String(res.status));
      for (const sp of await res.json()) {
        if (!byCode.has(sp.speciesCode)) byCode.set(sp.speciesCode, Object.assign({ locId: h.locId, locName: h.name }, sp));
      }
    }
  } catch (e) {
    $("compareStatus").textContent = t("compare.learn.error");
    return;
  }
  const cards = codes.map((c) => byCode.get(c)).filter(Boolean);
  if (cards.length === 0) {
    $("compareStatus").textContent = t("compare.learn.error");
    return;
  }
  $("compareStatus").textContent = "";
  state.secondaryNames = {};
  learn.start(cards, hotspots[0].locId, 0, { quiet: true });
}
