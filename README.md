# Guía Familiar de Aves

Catálogo de identificación de aves para paseos familiares en **Alicante**, **Ourense** y **Bruselas** (20 especies por lugar). Web estática, navegable con gestos, más una guía A4 imprimible generada por el propio navegador.

**Web:** https://jmelis.github.io/pajaros/

## Uso local

`index.html` lleva el catálogo entero horneado dentro (nada de `fetch`), así que puedes abrirlo directamente desde el Finder/Explorador — **sin servidor, sin Node, sin nada**.

## Editar el catálogo

`index.html` es un artefacto generado — no lo edites a mano. La plantilla es `index.template.html`; `scripts/bake.sh` combina `places.json` + `birds/*/metadata.json` en él. Necesita [`jq`](https://jqlang.org/) (`brew install jq` / `apt install jq`), nada más.

- **Especie nueva:** crea `birds/<Genus species>/metadata.json` (copia el de otra especie), añádela a `places.json`, y ejecuta `python3 scripts/fetch_images.py "<Genus species>"` para descargar 3 fotos con licencia libre y su atribución desde Wikimedia (solo stdlib, sin `pip install`).
- **Imagen nueva en una especie existente:** copia el archivo a `birds/<Genus species>/` y añade su entrada en `images[]` de `metadata.json` (`file`, `author`, `source_url`, `license`, `license_url`).
- **Lugar nuevo:** añade la entrada en `places.json` (`name_es`, `name_fr`, 20 `species`) y un botón en `index.template.html` (`#place-buttons`).
- **Cambio de interfaz:** edita `index.template.html`, `src/app.js` o `src/style.css`, nunca `index.html` directamente.
- **Recorte para el póster:** `scripts/cutout.py` quita el fondo de `principal.jpg` y genera `birds/<Genus species>/poster-cutout.png` (ave flotando, sin caja) que usa la guía impresa si existe. Aparte del resto del proyecto — necesita un entorno con `rembg`+`Pillow` (descarga un modelo de ~1GB la primera vez):
  ```bash
  python3 -m venv .venv && .venv/bin/pip install rembg pillow
  .venv/bin/python scripts/cutout.py
  ```

Después de cualquier cambio:

```bash
bash scripts/validate.sh   # integridad y atribución del catálogo
bash scripts/bake.sh       # regenera index.html — comitéalo
```

## Imprimir

Menú (☰) → **Imprimir esta guía** → diálogo de impresión del navegador (Guardar como PDF, o imprimir a tamaño real 100%). Genera 4 láminas A4 (5 aves cada una). Las especies con `poster-cutout.png` aparecen sin caja, flotando sobre la página; el resto usa la foto normal.

## Estructura

```
birds/<Genus species>/metadata.json, principal.jpg, foto2.jpg, foto3.jpg
                            poster-cutout.png (opcional)  # ave sin fondo, para el póster
places.json                # 3 lugares × 20 especies

index.template.html        # plantilla — edítala a ella, no a index.html
index.html                 # generado por bake.sh, con el catálogo ya horneado dentro
src/app.js, src/style.css, 404.html

scripts/
  bake.sh                  # places.json + birds/ → index.html
  validate.sh              # integridad del catálogo (bash + jq)
  fetch_images.py          # descarga fotos + atribución desde Wikimedia (Python stdlib)
  cutout.py                # quita el fondo del póster (Python + rembg, aparte)

docs/DESIGN.md, docs/IMPLEMENTATION_PLAN.md
```

CI (`.github/workflows/ci.yml`) valida, hornea y publica en GitHub Pages en cada push a `main`.

## Licencias de imágenes

Todas con licencia libre (CC BY, CC BY-SA, CC0 o dominio público). Los datos de atribución (`author`, `source_url`, `license`, `license_url`) siguen en `metadata.json` de cada especie, pero no se muestran en la web ni en la guía impresa — proyecto familiar no comercial, sin intención de redistribución más allá de este uso.
