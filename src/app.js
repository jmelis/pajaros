'use strict';

// ─── Configuration ────────────────────────────────────────────────────────────

// GitHub Pages deploys under /pajaros/ — adjust BASE_PATH for local dev if needed
const BASE_PATH = (() => {
  // Detect GitHub Pages repo prefix from pathname
  const m = location.pathname.match(/^(\/[^/]+)\//);
  if (m && m[1] !== '') return m[1];
  return '';
})();

const CATALOG_URL = `${BASE_PATH}/catalog.json`;
const BIRDS_BASE = `${BASE_PATH}/birds`;

// Swipe thresholds
const SWIPE_MIN_PX = 50;       // minimum displacement to register
const SWIPE_RATIO = 2.0;       // dominant/secondary axis ratio to avoid diagonals

// ─── State ────────────────────────────────────────────────────────────────────

let catalog = null;
let currentPlace = 'alicante';
let currentBirdIndex = 0;
let currentImageIndex = 0;
let isTransitioning = false;

// ─── DOM references ───────────────────────────────────────────────────────────

const cardEl       = document.getElementById('card');
const imgEl        = document.getElementById('bird-img');
const imgContainer = document.getElementById('img-container');
const imgLoading   = document.getElementById('img-loading');
const nameLatin    = document.getElementById('name-latin');
const nameEs       = document.getElementById('name-es');
const nameFr       = document.getElementById('name-fr');
const imgDots      = document.getElementById('img-dots');
const birdCounter  = document.getElementById('bird-counter');

const btnPrev    = document.getElementById('btn-prev');
const btnNext    = document.getElementById('btn-next');
const btnImgPrev = document.getElementById('btn-img-prev');
const btnImgNext = document.getElementById('btn-img-next');

const menuBtn     = document.getElementById('menu-btn');
const menuPanel   = document.getElementById('menu-panel');
const menuClose   = document.getElementById('menu-close');
const menuOverlay = document.getElementById('menu-overlay');
const placeBtns   = document.querySelectorAll('.place-btn');
const creditsList = document.getElementById('credits-list');

// ─── Utility ──────────────────────────────────────────────────────────────────

function getSpeciesList() {
  return catalog.places[currentPlace].species;
}

function getCurrentSpecies() {
  const key = getSpeciesList()[currentBirdIndex];
  return catalog.species[key];
}

function imageUrl(sp, file) {
  return `${BIRDS_BASE}/${encodeURIComponent(sp.scientific_name)}/${file}`;
}

// ─── Rendering ───────────────────────────────────────────────────────────────

function render() {
  const sp = getCurrentSpecies();
  const imgList = sp.images;
  const imgEntry = imgList[currentImageIndex];

  // Show loading state
  imgEl.classList.add('loading');
  imgLoading.classList.add('visible');

  // Cancel any pending load on current img
  const newImg = new Image();
  const src = imageUrl(sp, imgEntry.file);

  newImg.onload = () => {
    imgEl.src = src;
    imgEl.alt = imgEntry.alt_es || sp.name_es;
    imgEl.classList.remove('loading');
    imgLoading.classList.remove('visible');
    imgContainer.setAttribute('aria-label', imgEntry.alt_es || sp.name_es);
  };

  newImg.onerror = () => {
    imgEl.src = '';
    imgEl.alt = sp.name_es;
    imgEl.classList.remove('loading');
    imgLoading.classList.remove('visible');
  };

  newImg.src = src;

  nameLatin.textContent = sp.scientific_name;
  nameEs.textContent = sp.name_es;
  nameFr.textContent = sp.name_fr;

  // Counter
  const list = getSpeciesList();
  birdCounter.textContent = `${currentBirdIndex + 1} / ${list.length}`;
  birdCounter.setAttribute('aria-label', `Ave ${currentBirdIndex + 1} de ${list.length}`);

  // Image dots
  imgDots.innerHTML = '';
  if (imgList.length > 1) {
    imgList.forEach((_, i) => {
      const dot = document.createElement('div');
      dot.className = 'img-dot' + (i === currentImageIndex ? ' active' : '');
      imgDots.appendChild(dot);
    });
  }

  // Show/hide vertical nav buttons
  const hasMultipleImages = imgList.length > 1;
  btnImgPrev.classList.toggle('hidden', !hasMultipleImages);
  btnImgNext.classList.toggle('hidden', !hasMultipleImages);

  // Update place button active state
  placeBtns.forEach(btn => {
    btn.classList.toggle('active', btn.dataset.place === currentPlace);
  });

  // Preload neighbors
  schedulePreload(sp);
}

// ─── Preload ──────────────────────────────────────────────────────────────────

const preloadCache = new Map();

function preloadImage(url) {
  if (preloadCache.has(url)) return;
  const img = new Image();
  img.src = url;
  preloadCache.set(url, img);
  // Limit cache size
  if (preloadCache.size > 20) {
    const firstKey = preloadCache.keys().next().value;
    preloadCache.delete(firstKey);
  }
}

function schedulePreload(currentSp) {
  const list = getSpeciesList();

  // Preload all images of current species
  for (const img of currentSp.images) {
    preloadImage(imageUrl(currentSp, img.file));
  }

  // Preload poster of next and previous species
  const nextIdx = (currentBirdIndex + 1) % list.length;
  const prevIdx = (currentBirdIndex - 1 + list.length) % list.length;

  for (const idx of [nextIdx, prevIdx]) {
    const sp = catalog.species[list[idx]];
    if (sp) preloadImage(imageUrl(sp, sp.poster_image));
  }
}

// ─── Navigation ───────────────────────────────────────────────────────────────

function goNextBird() {
  const list = getSpeciesList();
  currentBirdIndex = (currentBirdIndex + 1) % list.length;
  currentImageIndex = 0;
  render();
}

function goPrevBird() {
  const list = getSpeciesList();
  currentBirdIndex = (currentBirdIndex - 1 + list.length) % list.length;
  currentImageIndex = 0;
  render();
}

function goNextImage() {
  const sp = getCurrentSpecies();
  currentImageIndex = (currentImageIndex + 1) % sp.images.length;
  render();
}

function goPrevImage() {
  const sp = getCurrentSpecies();
  currentImageIndex = (currentImageIndex - 1 + sp.images.length) % sp.images.length;
  render();
}

function goToPlace(place) {
  if (!catalog.places[place]) return;
  currentPlace = place;
  currentBirdIndex = 0;
  currentImageIndex = 0;
  updateUrl(place);
  render();
  closeMenu();
}

// ─── URL routing ──────────────────────────────────────────────────────────────

const PLACE_SLUGS = ['alicante', 'ourense', 'bruselas'];

function detectPlaceFromUrl() {
  const path = location.pathname.toLowerCase();
  for (const slug of PLACE_SLUGS) {
    if (path.includes(`/${slug}`)) return slug;
  }
  return null;
}

function updateUrl(place) {
  const newPath = `${BASE_PATH}/${place}/`;
  if (location.pathname !== newPath) {
    history.pushState({ place }, '', newPath);
  }
}

// ─── Touch / swipe ────────────────────────────────────────────────────────────

let touchStartX = 0;
let touchStartY = 0;
let touchActive = false;

imgContainer.addEventListener('touchstart', e => {
  if (e.touches.length !== 1) return;
  touchStartX = e.touches[0].clientX;
  touchStartY = e.touches[0].clientY;
  touchActive = true;
}, { passive: true });

imgContainer.addEventListener('touchend', e => {
  if (!touchActive) return;
  touchActive = false;

  const dx = e.changedTouches[0].clientX - touchStartX;
  const dy = e.changedTouches[0].clientY - touchStartY;
  handleSwipe(dx, dy);
}, { passive: true });

imgContainer.addEventListener('touchcancel', () => { touchActive = false; }, { passive: true });

function handleSwipe(dx, dy) {
  const absDx = Math.abs(dx);
  const absDy = Math.abs(dy);

  // Must exceed minimum distance
  if (absDx < SWIPE_MIN_PX && absDy < SWIPE_MIN_PX) return;

  // Must be clearly in one axis (avoid diagonals)
  if (absDx > absDy) {
    if (absDx < absDy * SWIPE_RATIO) return; // too diagonal
    // Horizontal: change bird
    if (dx < 0) goNextBird();
    else goPrevBird();
  } else {
    if (absDy < absDx * SWIPE_RATIO) return; // too diagonal
    // Vertical: change image (only if multiple images)
    const sp = getCurrentSpecies();
    if (sp.images.length <= 1) return;
    if (dy < 0) goNextImage();
    else goPrevImage();
  }
}

// ─── Pointer drag (desktop) ───────────────────────────────────────────────────

let pointerStartX = 0;
let pointerStartY = 0;
let pointerActive = false;

imgContainer.addEventListener('pointerdown', e => {
  if (e.pointerType === 'touch') return; // handled by touch events
  pointerStartX = e.clientX;
  pointerStartY = e.clientY;
  pointerActive = true;
  imgContainer.setPointerCapture(e.pointerId);
});

imgContainer.addEventListener('pointerup', e => {
  if (!pointerActive || e.pointerType === 'touch') return;
  pointerActive = false;
  const dx = e.clientX - pointerStartX;
  const dy = e.clientY - pointerStartY;
  handleSwipe(dx, dy);
});

imgContainer.addEventListener('pointercancel', () => { pointerActive = false; });

// ─── Keyboard ─────────────────────────────────────────────────────────────────

document.addEventListener('keydown', e => {
  if (menuPanel && !menuPanel.hasAttribute('hidden')) return; // menu open

  switch (e.key) {
    case 'ArrowRight': e.preventDefault(); goNextBird(); break;
    case 'ArrowLeft':  e.preventDefault(); goPrevBird(); break;
    case 'ArrowUp':    e.preventDefault(); goPrevImage(); break;
    case 'ArrowDown':  e.preventDefault(); goNextImage(); break;
  }
});

// ─── Button clicks ────────────────────────────────────────────────────────────

btnNext.addEventListener('click', goNextBird);
btnPrev.addEventListener('click', goPrevBird);
btnImgNext.addEventListener('click', goNextImage);
btnImgPrev.addEventListener('click', goPrevImage);

// ─── Menu ─────────────────────────────────────────────────────────────────────

function openMenu() {
  menuPanel.removeAttribute('hidden');
  menuOverlay.classList.add('visible');
  menuBtn.setAttribute('aria-expanded', 'true');
  menuClose.focus();
}

function closeMenu() {
  menuPanel.setAttribute('hidden', '');
  menuOverlay.classList.remove('visible');
  menuBtn.setAttribute('aria-expanded', 'false');
  menuBtn.focus();
}

menuBtn.addEventListener('click', openMenu);
menuClose.addEventListener('click', closeMenu);
menuOverlay.addEventListener('click', closeMenu);

menuPanel.addEventListener('keydown', e => {
  if (e.key === 'Escape') closeMenu();
});

placeBtns.forEach(btn => {
  btn.addEventListener('click', () => goToPlace(btn.dataset.place));
});

// ─── Credits ──────────────────────────────────────────────────────────────────

function buildCredits() {
  const items = [];
  for (const [key, sp] of Object.entries(catalog.species)) {
    for (const attr of sp.attribution) {
      items.push({ bird: sp.name_es, ...attr });
    }
  }

  creditsList.innerHTML = items.map(item => `
    <div class="credit-item">
      <span class="credit-bird">${escHtml(item.bird)}</span> — ${escHtml(item.file)}<br>
      ${escHtml(item.author)} ·
      <a href="${escHtml(item.source_url)}" target="_blank" rel="noopener license">${escHtml(item.license)}</a>
    </div>
  `).join('');
}

function escHtml(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// ─── Browser back/forward ─────────────────────────────────────────────────────

window.addEventListener('popstate', e => {
  const place = (e.state && e.state.place) || detectPlaceFromUrl() || 'alicante';
  if (catalog && catalog.places[place]) {
    currentPlace = place;
    currentBirdIndex = 0;
    currentImageIndex = 0;
    render();
  }
});

// ─── Init ─────────────────────────────────────────────────────────────────────

async function init() {
  try {
    const res = await fetch(CATALOG_URL);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    catalog = await res.json();

    // Detect place from URL
    const placeFromUrl = detectPlaceFromUrl();
    if (placeFromUrl && catalog.places[placeFromUrl]) {
      currentPlace = placeFromUrl;
    }

    // Set initial URL state
    updateUrl(currentPlace);

    buildCredits();
    render();
  } catch (err) {
    console.error('Failed to load catalog:', err);
    nameEs.textContent = 'Error al cargar el catálogo';
    nameLatin.textContent = err.message;
  }
}

init();
