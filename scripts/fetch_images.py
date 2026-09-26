#!/usr/bin/env python3
"""fetch_images.py — Download a real, freely-licensed photo per species from
Wikimedia, and fill in the attribution fields in metadata.json.

Strategy: use the photo already chosen as the Wikipedia infobox image for the
species (curated, generally good quality) via the Wikipedia pageimages API,
then pull license/author from the Commons file's imageinfo. Falls back to the
first suitably-licensed photo in the species' Commons category if no
Wikipedia infobox image is available.

Stdlib only, no pip install needed.

Usage:
  python3 scripts/fetch_images.py                # all species in birds/
  python3 scripts/fetch_images.py "Turdus merula" # a single species
"""

import json
import os
import re
import sys
import time
import urllib.parse
import urllib.request

ROOT = os.path.join(os.path.dirname(__file__), "..")
BIRDS_DIR = os.path.join(ROOT, "birds")
UA = "PajarosFamilyGuide/1.0 (https://github.com/jmelis/pajaros; personal non-commercial family project)"
MAX_WIDTH = 1600
EXTRA_WIDTH = 1200
EXTRA_IMAGES = 2  # additional photos per species, beyond principal.jpg
ALLOWED_LICENSE = re.compile(r"^(cc0|cc[- ]by(-sa)?[- ]?[\d.]*|public domain|pd)", re.I)
EXCLUDE_FILENAME = re.compile(
    r"(map|range|distribution|egg|nest|skeleton|anatomy|illustration|drawing|"
    r"painting|sound|spectrogram|call\b|song\b|vocali|logo|stamp|coin|taxonomy|cladogram)",
    re.I,
)


def get_json(url):
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    with urllib.request.urlopen(req) as res:
        return json.loads(res.read().decode("utf-8"))


def strip_html(s):
    return re.sub(r"\s+", " ", re.sub(r"<[^>]*>", "", s or "")).strip()


def commons_file_info(file_title, width=MAX_WIDTH):
    url = "https://commons.wikimedia.org/w/api.php?" + urllib.parse.urlencode({
        "action": "query",
        "titles": f"File:{file_title}",
        "prop": "imageinfo",
        "iiprop": "url|extmetadata|size",
        "iiurlwidth": str(width),
        "format": "json",
    })
    data = get_json(url)
    pages = data.get("query", {}).get("pages")
    if not pages:
        return None
    page = next(iter(pages.values()))
    if not page or "missing" in page or not page.get("imageinfo"):
        return None
    info = page["imageinfo"][0]
    meta = info.get("extmetadata", {})

    license_short = meta.get("LicenseShortName", {}).get("value")
    license_url = meta.get("LicenseUrl", {}).get("value")
    artist = strip_html(meta.get("Artist", {}).get("value")) or "Desconocido"

    if not license_short or not ALLOWED_LICENSE.match(re.sub(r"\s+", " ", license_short)):
        return None  # not a license we redistribute under

    # Some Artist fields ramble on with license text / contact requests outside
    # the markup strip_html can see. Keep just the leading name in that case.
    if len(artist) > 100 or re.search(r"@|https?://", artist):
        lead = re.split(r"[.(]", artist)[0].strip()
        artist = lead if lead and len(lead) <= 100 else "Desconocido"

    return {
        "title": file_title,
        "download_url": info.get("thumburl") or info.get("url"),
        "author": artist,
        "license": license_short,
        "license_url": license_url or "https://commons.wikimedia.org/wiki/Commons:Licensing",
        "source_url": "https://commons.wikimedia.org/wiki/File:"
        + urllib.parse.quote(file_title).replace("%20", "_"),
    }


def category_file_titles(scientific_name):
    url = "https://commons.wikimedia.org/w/api.php?" + urllib.parse.urlencode({
        "action": "query",
        "list": "categorymembers",
        "cmtitle": f"Category:{scientific_name}",
        "cmtype": "file",
        "cmlimit": "50",
        "format": "json",
    })
    data = get_json(url)
    members = data.get("query", {}).get("categorymembers", [])
    titles = [m["title"][len("File:"):] for m in members if m["title"].startswith("File:")]
    return [t for t in titles if re.search(r"\.(jpe?g)$", t, re.I) and not EXCLUDE_FILENAME.search(t)]


def extra_images(scientific_name, exclude_title, count):
    """Fetch up to `count` extra photos for a species, skipping `exclude_title`."""
    picked = []
    for title in category_file_titles(scientific_name):
        if len(picked) >= count:
            break
        if title == exclude_title:
            continue
        info = commons_file_info(title, EXTRA_WIDTH)
        if info:
            picked.append(info)
    return picked


def wikipedia_infobox_file(scientific_name):
    url = "https://en.wikipedia.org/w/api.php?" + urllib.parse.urlencode({
        "action": "query",
        "titles": scientific_name,
        "redirects": "1",
        "prop": "pageimages",
        "piprop": "name",
        "format": "json",
    })
    data = get_json(url)
    pages = data.get("query", {}).get("pages")
    if not pages:
        return None
    page = next(iter(pages.values()))
    name = page.get("pageimage") if page else None
    if not name:
        return None
    # pageimage comes back as "File_name.jpg" (underscores, no "File:" prefix)
    return name.replace("_", " ")


def commons_category_fallback(scientific_name):
    for title in category_file_titles(scientific_name):
        info = commons_file_info(title)
        if info:
            return info
    return None


def download_to(url, dest_path):
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    with urllib.request.urlopen(req) as res:
        data = res.read()
    with open(dest_path, "wb") as f:
        f.write(data)
    return len(data)


def process_species(dir_name):
    dir_path = os.path.join(BIRDS_DIR, dir_name)
    meta_path = os.path.join(dir_path, "metadata.json")
    with open(meta_path, encoding="utf-8") as f:
        meta = json.load(f)
    scientific_name = meta["scientific_name"]

    print(f"\n--- {scientific_name} ---")

    info = None
    infobox_file = wikipedia_infobox_file(scientific_name)
    if infobox_file:
        info = commons_file_info(infobox_file)
    if not info:
        print("  Sin imagen de Wikipedia válida, probando categoría de Commons...")
        info = commons_category_fallback(scientific_name)
    if not info:
        print(f"  ERROR: no se encontró ninguna imagen con licencia libre para {scientific_name}")
        return False

    dest_path = os.path.join(dir_path, "principal.jpg")
    n_bytes = download_to(info["download_url"], dest_path)
    print(f"  OK principal.jpg: {n_bytes // 1024}KB — {info['author']} — {info['license']}")

    images = [{
        "file": "principal.jpg",
        "alt_es": meta["name_es"],
        "author": info["author"],
        "source_url": info["source_url"],
        "license": info["license"],
        "license_url": info["license_url"],
    }]

    extras = extra_images(scientific_name, info["title"], EXTRA_IMAGES)
    n = 2
    for extra in extras:
        fname = f"foto{n}.jpg"
        n_bytes2 = download_to(extra["download_url"], os.path.join(dir_path, fname))
        print(f"  OK {fname}: {n_bytes2 // 1024}KB — {extra['author']} — {extra['license']}")
        images.append({
            "file": fname,
            "alt_es": f"{meta['name_es']} (vista adicional)",
            "author": extra["author"],
            "source_url": extra["source_url"],
            "license": extra["license"],
            "license_url": extra["license_url"],
        })
        n += 1

    # Remove any leftover image files from a previous run with more images than this one.
    kept = {img["file"] for img in images}
    for existing in os.listdir(dir_path):
        if re.match(r"^foto\d+\.jpg$", existing) and existing not in kept:
            os.unlink(os.path.join(dir_path, existing))

    meta["poster_image"] = "principal.jpg"
    meta["images"] = images

    with open(meta_path, "w", encoding="utf-8") as f:
        json.dump(meta, f, indent=2, ensure_ascii=False)
        f.write("\n")
    return True


def main():
    arg = sys.argv[1] if len(sys.argv) > 1 else None
    dirs = [arg] if arg else sorted(
        d for d in os.listdir(BIRDS_DIR) if os.path.isdir(os.path.join(BIRDS_DIR, d))
    )

    ok, fail = 0, 0
    for d in dirs:
        try:
            if process_species(d):
                ok += 1
            else:
                fail += 1
        except Exception as e:
            print(f"  ERROR ({d}): {e}")
            fail += 1
        time.sleep(0.25)  # be polite to the API

    print(f"\n=== Done: {ok} ok, {fail} failed (of {len(dirs)}) ===")
    sys.exit(1 if fail > 0 else 0)


if __name__ == "__main__":
    main()
