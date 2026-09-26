# Guía Familiar de Aves

Catálogo de identificación de aves para paseos familiares en **Alicante**, **Ourense** y **Bruselas**, con 20 especies por lugar.

El mismo catálogo alimenta:
- **Aplicación web** estática para móvil (navega con gestos o flechas).
- **Guías PDF A4** para imprimir y plastificar.

**Web:** https://jmelis.github.io/pajaros/ (si está publicada)

---

## Instalación de dependencias

Requiere [Node.js](https://nodejs.org/) ≥ 18.

```bash
npm install
```

Esto instala `js-yaml`, para parsear `places.yaml`. La web en sí es HTML/CSS/JS vanilla, sin dependencias.

---

## Validar el catálogo

Comprueba la integridad de todos los datos (20 especies por lugar, metadata completo, imágenes presentes, atribución):

```bash
npm run validate
# o bien:
node scripts/validate.js
```

Sale con código 0 si todo es correcto, 1 si hay errores.

---

## Ejecutar la web localmente

1. Genera el catálogo:
   ```bash
   npm run build
   # node scripts/build-catalog.js → genera catalog.json
   ```
2. Sirve el directorio raíz con cualquier servidor estático, por ejemplo:
   ```bash
   npx serve . -l 8080
   # Abre http://localhost:8080
   ```

---

## Imprimir la guía en A4

No hace falta generar PDFs por separado: la propia web incluye una hoja imprimible.

1. Abre la web y selecciona el lugar que quieras imprimir.
2. Abre el menú (☰) → **Imprimir esta guía**.
3. Se abre el diálogo de impresión del navegador. Elige "Guardar como PDF" o imprime directamente.
4. Imprime a **tamaño real (100%)**, sin ajustar a página.

Genera automáticamente 4 láminas A4 (5 aves por lámina, con las 20 especies del lugar) más una página final de créditos.

---

## Compilar y publicar

```bash
npm run all
# Ejecuta: validate → build
```

La publicación en GitHub Pages se realiza automáticamente a través de GitHub Actions cuando se hace push a `main` (si los permisos de la integración lo permiten).

Para activar GitHub Pages manualmente:
1. Ve a `Settings → Pages` del repositorio.
2. En *Source*, selecciona **GitHub Actions**.

---

## Añadir una especie

1. Crea `birds/<Genus species>/metadata.json` (puedes copiar el de otra especie como plantilla) y su carpeta.
2. Añade la especie a la lista del lugar en `places.yaml`.
3. Ejecuta `node scripts/fetch-images.js "<Genus species>"` para descargar automáticamente `principal.jpg` y dos fotos adicionales desde Wikimedia, con su atribución.
4. Ejecuta `npm run validate` para verificar.
5. Ejecuta `npm run build` para regenerar `catalog.json`.

## Añadir una imagen a una especie existente

1. Copia la imagen en la carpeta de la especie (`birds/<Genus species>/`).
2. Añade una entrada en `images[]` en `metadata.json` con todos los campos de atribución.
3. Ejecuta `npm run validate`.

## Añadir un lugar

1. Añade una entrada en `places.yaml` con `name_es`, `name_fr` y una lista de 20 `species`.
2. Asegúrate de que cada especie tiene su carpeta en `birds/`.
3. Añade un botón en `index.html` (dentro de `#place-buttons`).
4. Ejecuta `npm run validate` y `npm run build`.

---

## Estructura del proyecto

```
birds/
  <Genus species>/
    metadata.json       # datos y atribución
    principal.jpg       # imagen principal (obligatoria)
    [otras].jpg         # vistas complementarias

places.yaml             # tres lugares con 20 especies cada uno
catalog.json            # generado por build-catalog.js, no editar

index.html              # aplicación web
src/
  app.js                # lógica de la web
  style.css             # estilos
404.html                # redirección SPA para GitHub Pages

scripts/
  validate.js           # comprobaciones de integridad
  build-catalog.js      # genera catalog.json
  fetch-images.js       # descarga fotos e info de licencia desde Wikimedia

docs/
  DESIGN.md             # arquitectura y decisiones de diseño
  IMPLEMENTATION_PLAN.md
```

---

## Licencias de imágenes

Todas las imágenes del catálogo tienen licencia compatible con redistribución pública (CC BY, CC BY-SA, CC0 o dominio público). Los créditos individuales están en `metadata.json` de cada especie, en la página de créditos de la web (menú → Créditos) y en la página final de cada guía impresa.
