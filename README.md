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

Esto instala `js-yaml` (para parsear places.yaml) y `puppeteer` (para generar PDFs).

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

## Generar los PDF

```bash
npm run pdf
# node scripts/generate-pdf.js
```

Esto genera:
- `output/pdf/alicante.pdf`
- `output/pdf/ourense.pdf`
- `output/pdf/bruselas.pdf`

**Página de prueba** (solo primera lámina de Alicante):

```bash
node scripts/generate-pdf.js --test
# Genera output/pdf/alicante-test.pdf
```

**Cómo imprimir:**
- Imprime a **tamaño real (100%)**, sin ajustar a página.
- Las **páginas 1–4** son las láminas para plastificar (5 aves por lámina).
- La **última página** es el anexo de créditos (no hace falta plastificarla).

---

## Compilar y publicar

```bash
npm run all
# Ejecuta: validate → build → pdf
```

La publicación en GitHub Pages se realiza automáticamente a través de GitHub Actions cuando se hace push a `main` (si los permisos de la integración lo permiten).

Para activar GitHub Pages manualmente:
1. Ve a `Settings → Pages` del repositorio.
2. En *Source*, selecciona **GitHub Actions**.

---

## Añadir una especie

1. Crea `birds/<Genus species>/metadata.json` con todos los campos obligatorios.
2. Descarga al menos `principal.jpg` en esa carpeta.
3. Añade la especie a la lista del lugar en `places.yaml`.
4. Ejecuta `npm run validate` para verificar.
5. Ejecuta `npm run build` para regenerar `catalog.json`.

## Añadir una imagen a una especie existente

1. Copia la imagen en la carpeta de la especie (`birds/<Genus species>/`).
2. Añade una entrada en `images[]` en `metadata.json` con todos los campos de atribución.
3. Ejecuta `npm run validate`.

## Añadir un lugar

1. Añade una entrada en `places.yaml` con `name_es`, `name_fr` y una lista de 20 `species`.
2. Asegúrate de que cada especie tiene su carpeta en `birds/`.
3. Añade un botón en `index.html` (dentro de `#place-buttons`) y una ruta si es necesario.
4. Añade el enlace al PDF en el menú.
5. Ejecuta `npm run validate` y `npm run build`.

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
  generate-pdf.js       # genera los PDFs

output/
  pdf/
    alicante.pdf
    ourense.pdf
    bruselas.pdf

docs/
  DESIGN.md             # arquitectura y decisiones de diseño
  IMPLEMENTATION_PLAN.md
```

---

## Licencias de imágenes

Todas las imágenes del catálogo tienen licencia compatible con redistribución pública (CC BY, CC BY-SA, CC0 o dominio público). Los créditos individuales están en `metadata.json` de cada especie, en la página de créditos de la web (menú → Créditos) y en el anexo de cada PDF.
