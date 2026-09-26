#!/usr/bin/env node
/**
 * fetch-images.js — Download a real, freely-licensed photo per species from
 * Wikimedia, and fill in the attribution fields in metadata.json.
 *
 * Strategy: use the photo already chosen as the Wikipedia infobox image for
 * the species (curated, generally good quality) via the Wikipedia REST
 * pageimages API, then pull license/author from the Commons file's
 * imageinfo. Falls back to the first suitably-licensed photo in the
 * species' Commons category if no Wikipedia infobox image is available.
 *
 * Usage:
 *   node scripts/fetch-images.js                # all species in birds/
 *   node scripts/fetch-images.js "Turdus merula" # a single species
 */

const fs = require('fs');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const BIRDS_DIR = path.join(ROOT, 'birds');
const UA = 'PajarosFamilyGuide/1.0 (https://github.com/jmelis/pajaros; personal non-commercial family project)';
const MAX_WIDTH = 1600;
const EXTRA_WIDTH = 1200;
const EXTRA_IMAGES = 2; // additional photos per species, beyond principal.jpg
const ALLOWED_LICENSE = /^(cc0|cc[- ]by(-sa)?[- ]?[\d.]*|public domain|pd)/i;
const EXCLUDE_FILENAME = /(map|range|distribution|egg|nest|skeleton|anatomy|illustration|drawing|painting|sound|spectrogram|call\b|song\b|vocali|logo|stamp|coin|taxonomy|cladogram)/i;

async function getJson(url) {
  const res = await fetch(url, { headers: { 'User-Agent': UA } });
  if (!res.ok) throw new Error(`HTTP ${res.status} for ${url}`);
  return res.json();
}

function stripHtml(str) {
  return String(str || '')
    .replace(/<[^>]*>/g, '')
    .replace(/\s+/g, ' ')
    .trim();
}

async function commonsFileInfo(fileTitle, width = MAX_WIDTH) {
  const url = 'https://commons.wikimedia.org/w/api.php?' + new URLSearchParams({
    action: 'query',
    titles: `File:${fileTitle}`,
    prop: 'imageinfo',
    iiprop: 'url|extmetadata|size',
    iiurlwidth: String(width),
    format: 'json'
  });
  const data = await getJson(url);
  const pages = data.query && data.query.pages;
  if (!pages) return null;
  const page = Object.values(pages)[0];
  if (!page || page.missing || !page.imageinfo || !page.imageinfo[0]) return null;
  const info = page.imageinfo[0];
  const meta = info.extmetadata || {};

  const licenseShort = meta.LicenseShortName && meta.LicenseShortName.value;
  const licenseUrl = meta.LicenseUrl && meta.LicenseUrl.value;
  let artist = stripHtml(meta.Artist && meta.Artist.value) || 'Desconocido';

  if (!licenseShort || !ALLOWED_LICENSE.test(licenseShort.replace(/\s+/g, ' '))) {
    return null; // not a license we redistribute under
  }

  // Some Artist fields ramble on with license text / contact requests outside
  // the markup our stripHtml can see. Keep just the leading name in that case.
  if (artist.length > 100 || /@|https?:\/\//.test(artist)) {
    const lead = artist.split(/[.(]/)[0].trim();
    artist = lead && lead.length <= 100 ? lead : 'Desconocido';
  }

  return {
    title: fileTitle,
    downloadUrl: info.thumburl || info.url,
    width: info.thumbwidth || info.width,
    author: artist,
    license: licenseShort,
    license_url: licenseUrl || 'https://commons.wikimedia.org/wiki/Commons:Licensing',
    source_url: `https://commons.wikimedia.org/wiki/File:${encodeURIComponent(fileTitle).replace(/%20/g, '_')}`
  };
}

async function categoryFileTitles(scientificName) {
  const catUrl = 'https://commons.wikimedia.org/w/api.php?' + new URLSearchParams({
    action: 'query',
    list: 'categorymembers',
    cmtitle: `Category:${scientificName}`,
    cmtype: 'file',
    cmlimit: '50',
    format: 'json'
  });
  const data = await getJson(catUrl);
  const members = (data.query && data.query.categorymembers) || [];
  return members
    .map(m => m.title.replace(/^File:/, ''))
    .filter(title => /\.(jpe?g)$/i.test(title) && !EXCLUDE_FILENAME.test(title));
}

// Fetch up to `count` extra photos for a species, skipping `excludeTitle`.
async function extraImages(scientificName, excludeTitle, count) {
  const titles = await categoryFileTitles(scientificName);
  const picked = [];
  for (const title of titles) {
    if (picked.length >= count) break;
    if (title === excludeTitle) continue;
    const info = await commonsFileInfo(title, EXTRA_WIDTH);
    if (info) picked.push(info);
  }
  return picked;
}

async function wikipediaInfoboxFile(scientificName) {
  const url = 'https://en.wikipedia.org/w/api.php?' + new URLSearchParams({
    action: 'query',
    titles: scientificName,
    redirects: '1',
    prop: 'pageimages',
    piprop: 'name',
    format: 'json'
  });
  const data = await getJson(url);
  const pages = data.query && data.query.pages;
  if (!pages) return null;
  const page = Object.values(pages)[0];
  const name = page && page.pageimage;
  if (!name) return null;
  // pageimage comes back as "File_name.jpg" (underscores, no "File:" prefix)
  return name.replace(/_/g, ' ');
}

async function commonsCategoryFallback(scientificName) {
  const titles = await categoryFileTitles(scientificName);
  for (const title of titles) {
    const info = await commonsFileInfo(title);
    if (info) return info;
  }
  return null;
}

async function downloadTo(url, destPath) {
  const res = await fetch(url, { headers: { 'User-Agent': UA } });
  if (!res.ok) throw new Error(`HTTP ${res.status} downloading ${url}`);
  const buf = Buffer.from(await res.arrayBuffer());
  fs.writeFileSync(destPath, buf);
  return buf.length;
}

async function processSpecies(dirName) {
  const dir = path.join(BIRDS_DIR, dirName);
  const metaPath = path.join(dir, 'metadata.json');
  const meta = JSON.parse(fs.readFileSync(metaPath, 'utf8'));
  const scientificName = meta.scientific_name;

  console.log(`\n--- ${scientificName} ---`);

  let info = null;
  const infoboxFile = await wikipediaInfoboxFile(scientificName);
  if (infoboxFile) {
    info = await commonsFileInfo(infoboxFile);
  }
  if (!info) {
    console.log('  Sin imagen de Wikipedia válida, probando categoría de Commons...');
    info = await commonsCategoryFallback(scientificName);
  }
  if (!info) {
    console.error(`  ERROR: no se encontró ninguna imagen con licencia libre para ${scientificName}`);
    return false;
  }

  const destPath = path.join(dir, 'principal.jpg');
  const bytes = await downloadTo(info.downloadUrl, destPath);
  console.log(`  OK principal.jpg: ${Math.round(bytes / 1024)}KB — ${info.author} — ${info.license}`);

  const images = [{
    file: 'principal.jpg',
    alt_es: meta.name_es,
    author: info.author,
    source_url: info.source_url,
    license: info.license,
    license_url: info.license_url
  }];

  const extras = await extraImages(scientificName, info.title, EXTRA_IMAGES);
  let n = 2;
  for (const extra of extras) {
    const fname = `foto${n}.jpg`;
    const bytes2 = await downloadTo(extra.downloadUrl, path.join(dir, fname));
    console.log(`  OK ${fname}: ${Math.round(bytes2 / 1024)}KB — ${extra.author} — ${extra.license}`);
    images.push({
      file: fname,
      alt_es: `${meta.name_es} (vista adicional)`,
      author: extra.author,
      source_url: extra.source_url,
      license: extra.license,
      license_url: extra.license_url
    });
    n++;
  }

  // Remove any leftover image files from a previous run with more images than this one.
  for (const existing of fs.readdirSync(dir)) {
    if (/^foto\d+\.jpg$/.test(existing) && !images.some(img => img.file === existing)) {
      fs.unlinkSync(path.join(dir, existing));
    }
  }

  meta.poster_image = 'principal.jpg';
  meta.images = images;

  fs.writeFileSync(metaPath, JSON.stringify(meta, null, 2) + '\n');
  return true;
}

async function main() {
  const arg = process.argv[2];
  const dirs = arg ? [arg] : fs.readdirSync(BIRDS_DIR).filter(d =>
    fs.statSync(path.join(BIRDS_DIR, d)).isDirectory()
  );

  let ok = 0, fail = 0;
  for (const dir of dirs) {
    try {
      const success = await processSpecies(dir);
      if (success) ok++; else fail++;
    } catch (e) {
      console.error(`  ERROR (${dir}): ${e.message}`);
      fail++;
    }
    await new Promise(r => setTimeout(r, 250)); // be polite to the API
  }

  console.log(`\n=== Done: ${ok} ok, ${fail} failed (of ${dirs.length}) ===`);
  process.exit(fail > 0 ? 1 : 0);
}

main();
