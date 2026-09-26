'use strict';

// ─── Configuration ────────────────────────────────────────────────────────────

// 404.html implements the GitHub Pages SPA-redirect trick: a deep link or a
// plain page refresh on e.g. /pajaros/bruselas/ 404s there, which forwards
// the real path in `?p=` and drops the trailing segment. Restore it before
// any routing below runs, so a refresh or a shared link lands on the right
// place instead of falling back to the chooser.
(function restorePathFromRedirect() {
  const params = new URLSearchParams(location.search);
  if (!params.has('p')) return;
  const restoredPath = location.pathname + params.get('p');
  const q = params.get('q');
  history.replaceState(null, '', restoredPath + (q ? `?${q}` : '') + location.hash);
})();

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

const LANGUAGES = {
  es: 'Español',
  fr: 'Français',
  it: 'Italiano',
  ca: 'Català'
};
const DEFAULT_PRIMARY_LANGUAGE = 'es';
const DEFAULT_SECONDARY_LANGUAGE = 'fr';

// ─── State ────────────────────────────────────────────────────────────────────

let catalog = null;
let currentPlace = null;
let currentBirdIndex = 0;
let currentImageIndex = 0;
let isTransitioning = false;
let primaryLanguage = DEFAULT_PRIMARY_LANGUAGE;
let secondaryLanguage = DEFAULT_SECONDARY_LANGUAGE;

// ─── DOM references ───────────────────────────────────────────────────────────

const cardEl       = document.getElementById('card');
const imgEl        = document.getElementById('bird-img');
const imgContainer = document.getElementById('img-container');
const imgLoading   = document.getElementById('img-loading');
const nameLatin    = document.getElementById('name-latin');
const namePrimary  = document.getElementById('name-primary');
const nameSecondary = document.getElementById('name-secondary');
const imgDots      = document.getElementById('img-dots');
const birdCounter  = document.getElementById('bird-counter');

const btnPrev       = document.getElementById('btn-prev');
const btnNext       = document.getElementById('btn-next');
const btnMorePhotos = document.getElementById('btn-more-photos');
const photosCount   = document.getElementById('photos-count');

const btnPlaySong = document.getElementById('btn-play-song');
const songIcon    = document.getElementById('song-icon');
const birdAudio   = document.getElementById('bird-audio');

const menuBtn     = document.getElementById('menu-btn');
const menuPanel   = document.getElementById('menu-panel');
const menuClose   = document.getElementById('menu-close');
const menuOverlay = document.getElementById('menu-overlay');
const placeBtns   = document.querySelectorAll('.place-btn');
const btnPrint    = document.getElementById('btn-print');
const printSheet  = document.getElementById('print-sheet');
const primaryLanguageSelect = document.getElementById('primary-language');
const secondaryLanguageSelect = document.getElementById('secondary-language');

const stageEl              = document.getElementById('stage');
const placeChooser         = document.getElementById('place-chooser');
const chooserBtns          = document.querySelectorAll('.chooser-btn');
const btnPosterMode        = document.getElementById('btn-poster-mode');
const posterView           = document.getElementById('poster-view');
const posterPlaceName      = document.getElementById('poster-place-name');
const posterGrid           = document.getElementById('poster-grid');
const btnPosterBack        = document.getElementById('btn-poster-back');
const btnPosterShuffle     = document.getElementById('btn-poster-shuffle');
const btnPosterToggleNames = document.getElementById('btn-poster-toggle-names');
const posterToggleIcon     = document.getElementById('poster-toggle-icon');
const posterToggleLabel    = document.getElementById('poster-toggle-label');

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

function speciesName(sp, language) {
  return sp[`name_${language}`] || sp.scientific_name;
}

function readLanguagesFromUrl() {
  const params = new URLSearchParams(location.search);
  const requestedPrimary = params.get('primary');
  const requestedSecondary = params.get('secondary');

  primaryLanguage = Object.hasOwn(LANGUAGES, requestedPrimary)
    ? requestedPrimary
    : DEFAULT_PRIMARY_LANGUAGE;
  secondaryLanguage = Object.hasOwn(LANGUAGES, requestedSecondary)
    ? requestedSecondary
    : DEFAULT_SECONDARY_LANGUAGE;

  if (primaryLanguage === secondaryLanguage) {
    secondaryLanguage = primaryLanguage === DEFAULT_SECONDARY_LANGUAGE
      ? DEFAULT_PRIMARY_LANGUAGE
      : DEFAULT_SECONDARY_LANGUAGE;
  }
}

function syncLanguageControls() {
  primaryLanguageSelect.value = primaryLanguage;
  secondaryLanguageSelect.value = secondaryLanguage;
  namePrimary.lang = primaryLanguage;
  nameSecondary.lang = secondaryLanguage;
}

function languageQueryString() {
  const params = new URLSearchParams(location.search);
  params.delete('p');
  params.delete('q');
  params.set('primary', primaryLanguage);
  params.set('secondary', secondaryLanguage);
  return params.toString();
}

function replaceLanguageQuery() {
  if (IS_FILE) return;
  const query = languageQueryString();
  history.replaceState(history.state, '', `${location.pathname}?${query}${location.hash}`);
}

function selectLanguage(role, language) {
  if (!Object.hasOwn(LANGUAGES, language)) return;

  if (role === 'primary') {
    if (language === secondaryLanguage) secondaryLanguage = primaryLanguage;
    primaryLanguage = language;
  } else {
    if (language === primaryLanguage) primaryLanguage = secondaryLanguage;
    secondaryLanguage = language;
  }

  syncLanguageControls();
  replaceLanguageQuery();
  if (currentPlace) {
    render();
    buildPrintSheet();
    if (posterMode) {
      posterPlaceName.textContent = catalog.places[currentPlace].name_es;
      renderPoster();
    }
  }
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
    imgEl.alt = speciesName(sp, primaryLanguage);
    imgEl.classList.remove('loading');
    imgLoading.classList.remove('visible');
    imgContainer.setAttribute('aria-label', speciesName(sp, primaryLanguage));
  };

  newImg.onerror = () => {
    imgEl.src = '';
    imgEl.alt = speciesName(sp, primaryLanguage);
    imgEl.classList.remove('loading');
    imgLoading.classList.remove('visible');
  };

  newImg.src = src;

  nameLatin.textContent = sp.scientific_name;
  namePrimary.textContent = speciesName(sp, primaryLanguage);
  nameSecondary.textContent = speciesName(sp, secondaryLanguage);

  // Counter (also shows the current place, since a session can start at any place)
  const list = getSpeciesList();
  const placeName = catalog.places[currentPlace].name_es;
  birdCounter.textContent = `${placeName} · ${currentBirdIndex + 1} / ${list.length}`;
  birdCounter.setAttribute('aria-label', `${placeName}, ave ${currentBirdIndex + 1} de ${list.length}`);

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

  // Bird song: stop whatever was playing for the previous bird, load the new one
  birdAudio.pause();
  birdAudio.currentTime = 0;
  setSongPlayingUI(false);
  if (sp.audio) {
    birdAudio.src = imageUrl(sp, sp.audio.file);
    btnPlaySong.hidden = false;
  } else {
    birdAudio.removeAttribute('src');
    btnPlaySong.hidden = true;
  }

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

// Shared by the initial place chooser, the menu's place switcher, and
// back/forward navigation — anywhere we land on a place and need to render it.
function enterPlace(place) {
  if (!catalog.places[place]) return;
  currentPlace = place;
  currentBirdIndex = 0;
  currentImageIndex = 0;
  updateUrl(place);
  placeChooser.hidden = true;
  stageEl.hidden = false;
  render();
  buildPrintSheet();
}

function goToPlace(place) {
  enterPlace(place);
  closeMenu();
}

function showChooser() {
  stageEl.hidden = true;
  placeChooser.hidden = false;
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
  const newUrl = `${newPath}?${languageQueryString()}`;
  if (location.pathname !== newPath) {
    history.pushState({ place }, '', newUrl);
  } else {
    history.replaceState({ place }, '', newUrl);
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

  if (posterMode) {
    if (e.key === 'Escape') { e.preventDefault(); exitPosterMode(); }
    return;
  }

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

// ─── Bird song playback ───────────────────────────────────────────────────────

function setSongPlayingUI(playing) {
  btnPlaySong.classList.toggle('playing', playing);
  btnPlaySong.setAttribute('aria-label', playing ? 'Pausar canto' : 'Reproducir canto');
  songIcon.innerHTML = playing ? '&#10074;&#10074;' : '&#9658;';
}

btnPlaySong.addEventListener('click', () => {
  if (birdAudio.paused) {
    birdAudio.play().catch(() => {}); // ignore AbortError from a rapid pause/bird change
  } else {
    birdAudio.pause();
  }
});

birdAudio.addEventListener('play', () => setSongPlayingUI(true));
birdAudio.addEventListener('pause', () => setSongPlayingUI(false));
birdAudio.addEventListener('ended', () => setSongPlayingUI(false));

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

chooserBtns.forEach(btn => {
  btn.addEventListener('click', () => enterPlace(btn.dataset.place));
});

primaryLanguageSelect.addEventListener('change', e => selectLanguage('primary', e.target.value));
secondaryLanguageSelect.addEventListener('change', e => selectLanguage('secondary', e.target.value));

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
                <img src="${imgSrc}" alt="${escHtml(speciesName(sp, primaryLanguage))}" class="${imgClass}" style="${imgStyle}">
              </div>
              <p class="print-bird-label">
                <span class="print-name-latin">${escHtml(sp.scientific_name)}</span>
                <span class="print-name-primary" lang="${primaryLanguage}">${escHtml(speciesName(sp, primaryLanguage))}</span>
                <span class="print-name-secondary" lang="${secondaryLanguage}">${escHtml(speciesName(sp, secondaryLanguage))}</span>
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

// ─── Poster mode (fullscreen bin-packed collage, random order, every bird at once) ──
// Reuses gridSpanClass/rotationDeg/escHtml/imageUrl from the print sheet above.
// A names on/off toggle turns it into a quiz: with names off, tapping a bird
// reveals (or re-hides) just that bird's name. Starts with names hidden.

let posterMode = false;
let posterShowNames = false;
let posterOrder = [];

function shuffleArray(arr) {
  const a = arr.slice();
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [a[i], a[j]] = [a[j], a[i]];
  }
  return a;
}

function renderPoster() {
  posterGrid.innerHTML = posterOrder.map(key => {
    const sp = catalog.species[key];
    const hasCutout = Boolean(sp.poster_cutout);
    const imgSrc = hasCutout ? imageUrl(sp, sp.poster_cutout) : imageUrl(sp, sp.poster_image);
    const imgClass = hasCutout ? 'poster-bird-img poster-bird-img-cutout' : 'poster-bird-img';
    const imgStyle = hasCutout ? `transform: rotate(${rotationDeg(sp)}deg);` : '';
    return `
      <div class="poster-bird-cell ${gridSpanClass(sp)}">
        <div class="poster-bird-img-wrap">
          <img src="${imgSrc}" alt="${escHtml(speciesName(sp, primaryLanguage))}" class="${imgClass}" style="${imgStyle}">
        </div>
        <p class="poster-bird-label">
          <span class="poster-name-latin">${escHtml(sp.scientific_name)}</span>
          <span class="poster-name-primary" lang="${primaryLanguage}">${escHtml(speciesName(sp, primaryLanguage))}</span>
          <span class="poster-name-secondary" lang="${secondaryLanguage}">${escHtml(speciesName(sp, secondaryLanguage))}</span>
        </p>
      </div>
    `;
  }).join('');
}

function shufflePoster() {
  posterOrder = shuffleArray(getSpeciesList());
  renderPoster();
}

function updatePosterToggleNamesUI() {
  btnPosterToggleNames.setAttribute('aria-pressed', String(posterShowNames));
  btnPosterToggleNames.classList.toggle('quiz-active', !posterShowNames);
  posterToggleIcon.textContent = posterShowNames ? '\u{1F441}' : '\u{1F576}';
  posterToggleLabel.textContent = posterShowNames ? 'Nombres' : 'Adivinar';
}

function togglePosterNames() {
  posterShowNames = !posterShowNames;
  posterGrid.classList.toggle('show-names', posterShowNames);
  if (!posterShowNames) {
    // Turning quiz mode on starts a fresh round: hide every name again.
    posterGrid.querySelectorAll('.poster-bird-cell.revealed').forEach(cell => {
      cell.classList.remove('revealed');
    });
  }
  updatePosterToggleNamesUI();
}

function enterPosterMode() {
  if (!catalog) return;
  birdAudio.pause();
  posterMode = true;
  posterPlaceName.textContent = catalog.places[currentPlace].name_es;
  posterGrid.classList.toggle('show-names', posterShowNames);
  updatePosterToggleNamesUI();
  shufflePoster();
  stageEl.hidden = true;
  posterView.hidden = false;
  closeMenu();
}

function exitPosterMode() {
  posterMode = false;
  posterView.hidden = true;
  stageEl.hidden = false;
}

posterGrid.addEventListener('click', e => {
  if (posterShowNames) return;
  const cell = e.target.closest('.poster-bird-cell');
  if (!cell) return;
  cell.classList.toggle('revealed');
});

btnPosterMode.addEventListener('click', enterPosterMode);
btnPosterBack.addEventListener('click', exitPosterMode);
btnPosterShuffle.addEventListener('click', shufflePoster);
btnPosterToggleNames.addEventListener('click', togglePosterNames);

// ─── Browser back/forward ─────────────────────────────────────────────────────

window.addEventListener('popstate', e => {
  if (!catalog) return;
  readLanguagesFromUrl();
  syncLanguageControls();
  const place = (e.state && e.state.place) || detectPlaceFromUrl();
  if (place && catalog.places[place]) {
    currentPlace = place;
    currentBirdIndex = 0;
    currentImageIndex = 0;
    placeChooser.hidden = true;
    stageEl.hidden = false;
    render();
    buildPrintSheet();
  } else {
    showChooser();
  }
});

// ─── Init ─────────────────────────────────────────────────────────────────────

function init() {
  try {
    const dataEl = document.getElementById('catalog-data');
    if (!dataEl) throw new Error('catalog-data script tag not found — run scripts/bake.sh');
    catalog = JSON.parse(dataEl.textContent);
    readLanguagesFromUrl();
    syncLanguageControls();
    replaceLanguageQuery();

    // Only skip the chooser if the URL already names a place (a shared link,
    // a bookmark, or coming back via browser history).
    const placeFromUrl = detectPlaceFromUrl();
    if (placeFromUrl && catalog.places[placeFromUrl]) {
      enterPlace(placeFromUrl);
    } else {
      showChooser();
    }
  } catch (err) {
    console.error('Failed to load catalog:', err);
    placeChooser.hidden = true;
    stageEl.hidden = false;
    namePrimary.textContent = 'Error al cargar el catálogo';
    nameLatin.textContent = err.message;
  }
}

init();
