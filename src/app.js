'use strict';

// ─── Configuration ────────────────────────────────────────────────────────────

// Opened straight from disk (file://) rather than served over http(s): there's
// no meaningful path-prefix routing there, and paths must stay relative.
const IS_FILE = location.protocol === 'file:';

// GitHub Pages deploys under /pajaros/ — adjust BASE_PATH for local dev if needed
const BASE_PATH = (() => {
  if (IS_FILE) return '';
  // Detect GitHub Pages repo prefix from pathname
  const m = location.pathname.match(/^(\/[^/]+)\//);
  if (m && m[1] !== '') return m[1];
  return '';
})();

const BIRDS_BASE = IS_FILE ? 'birds' : `${BASE_PATH}/birds`;

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

const btnPrev       = document.getElementById('btn-prev');
const btnNext       = document.getElementById('btn-next');
const btnMorePhotos = document.getElementById('btn-more-photos');
const photosCount   = document.getElementById('photos-count');

const menuBtn     = document.getElementById('menu-btn');
const menuPanel   = document.getElementById('menu-panel');
const menuClose   = document.getElementById('menu-close');
const menuOverlay = document.getElementById('menu-overlay');
const placeBtns   = document.querySelectorAll('.place-btn');
const btnPrint    = document.getElementById('btn-print');
const printSheet  = document.getElementById('print-sheet');

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

  // More-photos button: only shown (and only useful) when there's more than one image
  const hasMultipleImages = imgList.length > 1;
  btnMorePhotos.hidden = !hasMultipleImages;
  if (hasMultipleImages) {
    photosCount.textContent = `${currentImageIndex + 1}/${imgList.length}`;
  }

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

// Slides the card out, swaps its content, then slides it back in from the
// opposite side — a single element "ping-pongs" rather than animating two.
function animateCardChange(direction, mutate) {
  if (isTransitioning) return;
  isTransitioning = true;

  const exitClass = direction === 'next' ? 'shift-left' : 'shift-right';
  const enterClass = direction === 'next' ? 'shift-right' : 'shift-left';

  cardEl.addEventListener('transitionend', function onExitEnd(e) {
    if (e.target !== cardEl) return;
    cardEl.removeEventListener('transitionend', onExitEnd);

    mutate();

    cardEl.classList.add('no-anim');
    cardEl.classList.remove(exitClass);
    cardEl.classList.add(enterClass);
    void cardEl.offsetWidth; // force reflow so the next class change transitions
    cardEl.classList.remove('no-anim');

    requestAnimationFrame(() => {
      cardEl.classList.remove(enterClass);
    });

    cardEl.addEventListener('transitionend', function onEnterEnd(e2) {
      if (e2.target !== cardEl) return;
      cardEl.removeEventListener('transitionend', onEnterEnd);
      isTransitioning = false;
    });
  });

  cardEl.classList.add(exitClass);
}

function goNextBird() {
  animateCardChange('next', () => {
    const list = getSpeciesList();
    currentBirdIndex = (currentBirdIndex + 1) % list.length;
    currentImageIndex = 0;
    render();
  });
}

function goPrevBird() {
  animateCardChange('prev', () => {
    const list = getSpeciesList();
    currentBirdIndex = (currentBirdIndex - 1 + list.length) % list.length;
    currentImageIndex = 0;
    render();
  });
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
  buildPrintSheet();
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
  if (IS_FILE) return; // pushState to an absolute path breaks file:// navigation
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
  if (e.target.closest('button')) return; // let overlay buttons (prev/next, more photos) work as plain buttons
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
  if (e.target.closest('button')) return; // let overlay buttons (prev/next, more photos) work as plain buttons
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
btnMorePhotos.addEventListener('click', goNextImage);

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

// ─── Print sheet (A4 poster collage, replaces the old Puppeteer PDF pipeline) ─

const BIRDS_PER_PAGE = 10;

// Small deterministic hash so the same species always lands on the same size/
// rotation between reloads and print runs, without a lookup table to maintain.
function hashStr(str) {
  let h = 0;
  for (let i = 0; i < str.length; i++) {
    h = (h * 31 + str.charCodeAt(i)) | 0;
  }
  return Math.abs(h);
}

// Grid cell size from the cutout's aspect ratio (falls back to a fixed size
// for species without a cutout yet). ~1/3 of birds get bumped up a size for
// variety, so the poster reads as scattered rather than tiled.
function gridSpanClass(sp) {
  if (!sp.poster_cutout_aspect) return 'span-normal';
  const big = hashStr(sp.scientific_name) % 3 === 0;
  const ratio = sp.poster_cutout_aspect;
  if (ratio >= 1.3) return big ? 'span-wide-big' : 'span-wide';
  if (ratio <= 0.7) return big ? 'span-tall-big' : 'span-tall';
  return big ? 'span-normal-big' : 'span-normal';
}

// A few degrees either way — enough to feel scattered, not enough to tilt
// the bird into an awkward pose.
function rotationDeg(sp) {
  return (hashStr(sp.scientific_name + '#rot') % 9) - 4;
}

function buildPrintSheet() {
  const place = catalog.places[currentPlace];
  const speciesKeys = place.species;

  const pages = [];
  for (let i = 0; i < speciesKeys.length; i += BIRDS_PER_PAGE) {
    pages.push(speciesKeys.slice(i, i + BIRDS_PER_PAGE));
  }

  const pagesHtml = pages.map((keys, pageIdx) => `
    <div class="print-page">
      <div class="print-page-header">
        <span class="print-place-name">Aves de ${escHtml(place.name_es)}</span>
        <span class="print-page-num">Lámina ${pageIdx + 1} de ${pages.length}</span>
      </div>
      <div class="print-poster-grid">
        ${keys.map(key => {
          const sp = catalog.species[key];
          // Prefer the background-removed cutout floating free on the page;
          // falls back to the regular boxed photo if no cutout exists yet.
          const hasCutout = Boolean(sp.poster_cutout);
          const imgSrc = hasCutout ? imageUrl(sp, sp.poster_cutout) : imageUrl(sp, sp.poster_image);
          const imgClass = hasCutout ? 'print-bird-img print-bird-img-cutout' : 'print-bird-img';
          const imgStyle = hasCutout ? `transform: rotate(${rotationDeg(sp)}deg);` : '';
          return `
            <div class="print-bird-cell ${gridSpanClass(sp)}">
              <div class="print-bird-img-wrap">
                <img src="${imgSrc}" alt="${escHtml(sp.name_es)}" class="${imgClass}" style="${imgStyle}">
              </div>
              <p class="print-bird-label">
                <span class="print-name-es">${escHtml(sp.name_es)}</span>
                <span class="print-name-fr">${escHtml(sp.name_fr)}</span>
                <span class="print-name-latin">${escHtml(sp.scientific_name)}</span>
              </p>
            </div>
          `;
        }).join('')}
      </div>
    </div>
  `).join('');

  printSheet.innerHTML = pagesHtml;
}

if (btnPrint) {
  btnPrint.addEventListener('click', () => window.print());
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
    buildPrintSheet();
  }
});

// ─── Init ─────────────────────────────────────────────────────────────────────

function init() {
  try {
    const dataEl = document.getElementById('catalog-data');
    if (!dataEl) throw new Error('catalog-data script tag not found — run scripts/bake.sh');
    catalog = JSON.parse(dataEl.textContent);

    // Detect place from URL
    const placeFromUrl = detectPlaceFromUrl();
    if (placeFromUrl && catalog.places[placeFromUrl]) {
      currentPlace = placeFromUrl;
    }

    // Set initial URL state
    updateUrl(currentPlace);

    render();
    buildPrintSheet();
  } catch (err) {
    console.error('Failed to load catalog:', err);
    nameEs.textContent = 'Error al cargar el catálogo';
    nameLatin.textContent = err.message;
  }
}

init();
