# Guía Familiar de Aves

Catálogo de identificación de aves para paseos familiares en **Alicante**, **Ourense** y **Bruselas** (20 especies por lugar). Web estática, navegable con gestos, más una guía A4 imprimible generada por el propio navegador.

**Web:** https://jmelis.github.io/pajaros/

## Uso local

Es HTML/CSS/JS estático, sin build ni dependencias. `catalog.json` ya viene generado y comiteado, así que basta con servir el directorio con cualquier servidor estático:

```bash
python3 -m http.server 8080   # o: npx serve . -l 8080, o cualquier otro
```

Abre `http://localhost:8080`.

## Editar el catálogo

Esto sí requiere [Node.js](https://nodejs.org/) ≥ 18 (solo para estos scripts de mantenimiento — no hace falta `npm install`, no hay dependencias):

- **Especie nueva:** crea `birds/<Genus species>/metadata.json` (copia el de otra especie), añádela a `places.json`, y ejecuta `node scripts/fetch-images.js "<Genus species>"` para descargar 3 fotos con licencia libre y su atribución desde Wikimedia.
- **Imagen nueva en una especie existente:** copia el archivo a `birds/<Genus species>/` y añade su entrada en `images[]` de `metadata.json` (`file`, `author`, `source_url`, `license`, `license_url`).
- **Lugar nuevo:** añade la entrada en `places.json` (`name_es`, `name_fr`, 20 `species`) y un botón en `index.html` (`#place-buttons`).

Después de cualquier cambio: `node scripts/validate.js` (comprueba integridad y atribución) y `node scripts/build-catalog.js` (regenera `catalog.json`, que hay que comitear).

## Imprimir

Menú (☰) → **Imprimir esta guía** → diálogo de impresión del navegador (Guardar como PDF, o imprimir a tamaño real 100%). Genera 4 láminas A4 (5 aves cada una) más una página de créditos.

## Estructura

```
birds/<Genus species>/metadata.json, principal.jpg, foto2.jpg, foto3.jpg
places.json             # 3 lugares × 20 especies
catalog.json            # generado, no editar a mano

index.html, src/app.js, src/style.css, 404.html   # la web

scripts/
  validate.js           # integridad del catálogo
  build-catalog.js      # genera catalog.json
  fetch-images.js       # descarga fotos + atribución desde Wikimedia

docs/DESIGN.md, docs/IMPLEMENTATION_PLAN.md
```

CI (`.github/workflows/ci.yml`) valida, construye y publica en GitHub Pages en cada push a `main`.

## Licencias de imágenes

Todas con licencia libre (CC BY, CC BY-SA, CC0 o dominio público). Créditos en `metadata.json` de cada especie, en la web (menú → Créditos) y en la última página de cada guía impresa.
