#!/usr/bin/env node
/**
 * generate-pdf.js — Generate printable A4 guides for each place
 * Uses Puppeteer to render HTML → PDF
 * Output: output/pdf/{place}.pdf
 */

const fs = require('fs');
const path = require('path');
const puppeteer = require('puppeteer');
const yaml = require('js-yaml');

const ROOT = path.join(__dirname, '..');
const BIRDS_DIR = path.join(ROOT, 'birds');
const PLACES_FILE = path.join(ROOT, 'places.yaml');
const OUTPUT_DIR = path.join(ROOT, 'output', 'pdf');

const BIRDS_PER_PAGE = 5;
const PLACES = ['alicante', 'ourense', 'bruselas'];

// Create output dir
fs.mkdirSync(OUTPUT_DIR, { recursive: true });

// Load places
const placesData = yaml.load(fs.readFileSync(PLACES_FILE, 'utf8'));
const places = placesData.places;

// ─── HTML template ────────────────────────────────────────────────────────────

function fileToDataUrl(filePath) {
  if (!fs.existsSync(filePath)) return '';
  const data = fs.readFileSync(filePath);
  const ext = path.extname(filePath).toLowerCase().replace('.', '');
  const mime = ext === 'jpg' || ext === 'jpeg' ? 'image/jpeg' :
               ext === 'png' ? 'image/png' :
               ext === 'webp' ? 'image/webp' : 'image/jpeg';
  return `data:${mime};base64,${data.toString('base64')}`;
}

function buildPageHtml(placeName, pageNum, totalPages, birds) {
  const rows = birds.map(({ meta, imgDataUrl }) => `
    <div class="bird-row">
      <div class="bird-img-wrap">
        ${imgDataUrl
          ? `<img src="${imgDataUrl}" alt="${escHtml(meta.name_es)}" class="bird-img">`
          : `<div class="bird-img-placeholder">${escHtml(meta.name_es)}</div>`
        }
      </div>
      <div class="bird-names">
        <p class="name-es">${escHtml(meta.name_es)}</p>
        <p class="name-fr">${escHtml(meta.name_fr)}</p>
        <p class="name-latin">${escHtml(meta.scientific_name)}</p>
      </div>
    </div>
  `).join('');

  return `<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="UTF-8">
<style>
  @page {
    size: A4 portrait;
    margin: 15mm 12mm 12mm 12mm;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: 'Segoe UI', Arial, sans-serif;
    background: #fff;
    color: #111;
    width: 186mm;
  }
  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 2px solid #2c5f8a;
    padding-bottom: 4mm;
    margin-bottom: 5mm;
  }
  .place-name {
    font-size: 20pt;
    font-weight: 700;
    color: #2c5f8a;
  }
  .page-num {
    font-size: 11pt;
    color: #777;
  }
  .bird-row {
    display: flex;
    align-items: center;
    gap: 6mm;
    margin-bottom: 4mm;
    padding-bottom: 4mm;
    border-bottom: 1px solid #e8e8e8;
    height: 47mm;
  }
  .bird-row:last-child {
    border-bottom: none;
    margin-bottom: 0;
  }
  .bird-img-wrap {
    flex: 0 0 70mm;
    height: 45mm;
    overflow: hidden;
    border-radius: 3mm;
    background: #f5f5f5;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .bird-img {
    max-width: 70mm;
    max-height: 45mm;
    width: auto;
    height: auto;
    object-fit: contain;
    display: block;
  }
  .bird-img-placeholder {
    font-size: 9pt;
    color: #bbb;
    text-align: center;
    padding: 4mm;
  }
  .bird-names {
    flex: 1;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 2mm;
  }
  .name-es {
    font-size: 22pt;
    font-weight: 700;
    color: #111;
    line-height: 1.1;
  }
  .name-fr {
    font-size: 16pt;
    color: #444;
    font-style: italic;
  }
  .name-latin {
    font-size: 11pt;
    color: #888;
    font-style: italic;
  }
</style>
</head>
<body>
  <div class="page-header">
    <span class="place-name">Aves de ${escHtml(placeName)}</span>
    <span class="page-num">Lámina ${pageNum} de ${totalPages}</span>
  </div>
  ${rows}
</body>
</html>`;
}

function buildCreditsHtml(placeName, credits) {
  const rows = credits.map(c => `
    <tr>
      <td>${escHtml(c.name_es)}</td>
      <td><em>${escHtml(c.scientific_name)}</em></td>
      <td>${escHtml(c.author)}</td>
      <td>${escHtml(c.license)}</td>
      <td style="word-break:break-all;font-size:7pt"><a href="${escHtml(c.source_url)}">${escHtml(c.source_url)}</a></td>
    </tr>
  `).join('');

  return `<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="UTF-8">
<style>
  @page { size: A4 portrait; margin: 15mm 12mm; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: Arial, sans-serif; color: #111; width: 186mm; }
  h1 { font-size: 16pt; color: #2c5f8a; margin-bottom: 6mm; }
  p.note { font-size: 9pt; color: #777; margin-bottom: 5mm; }
  table { width: 100%; border-collapse: collapse; font-size: 8.5pt; }
  th { background: #2c5f8a; color: #fff; padding: 2mm 3mm; text-align: left; font-size: 8pt; }
  td { padding: 1.5mm 3mm; border-bottom: 1px solid #eee; vertical-align: top; }
  tr:nth-child(even) td { background: #f8f8f8; }
  a { color: #2c5f8a; }
</style>
</head>
<body>
  <h1>Créditos de imágenes — ${escHtml(placeName)}</h1>
  <p class="note">Esta página no es necesario plastificarla. Las imágenes están sujetas a las licencias indicadas.</p>
  <table>
    <thead>
      <tr><th>Nombre español</th><th>Nombre científico</th><th>Autor/a</th><th>Licencia</th><th>Fuente</th></tr>
    </thead>
    <tbody>${rows}</tbody>
  </table>
</body>
</html>`;
}

function escHtml(str) {
  return String(str || '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// ─── Main ─────────────────────────────────────────────────────────────────────

async function generatePdf(placeId, testOnly = false) {
  const place = places[placeId];
  if (!place) throw new Error(`Unknown place: ${placeId}`);

  const species = place.species;
  const placeName = place.name_es;

  // Load metadata for each species
  const birdData = species.map(sp => {
    const metaPath = path.join(BIRDS_DIR, sp, 'metadata.json');
    const meta = JSON.parse(fs.readFileSync(metaPath, 'utf8'));
    const imgPath = path.join(BIRDS_DIR, sp, meta.poster_image);
    const imgDataUrl = fileToDataUrl(imgPath);
    return { meta, imgDataUrl };
  });

  // Split into pages of BIRDS_PER_PAGE
  const pages = [];
  for (let i = 0; i < birdData.length; i += BIRDS_PER_PAGE) {
    pages.push(birdData.slice(i, i + BIRDS_PER_PAGE));
  }
  const totalLaminas = pages.length;

  // Credits data
  const credits = birdData.flatMap(({ meta }) =>
    (meta.images || []).map(img => ({
      name_es: meta.name_es,
      scientific_name: meta.scientific_name,
      author: img.author || '',
      license: img.license || '',
      source_url: img.source_url || ''
    }))
  );

  // Launch Puppeteer
  const browser = await puppeteer.launch({
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox']
  });

  try {
    const outputPath = path.join(OUTPUT_DIR, `${placeId}.pdf`);
    const pdfBuffers = [];

    const pagesToRender = testOnly ? [pages[0]] : pages;
    const totalForLabel = testOnly ? 1 : totalLaminas;

    for (let i = 0; i < pagesToRender.length; i++) {
      const page = await browser.newPage();
      const html = buildPageHtml(placeName, i + 1, totalLaminas, pagesToRender[i]);
      await page.setContent(html, { waitUntil: 'domcontentloaded' });

      const buf = await page.pdf({
        format: 'A4',
        printBackground: true,
        margin: { top: '15mm', right: '12mm', bottom: '12mm', left: '12mm' }
      });
      pdfBuffers.push(buf);
      await page.close();
      console.log(`  Rendered lámina ${i + 1}/${totalForLabel} for ${placeId}`);
    }

    // Add credits page (unless test-only)
    if (!testOnly) {
      const creditsPage = await browser.newPage();
      const creditsHtml = buildCreditsHtml(placeName, credits);
      await creditsPage.setContent(creditsHtml, { waitUntil: 'domcontentloaded' });
      const creditsBuf = await creditsPage.pdf({
        format: 'A4',
        printBackground: true,
        margin: { top: '15mm', right: '12mm', bottom: '12mm', left: '12mm' }
      });
      pdfBuffers.push(creditsBuf);
      await creditsPage.close();
      console.log(`  Rendered credits page for ${placeId}`);
    }

    // Merge PDFs using simple concatenation via PDFMerger or just save single pages
    // Since Puppeteer generates single-page PDFs, merge them
    // We use a simple approach: generate all pages in one go using multipage HTML
    // Re-render as single PDF
    const allPage = await browser.newPage();
    const allHtmlPages = [];

    const allPages = testOnly ? [pages[0]] : pages;
    for (let i = 0; i < allPages.length; i++) {
      allHtmlPages.push(buildPageHtml(placeName, i + 1, totalLaminas, allPages[i]));
    }

    if (!testOnly) {
      allHtmlPages.push(buildCreditsHtml(placeName, credits));
    }

    // Combined HTML with page breaks
    const combinedHtml = `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<style>
  @page { size: A4 portrait; margin: 0; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  .pdf-page {
    width: 210mm;
    height: 297mm;
    padding: 15mm 12mm 12mm 12mm;
    page-break-after: always;
    overflow: hidden;
  }
  .pdf-page:last-child { page-break-after: auto; }
</style>
</head>
<body>
${allHtmlPages.map(html => {
  // Extract body content from each page HTML
  const bodyMatch = html.match(/<body[^>]*>([\s\S]*)<\/body>/i);
  const styleMatch = html.match(/<style[^>]*>([\s\S]*?)<\/style>/gi);
  const styles = styleMatch ? styleMatch.map(s => s.replace(/<\/?style[^>]*>/gi, '')).join('\n') : '';
  const body = bodyMatch ? bodyMatch[1] : html;
  return `<style>${styles}</style><div class="pdf-page">${body}</div>`;
}).join('\n')}
</body>
</html>`;

    await allPage.setContent(combinedHtml, { waitUntil: 'domcontentloaded' });
    const finalBuf = await allPage.pdf({
      format: 'A4',
      printBackground: true,
      margin: { top: '0', right: '0', bottom: '0', left: '0' }
    });
    await allPage.close();

    if (testOnly) {
      const testPath = path.join(OUTPUT_DIR, `${placeId}-test.pdf`);
      fs.writeFileSync(testPath, finalBuf);
      console.log(`  Test page saved: ${testPath}`);
      return testPath;
    } else {
      fs.writeFileSync(outputPath, finalBuf);
      console.log(`  PDF saved: ${outputPath} (${Math.round(finalBuf.length / 1024)}KB)`);
      return outputPath;
    }
  } finally {
    await browser.close();
  }
}

async function main() {
  const args = process.argv.slice(2);
  const testOnly = args.includes('--test');
  const placeArg = args.find(a => PLACES.includes(a));

  const toGenerate = placeArg ? [placeArg] : PLACES;

  if (testOnly) {
    console.log(`=== TEST MODE: rendering first page of ${toGenerate[0]} ===`);
    await generatePdf(toGenerate[0], true);
    console.log('Test page generated. Review it before running full generation.');
  } else {
    for (const placeId of toGenerate) {
      console.log(`\n=== Generating PDF for ${placeId} ===`);
      await generatePdf(placeId, false);
    }
    console.log('\nAll PDFs generated.');
  }
}

main().catch(err => {
  console.error('PDF generation failed:', err);
  process.exit(1);
});
