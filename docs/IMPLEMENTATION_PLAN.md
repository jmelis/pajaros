# Plan de Implementación

## Fases y entregables

---

### Fase 0 — Documentos de diseño ✅

**Entregables:** `docs/DESIGN.md`, `docs/IMPLEMENTATION_PLAN.md`  
**Criterio de aceptación:** documentos commiteados antes de cualquier código.

---

### Fase 1 — Catálogo de datos

**Entregables:**
- `places.yaml` con 3 lugares × 20 especies.
- `birds/<Genus species>/metadata.json` para las ~50 especies únicas.

**Criterio de aceptación:**
- `scripts/validate.js` pasa sin errores.
- Todos los campos obligatorios presentes.
- Nombres científicos verificados contra SEO/BirdLife y referencias francesas.

**Commits propuestos:**
1. `data: add places.yaml with 3 locations and 60 species entries`
2. `data: add metadata.json for alicante species`
3. `data: add metadata.json for ourense-only species`
4. `data: add metadata.json for bruselas-only species`

---

### Fase 2 — Imágenes

**Entregables:**
- Al menos 1 imagen por especie (`principal.jpg`).
- Imágenes adicionales donde estén disponibles con licencia compatible.
- Atribución completa en `metadata.json`.

**Criterio de aceptación:**
- Ninguna especie sin imagen principal.
- Todas las imágenes referenciadas existen en disco.
- Todas las imágenes tienen `author`, `source_url`, `license`, `license_url`.
- Revisión visual: ave reconocible, sin recortes diagnósticos graves.

**Commits propuestos:**
1. `images: add principal images for alicante species (batch 1)`
2. `images: add principal images for alicante species (batch 2)`
3. `images: add principal images for ourense-only species`
4. `images: add principal images for bruselas-only species`
5. `images: add supplementary images (female, juvenile, flight)`
6. `images: update metadata with full attribution`

**Fuentes:** Wikimedia Commons API (descargas directas con atribución completa).

---

### Fase 3 — Script de validación

**Entregables:** `scripts/validate.js`

**Criterio de aceptación:**
- Detecta y reporta todos los tipos de error descritos en DESIGN.md §7.1.
- Ejecutable con `node scripts/validate.js`.
- Sale con código 0 si todo es correcto, código 1 si hay errores.

**Commit:** `scripts: add validate.js for catalog integrity checks`

---

### Fase 4 — Generación de catalog.json

**Entregables:** `scripts/build-catalog.js` → `catalog.json`

**Criterio de aceptación:**
- Genera un JSON con todas las fichas en el formato esperado por la web.
- Contiene los campos: `scientific_name`, `name_es`, `name_fr`, `poster_image`, `images`, `places`.
- Ejecutable con `node scripts/build-catalog.js`.

**Commit:** `scripts: add build-catalog.js`

---

### Fase 5 — Aplicación web

**Entregables:** `index.html`, `src/app.js`, `src/style.css`, `404.html`

**Criterio de aceptación:**
- Navegación en dos ejes (especie e imagen) funciona en móvil y escritorio.
- Gestos cumplen umbrales: 50 px mínimo, ratio 2:1 para cancelar diagonales.
- Cambiar especie resetea imagen a `poster_image`.
- Cambiar lugar va al primer pájaro.
- URL `/pajaros/alicante/` carga directamente ese lugar.
- Funciona con prefijo de repositorio GitHub Pages.
- Accesibilidad: alt, foco visible, reduced-motion.
- Precarga imágenes vecinas; no muestra imagen antigua al cambiar ficha.

**Commits propuestos:**
1. `web: add index.html and base styles`
2. `web: add catalog loading and card rendering`
3. `web: add swipe gesture and keyboard navigation`
4. `web: add place selector menu and PDF links`
5. `web: add 404.html redirect for SPA routing`
6. `web: add image preloading and transition fix`

---

### Fase 6 — Generación de PDF

**Entregables:**
- `scripts/generate-pdf.js`
- `scripts/pdf-template.html` (o plantilla inline)
- `output/pdf/alicante.pdf`
- `output/pdf/ourense.pdf`
- `output/pdf/bruselas.pdf`

**Criterio de aceptación:**
- Cada PDF tiene 4 láminas (páginas 1–4) con 5 aves cada una + anexo de créditos.
- Imagen principal visible y no distorsionada.
- Nombres legibles (≥ 14 pt para español/francés, ≥ 12 pt para latín).
- 20 especies correctas en el orden de `places.yaml`.
- Revisión visual de página de prueba antes de generar los tres completos.

**Commits propuestos:**
1. `pdf: add generate-pdf.js and HTML template`
2. `pdf: add test page render and review`
3. `pdf: add final output for alicante, ourense, bruselas`

---

### Fase 7 — CI/CD

**Entregables:** `.github/workflows/ci.yml`

**Criterio de aceptación:**
- Job `validate` ejecuta `node scripts/validate.js`.
- Job `build` ejecuta `node scripts/build-catalog.js`.
- Job `deploy` publica en GitHub Pages en push a `main` (si permisos disponibles).
- Los jobs de validación y build no requieren secretos.

**Commit:** `ci: add GitHub Actions workflow for validate, build and deploy`

---

### Fase 8 — README y documentación

**Entregables:** `README.md` actualizado.

**Contenido mínimo:**
- Instalación de dependencias.
- Validación de datos.
- Ejecución local de la web.
- Generación de PDF.
- Compilación y publicación.
- Cómo añadir una especie, imagen o lugar.

**Commit:** `docs: update README with full project documentation`

---

## Dependencias de fases

```
Fase 0 → Fase 1 → Fase 2 → Fase 3 → Fase 4 → Fase 5
                                              → Fase 6
                                              → Fase 7
                              ↓
                           Fase 8
```

## Herramientas y dependencias de Node

| Paquete | Uso | Contexto |
|---------|-----|----------|
| `js-yaml` | Parsear `places.yaml` | scripts |
| `puppeteer` | Render HTML→PDF | scripts/generate-pdf |
| `sharp` (opcional) | Resize/optimize images | scripts |

No hay dependencias de Node para la web (vanilla JS).

## Riesgos y mitigaciones

| Riesgo | Mitigación |
|--------|------------|
| Wikimedia API rate limiting | Descarga en lotes pequeños con pausa |
| Imagen con licencia incorrecta | Verificación individual antes de descargar |
| PDF con texto cortado | Prueba de página única antes de generar todo |
| GitHub Pages sin permisos | Documentar el bloqueo exacto; la web funciona localmente |
| 403 en push | Usar engine-tools-report_progress; no usar `git push` directo |
