# Guía Familiar de Aves — Documento de Diseño

## 1. Visión general

Un único catálogo de datos alimenta dos productos:

- **Aplicación web móvil** estática, publicada en GitHub Pages.
- **Guía imprimible A4** por lugar, para plastificar, generada por el propio navegador (sin PDF pregenerado).

## 2. Arquitectura de datos

### 2.1 Estructura de directorios

```
birds/
  <Genus species>/          # nombre científico, p. ej. "Passer domesticus"
    metadata.json
    principal.jpg           # imagen principal (obligatoria)
    [otras imágenes].jpg
places.json                 # tres lugares con lista ordenada de 20 especies cada uno
```

### 2.2 `metadata.json` — esquema

```json
{
  "scientific_name": "Passer domesticus",
  "name_es": "Gorrión común",
  "name_fr": "Moineau domestique",
  "poster_image": "principal.jpg",
  "images": [
    {
      "file": "principal.jpg",
      "alt_es": "Gorrión común macho, vista lateral",
      "sex_age": "male",
      "author": "Autor Apellido",
      "source_url": "https://commons.wikimedia.org/wiki/...",
      "license": "CC BY-SA 4.0",
      "license_url": "https://creativecommons.org/licenses/by-sa/4.0/"
    }
  ],
  "references": [
    "https://www.seo.org/ave/gorrion-comun/",
    "https://www.xeno-canto.org/species/Passer-domesticus"
  ]
}
```

Campos obligatorios: `scientific_name`, `name_es`, `name_fr`, `poster_image`, `images` (≥1), y para cada imagen: `file`, `author`, `source_url`, `license`, `license_url`.

### 2.3 `places.json` — esquema

```json
{
  "places": {
    "alicante": {
      "name_es": "Alicante",
      "name_fr": "Alicante",
      "species": ["Passer domesticus", "Turdus merula", "… hasta 20"]
    },
    "ourense": { "name_es": "Ourense", "name_fr": "Ourense", "species": ["…"] },
    "bruselas": { "name_es": "Bruselas", "name_fr": "Bruxelles", "species": ["…"] }
  }
}
```

`species` es una lista ordenada que referencia carpetas en `birds/`.

### 2.4 Identidad de especie

- Cada carpeta es identificada de forma estable por su nombre científico (genus + epithet).
- Las especies compartidas entre lugares reutilizan exactamente la misma carpeta.
- No existe ningún catálogo editable adicional fuera de `birds/` y `places.json`.

## 3. Selección de aves por lugar

### Criterio editorial

No existen rankings estadísticos comparables para los tres lugares con la granularidad de ciudad. Se adopta un criterio editorial explícito:

1. **Fuentes primarias consultadas:**
   - Alicante: Alicante Turismo (La Ereta, Serra Grossa), SEO/BirdLife aves España.
   - Ourense: Universidade de Vigo — catálogo campus / riberas del Miño, Turismo de Galicia.
   - Bruselas: Natagora, Aves Bruxelles (Bruxelles Environnement).
2. **Criterios de selección:**
   - Probabilidad de avistamiento en paseo familiar (urbano + periurbano).
   - Presencia durante la mayor parte del año (residentes y estivales frecuentes).
   - Tamaño o comportamiento conspicuo (fácil de ver y reconocer).
3. **Documentación:** cada especie incluye en `metadata.json` una lista de referencias usadas para verificar presencia local y nombres.
4. **Estacionalidad:** documentada en el campo `references` y anotada en el plan de implementación; no se añade a la ficha visible de la web.

### Listas de especies (ver `places.json` para el orden definitivo)

**Alicante** (calles, parques, costa, La Ereta, Serra Grossa):
Gorrión común, Mirlo común, Paloma bravía/urbana, Tórtola turca, Gaviota patiamarilla, Gaviota de Audouin (costa), Estornino negro, Vencejo común, Golondrina común, Avión común, Verdecillo, Jilguero, Carbonero común, Herrerillo común, Curruca capirotada, Petirrojo, Cernícalo vulgar, Lechuza común, Colirrojo tizón, Gorrión molinero.

**Ourense** (calles, parques, riberas del Miño):
Mirlo común, Gorrión común, Paloma bravía/urbana, Tórtola turca, Petirrojo, Carbonero común, Herrerillo común, Curruca capirotada, Jilguero, Verdecillo, Pinzón vulgar, Agateador común, Trepador azul, Martín pescador, Garza real, Cormorán grande, Pato real, Lavandera blanca, Lavandera cascadeña, Vencejo común.

**Bruselas** (calles, jardines, parques):
Mirlo común, Gorrión común, Paloma bravía/urbana, Herrerillo común, Carbonero común, Petirrojo, Curruca capirotada, Zorzal charlo, Pinzón vulgar, Tórtola turca, Chochín, Agateador común, Trepador azul, Estornino pinto, Pato real, Garza real, Gaviota reidora, Vencejo común, Golondrina común, Colirrojo real.

## 4. Imágenes

### 4.1 Fuentes permitidas

- Wikimedia Commons (licencias CC BY, CC BY-SA, CC0, dominio público).
- Otras fuentes con licencia explícita compatible con redistribución en repositorio público y PDF.
- No se usan imágenes generadas por IA.

### 4.2 Criterios de calidad

- Ave reconocible, buena luz, sin recortes en partes diagnósticas (pico, patas, cola, alas).
- Resolución mínima: 800 px en el lado mayor para impresión A4 aceptable.
- Preferencia por fotografías de campo sobre ilustraciones.

### 4.3 Nomenclatura de archivos

- `principal.jpg` — imagen para el póster (obligatoria, siempre macho adulto o plumaje más característico).
- Nombres descriptivos para las demás: `hembra.jpg`, `juvenil.jpg`, `vuelo.jpg`, etc.
- `poster-cutout.png` (opcional) — `principal.jpg` con el fondo quitado (`scripts/cutout.py`), usada por la guía impresa para que el ave flote sobre la página en vez de ir en una caja. No lleva atribución propia: es una versión derivada de `principal.jpg`, mismos créditos.

### 4.4 Atribución

- Campos `author`, `source_url`, `license`, `license_url` en cada entrada de `images`.
- Solo en la web (menú → Créditos). La guía impresa no lleva créditos, por decisión explícita — es un objeto físico para una niña de 7 años, no un documento legal; la atribución completa vive en la web, siempre accesible.

## 5. Aplicación web

### 5.1 Tecnología

- HTML + CSS + JavaScript vanilla, sin frameworks ni bundlers.
- Completamente estática, funciona en GitHub Pages sin backend — y también abriendo `index.html` directamente desde disco (`file://`), sin servidor.
- `index.template.html` es la plantilla editable; `scripts/bake.sh` combina `places.json` + `birds/*/metadata.json` y los inyecta como JSON inline (`<script type="application/json" id="catalog-data">`) en `index.html`. `src/app.js` lo lee con `JSON.parse`, sin `fetch` — así no choca con las restricciones de CORS de `file://`.
- `index.html` es un artefacto generado (no se edita a mano); `index.template.html` sí.

### 5.2 Estructura de archivos web

```
index.template.html   # plantilla — editar aquí
index.html            # generado por scripts/bake.sh
src/
  app.js
  style.css
```

### 5.3 Interfaz

**Pantalla principal (ficha):**
- Pantalla completa. Imagen al fondo o en zona amplia.
- Superpuesto: nombre en latín (negrita), nombre en español, nombre en francés.
- Controles discretos de navegación (flechas o puntos).

**Menú lateral/modal:**
- Selector de lugar (Alicante / Ourense / Bruselas).
- Instrucciones de gestos.
- Botón "Imprimir esta guía".
- Créditos.

**URLs por lugar:**
- `/pajaros/alicante/`
- `/pajaros/ourense/`
- `/pajaros/bruselas/`

Implementadas con `location.pathname` (sin hash) usando el prefijo de repo GitHub Pages. El `404.html` redirige al `index.html` con el path preservado (técnica estándar para GitHub Pages SPA). Bajo `file://` este enrutado se desactiva (no tiene sentido ahí): siempre arranca en Alicante y no toca `history.pushState`.

### 5.4 Gestos y controles

| Acción | Gesto | Teclado | Botón |
|--------|-------|---------|-------|
| Siguiente pájaro | Deslizar ← | → | ▶ |
| Pájaro anterior | Deslizar → | ← | ◀ |
| Siguiente imagen | Deslizar ↑ | ↑ | ▲ |
| Imagen anterior | Deslizar ↓ | ↓ | ▼ |

**Reglas de gestos:**
- Umbral mínimo de desplazamiento: 50 px.
- Ratio mínimo eje dominante/secundario: 2:1 (cancelar gestos diagonales).
- Un gesto activa un solo eje.
- Cambiar de pájaro resetea la imagen al `poster_image`.
- Cambiar de lugar va al primer pájaro.
- Navegación circular.
- Si el pájaro tiene una sola imagen, los controles verticales no se muestran.

### 5.5 Accesibilidad

- `alt` en todas las imágenes.
- Foco visible en todos los controles.
- Soporte `prefers-reduced-motion`: deshabilitar animaciones de transición.
- ARIA labels en botones de navegación.

### 5.6 Rendimiento

- Precarga las imágenes del pájaro anterior y siguiente.
- Al cambiar de ficha, se cancela cualquier carga pendiente de la ficha anterior (limpieza de `src` antes de asignar el nuevo).
- El catálogo entero va horneado en `index.html`; las imágenes se cargan bajo demanda.

## 6. Guías A4 para imprimir

### 6.1 Generación

- `src/app.js` construye un `#print-sheet` oculto a partir del catálogo horneado en `index.html`, con el mismo HTML/CSS que se ve en pantalla.
- Una hoja de estilos `@media print` en `src/style.css` define la maquetación A4 y oculta la interfaz normal (tarjeta, menú) al imprimir.
- El botón "Imprimir esta guía" del menú llama a `window.print()`; el usuario elige "Guardar como PDF" o imprime directamente desde el navegador. No hace falta Node, Chrome headless ni ningún paso de CI para generar el PDF.

### 6.2 Maquetación

- Formato A4 vertical (210 × 297 mm), vía `@page { size: A4 }`.
- 4 láminas por lugar, 5 aves por lámina (`page-break-after` entre láminas). Sin página de créditos — ver §4.4.
- Cada fila de ave: imagen a la izquierda (≈ 40 % del ancho), nombres a la derecha (español y francés al mismo tamaño, latín pequeño debajo).
- Si existe `poster-cutout.png` para la especie, se usa esa versión (sin fondo, flotando directamente sobre la página); si no, la foto normal en una caja con esquinas redondeadas. Estilo tipo "póster de aves de jardín" en vez de fila de fichas.
- Cabecera: lugar + número de lámina.
- Fondo blanco; tipografía grande, legible para una niña de 7 años.
- Sin párrafos descriptivos.

### 6.3 Proceso de validación visual

1. Abrir la web, seleccionar un lugar y usar la vista previa de impresión del navegador (Ctrl/Cmd+P).
2. Revisar: texto no cortado, imagen proporcionada, márgenes adecuados, saltos de página en el sitio correcto.
3. Repetir para los otros dos lugares.
4. Revisión final: 20 especies, sin duplicados, nombres coinciden con la web.

## 7. Validación y CI

### 7.1 Script de validación (`scripts/validate.sh`, bash + jq)

Comprobaciones:
- Exactamente 20 especies distintas por lugar en `places.json`.
- Cada especie referenciada tiene su carpeta en `birds/`.
- Cada carpeta tiene `metadata.json` con todos los campos obligatorios.
- El nombre científico en `metadata.json` coincide con el nombre de la carpeta.
- Cada imagen listada existe como archivo.
- `poster_image` existe dentro de `images[].file`.
- Todas las imágenes tienen `author`, `source_url`, `license`, `license_url`.
- No hay especies duplicadas dentro de un lugar.

### 7.2 GitHub Actions (`.github/workflows/ci.yml`)

- `on: push, pull_request`
- Jobs: validate → build-web (hornea `index.html`) → deploy (Pages, solo en `main`).

## 8. Decisiones de diseño registradas

| Decisión | Razón |
|----------|-------|
| Vanilla JS sin framework | Simplicidad, sin dependencias de npm para la web; evita rot de dependencias |
| Impresión vía `@media print` (sin Puppeteer) | Reutiliza el mismo HTML/CSS/catálogo de la web; cero dependencias de Node en runtime; el usuario controla el PDF final con el diálogo de impresión de su navegador |
| Catálogo horneado en `index.html` (sin `fetch`) | Cero dependencias de build para *ver* la web: abre `index.html` desde disco y funciona, sin servidor. `fetch()` de JSON local está bloqueado por CORS bajo `file://`; datos inline lo evitan del todo |
| Scripts de mantenimiento en bash+jq / Python stdlib (sin Node) | `python3` viene preinstalado en macOS/Linux; `jq` es una única dependencia externa minúscula (viene preinstalada en los runners de GitHub Actions). Cero `npm install`, cero `node_modules`, cero lockfile |
| Nombres científicos como IDs | Estables, unívocos, no requieren UUID artificial |
| 404.html redirect SPA | Técnica estándar y documentada para GitHub Pages sin servidor |
| CC BY / CC BY-SA / CC0 | Licencias compatibles con repositorio público, web y PDF |
| Cutouts (`rembg`) solo para el póster, generados aparte | La web sigue usando las fotos normales; solo la guía impresa necesita el ave "flotando" sin caja. Mantiene ese único paso pesado (modelo de ~1GB) fuera del resto del toolchain |
| Sin créditos en el PDF impreso | Es un objeto físico para un paseo familiar, no un documento de atribución; los créditos completos siguen siempre disponibles en la web |
