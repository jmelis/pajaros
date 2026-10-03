"use strict";

// Compare view: two areas side by side (#/compare/<a>/<b>). The server
// (GET /api/compare) sends the union of both species lists with counts,
// ranks and shares; everything shown here is derived from that. Loaded before
// app.js and relies on its globals ($, t, tn, state, learn, ...) only at call
// time.

const CMP_LOW_SAMPLE = 45; // combined records below which a ratio is only a hint
const CMP_RANK_SHIFT = 5; // rank move that earns a ▲/▼ chip
const CMP_REGULAR = 10; // records that make a one-area species "regular"
const CMP_DIFF_EACH = 6; // differences shown per side
const CMP_RANK_ROWS = 12;
const CMP_ONLY_ROWS = 8;
const CMP_FAMILY_ROWS = 7;
const CMP_DOMAIN_LOG2 = 2; // difference bars span equal .. ×4

const cmp = { data: null, onlySide: "a" };

function compareHash(a, b) {
  return "#/compare" + (a ? "/" + encodeKey(a) : "") + (b ? "/" + encodeKey(b) : "");
}

function cmpPct(x) { return (x * 100).toFixed(1); }
function cmpNum(n) { return Number(n).toLocaleString(); }

function cmpOptions(areas, selected, placeholder) {
  const sel = document.createElement("select");
  if (placeholder) {
    const o = document.createElement("option");
    o.value = "";
    o.textContent = placeholder;
    o.disabled = true;
    o.selected = !selected;
    sel.appendChild(o);
  }
  for (const a of areas) {
    const o = document.createElement("option");
    o.value = a.key;
    o.textContent = areaTitle(a);
    o.selected = a.key === selected;
    sel.appendChild(o);
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

// cmpChoices lists what can be picked: the two areas in play, then the
// account's saved ones.
function cmpChoices(a, b) {
  const out = [];
  const seen = new Set();
  for (const x of [a, b].concat(state.favorites)) {
    if (!x || seen.has(x.key)) continue;
    seen.add(x.key);
    out.push(x);
  }
  return out;
}

// renderComparePicker draws the two area selectors. Either side may be null
// while the user is still choosing the second area.
function renderComparePicker(a, b) {
  const picker = $("comparePicker");
  picker.innerHTML = "";
  const choices = cmpChoices(a, b);
  const selA = cmpOptions(choices, a && a.key, a ? "" : t("compare.choose"));
  const selB = cmpOptions(choices, b && b.key, b ? "" : t("compare.choose"));
  selA.setAttribute("aria-label", t("compare.areaA"));
  selB.setAttribute("aria-label", t("compare.areaB"));
  const meta = (x) => (x && x.species != null
    ? t("compare.meta", { species: cmpNum(x.species), obs: cmpNum(x.observations) })
    : "");

  const go = (na, nb) => navigate(compareHash(na, nb));
  selA.addEventListener("change", () => {
    const v = selA.value;
    go(v, b && b.key === v ? a.key : (b ? b.key : ""));
  });
  selB.addEventListener("change", () => {
    const v = selB.value;
    go(a && a.key === v ? b && b.key : (a ? a.key : ""), v);
  });

  const swap = document.createElement("button");
  swap.type = "button";
  swap.className = "cmp-swap";
  swap.textContent = "⇄";
  swap.setAttribute("aria-label", t("compare.swap"));
  swap.disabled = !(a && b);
  swap.addEventListener("click", () => go(b.key, a.key));

  picker.append(cmpSpot("a", "compare.areaA", selA, meta(a)), swap, cmpSpot("b", "compare.areaB", selB, meta(b)));
}

async function cmpFetchArea(key) {
  try {
    const res = await fetch(`/api/places/${encodeKey(key)}`);
    return res.ok ? await res.json() : null;
  } catch (e) {
    return null;
  }
}

async function openCompare(a, b) {
  const stale = () => currentRoute.name !== "compare" || currentRoute.key !== a || currentRoute.keyB !== b;
  window.scrollTo(0, 0);
  $("compareBody").innerHTML = "";
  $("comparePicker").innerHTML = "";
  $("compareDist").textContent = "";
  $("compareStatus").textContent = "";
  $("compareBack").href = a && AREA_KEY_RE.test(a) ? "#/area/" + encodeKey(a) : "#/home";
  cmp.data = null;
  cmp.onlySide = "a";

  if (!AREA_KEY_RE.test(a || "")) { navigate("#/home"); return; }

  if (!b || !AREA_KEY_RE.test(b) || a === b) {
    const origin = await cmpFetchArea(a);
    if (stale()) return;
    if (!origin) { $("compareStatus").textContent = t("area.error"); return; }
    renderComparePicker(origin, null);
    $("compareStatus").textContent = t("compare.pickPrompt", { name: areaTitle(origin) });
    return;
  }

  $("compareStatus").textContent = t("compare.loading");
  let data;
  try {
    const res = await fetch(`/api/compare?a=${encodeKey(a)}&b=${encodeKey(b)}&lang=${encodeURIComponent(state.language)}`);
    if (!res.ok) throw new Error(String(res.status));
    data = await res.json();
  } catch (e) {
    if (!stale()) $("compareStatus").textContent = t("compare.error");
    return;
  }
  if (stale()) return;
  // Custom circles have no name; label both sides for the sections below.
  for (const side of [data.a, data.b]) side.name = areaTitle(side);
  cmp.data = data;
  $("compareStatus").textContent = "";

  renderComparePicker(data.a, data.b);
  $("compareDist").textContent = t("compare.apart", { km: data.distanceKm.toFixed(1) });
  renderCompareBody(data);
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

// cmpBird renders a species name that opens Learn on that bird; list says which
// section's ordering the deck follows.
function cmpBird(list, s) {
  return `<button type="button" class="cmp-bird" data-cmp-list="${list}" data-code="${escapeHtml(s.code)}">${escapeHtml(s.comName)}</button>`;
}

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
        <div class="cmp-nm"><span>${cmpBird("diff", s)}</span><span class="cmp-cnt">${cmpSw("a")}${cmpNum(s.countA)} vs ${cmpSw("b")}${cmpNum(s.countB)}</span></div>
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
    ${diffs.ordered.length ? `<button type="button" class="btn btn-primary" data-cmp-learn="diff">${escapeHtml(t("compare.learn.diff", { n: diffs.ordered.length }))}</button>` : ""}
  </section>`;
}

function cmpRankList(data) {
  return data.species
    .filter((s) => s.rankA > 0 && s.rankB > 0)
    .sort((x, y) => (y.shareA + y.shareB) - (x.shareA + x.shareB))
    .slice(0, CMP_RANK_ROWS);
}

function cmpRankSection(data) {
  const both = cmpRankList(data);
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
      <div class="cmp-nm"><span>${cmpBird("rank", s)}</span>${chip}</div>
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
  const side = cmp.onlySide;
  const area = side === "a" ? data.a : data.b;
  const list = cmpOnly(data, side);
  const key = side === "a" ? "countA" : "countB";
  const items = list.slice(0, CMP_ONLY_ROWS).map((s) => `
      <li><span>${cmpBird("only", s)}${s[key] >= CMP_REGULAR ? `<span class="cmp-chip reg">${escapeHtml(t("compare.only.regular"))}</span>` : ""}</span><span class="fam">${escapeHtml(s.family)}</span><span class="n">${cmpNum(s[key])}</span></li>`).join("");
  const seg = (sd, a) => `<button type="button" class="cmp-seg ${sd}" data-cmp-side="${sd}" aria-pressed="${sd === side}">${cmpSw(sd)}${escapeHtml(a.name)}</button>`;
  return `
  <section class="cmp-card cmp-only" id="cmpOnly">
    <div><h2>${escapeHtml(t("compare.only.title"))}</h2><p class="cmp-sub">${escapeHtml(t("compare.only.intro", { n: CMP_REGULAR }))}</p></div>
    <div class="cmp-segs" role="group">${seg("a", data.a)}${seg("b", data.b)}</div>
    <h3><span>${escapeHtml(t("compare.only.name", { name: area.name }))}</span><span>${escapeHtml(tn("compare.only.species", list.length))}</span></h3>
    ${items ? `<ul>${items}</ul>` : `<p class="cmp-sub">${escapeHtml(t("compare.only.none"))}</p>`}
    ${list.length ? `<button type="button" class="btn btn-secondary" data-cmp-learn="only">${escapeHtml(t("compare.learn.only", { n: list.length, name: area.name }))}</button>` : ""}
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
  body.innerHTML =
    cmpSummary(data, diffs) +
    cmpDifferencesSection(data, diffs) +
    cmpRankSection(data) +
    cmpOnlySection(data) +
    cmpFamilySection(data) +
    `<p class="cmp-foot">${escapeHtml(t("compare.foot"))}</p>`;

  const lists = {
    diff: { codes: diffs.ordered.map((s) => s.code), areas: [data.a, data.b] },
    rank: { codes: cmpRankList(data).map((s) => s.code), areas: [data.a, data.b] },
  };
  const onlyDeck = () => {
    const area = cmp.onlySide === "a" ? data.a : data.b;
    return { codes: cmpOnly(data, cmp.onlySide).map((s) => s.code), areas: [area] };
  };
  body.onclick = async (e) => {
    const seg = e.target.closest("[data-cmp-side]");
    if (seg) {
      cmp.onlySide = seg.dataset.cmpSide;
      $("cmpOnly").outerHTML = cmpOnlySection(data);
      return;
    }
    const bird = e.target.closest("[data-cmp-list]");
    if (bird) {
      const deck = bird.dataset.cmpList === "only" ? onlyDeck() : lists[bird.dataset.cmpList];
      await cmpLearn(deck.codes, deck.areas, deck.codes.indexOf(bird.dataset.code));
      return;
    }
    const go = e.target.closest("[data-cmp-learn]");
    if (go) {
      const deck = go.dataset.cmpLearn === "only" ? onlyDeck() : lists.diff;
      go.disabled = true;
      try { await cmpLearn(deck.codes, deck.areas, 0); } finally { go.disabled = false; }
    }
  };
}

// cmpLearn opens Learn on the given species codes, in that order, taking each
// card (photos, names) from the first listed area that has the species.
async function cmpLearn(codes, areas, index = 0) {
  const byCode = new Map();
  try {
    for (const h of areas) {
      const res = await fetch(`/api/places/${encodeKey(h.key)}/species?lang=${encodeURIComponent(state.language)}&mode=popularity`);
      if (!res.ok) throw new Error(String(res.status));
      for (const sp of await res.json()) {
        if (!byCode.has(sp.speciesCode)) byCode.set(sp.speciesCode, Object.assign({ areaKey: h.key, areaName: h.name }, sp));
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
  learn.start(cards, areas[0].key, Math.max(0, cards.findIndex((c) => c.speciesCode === codes[index])), { quiet: true });
}
