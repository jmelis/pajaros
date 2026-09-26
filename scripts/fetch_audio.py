#!/usr/bin/env python3
"""fetch_audio.py — Download a real, freely-licensed bird-song recording per
species from Wikimedia Commons, and fill in the `audio` field in metadata.json.

Strategy: Commons keeps a "Category:Audio files of <Scientific name>" for most
bird species, mostly bot-imported from Xeno-canto (Commons only accepts fully
free licenses, so anything CC BY-NC-* on Xeno-canto never makes it here — the
same license filter used by fetch_images.py still applies as a second check).
Prefer the "<Scientific name> - <Common name> XCnnnnn.mp3/ogg" recordings,
since that naming is consistent and each file is a single clean recording;
fall back to any other reasonably-sized audio file in the category otherwise.

Stdlib only, no pip install needed.

Usage:
  python3 scripts/fetch_audio.py                # all species in birds/
  python3 scripts/fetch_audio.py "Turdus merula" # a single species
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
MAX_SIZE_BYTES = 6_000_000  # skip long dawn-chorus-style recordings
ALLOWED_LICENSE = re.compile(r"^(cc0|cc[- ]by(-sa)?[- ]?[\d.]*|public domain|pd)", re.I)
ALLOWED_EXT = re.compile(r"\.(mp3|ogg|oga)$", re.I)
EXCLUDE_FILENAME = re.compile(
    r"(chorus|polyphonic|multiple species|mixed|background|soundscape|"
    r"ambient|survey|various|unidentified|unknown species|thunder|storm|"
    r"traffic|rain\b)",
    re.I,
)


def get_json(url):
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    with urllib.request.urlopen(req, timeout=20) as res:
        return json.loads(res.read().decode("utf-8"))


def strip_html(s):
    return re.sub(r"\s+", " ", re.sub(r"<[^>]*>", "", s or "")).strip()


def commons_file_info(file_title):
    url = "https://commons.wikimedia.org/w/api.php?" + urllib.parse.urlencode({
        "action": "query",
        "titles": f"File:{file_title}",
        "prop": "imageinfo",
        "iiprop": "url|extmetadata|size",
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

    if info.get("size", 0) > MAX_SIZE_BYTES:
        return None

    license_short = meta.get("LicenseShortName", {}).get("value")
    license_url = meta.get("LicenseUrl", {}).get("value")
    artist = strip_html(meta.get("Artist", {}).get("value")) or "Desconocido"

    if not license_short or not ALLOWED_LICENSE.match(re.sub(r"\s+", " ", license_short)):
        return None  # not a license we redistribute under

    if len(artist) > 100 or re.search(r"@|https?://", artist):
        lead = re.split(r"[.(]", artist)[0].strip()
        artist = lead if lead and len(lead) <= 100 else "Desconocido"

    return {
        "title": file_title,
        "download_url": info.get("url"),
        "author": artist,
        "license": license_short,
        "license_url": license_url or "https://commons.wikimedia.org/wiki/Commons:Licensing",
        "source_url": "https://commons.wikimedia.org/wiki/File:"
        + urllib.parse.quote(file_title).replace("%20", "_"),
    }


def category_file_titles(category):
    url = "https://commons.wikimedia.org/w/api.php?" + urllib.parse.urlencode({
        "action": "query",
        "list": "categorymembers",
        "cmtitle": f"Category:{category}",
        "cmtype": "file",
        "cmlimit": "200",
        "format": "json",
    })
    data = get_json(url)
    members = data.get("query", {}).get("categorymembers", [])
    titles = [m["title"][len("File:"):] for m in members if m["title"].startswith("File:")]
    return [t for t in titles if ALLOWED_EXT.search(t) and not EXCLUDE_FILENAME.search(t)]


def pick_recording(scientific_name):
    # Most species keep recordings in a dedicated "Audio files of X" subcategory
    # (mostly Xeno-canto bot imports); some file them directly under the plain
    # species category instead, so fall back to that if the first is empty.
    titles = category_file_titles(f"Audio files of {scientific_name}")
    if not titles:
        titles = category_file_titles(scientific_name)
    if not titles:
        return None

    xc_pattern = re.compile(re.escape(scientific_name) + r" - .*XC\d+", re.I)
    preferred = [t for t in titles if xc_pattern.search(t)]
    rest = [t for t in titles if t not in preferred]

    for title in preferred + rest:
        info = commons_file_info(title)
        if info:
            return info
    return None


def download_to(url, dest_path):
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    with urllib.request.urlopen(req, timeout=60) as res:
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

    info = pick_recording(scientific_name)
    if not info:
        print(f"  SIN AUDIO: no se encontró ninguna grabación con licencia libre para {scientific_name}")
        return None

    ext = os.path.splitext(info["title"])[1].lower()
    fname = f"song{ext}"
    dest_path = os.path.join(dir_path, fname)

    # Remove a leftover song.* from a previous run with a different extension.
    for existing in os.listdir(dir_path):
        if re.match(r"^song\.\w+$", existing) and existing != fname:
            os.unlink(os.path.join(dir_path, existing))

    n_bytes = download_to(info["download_url"], dest_path)
    print(f"  OK {fname}: {n_bytes // 1024}KB — {info['author']} — {info['license']}")

    meta["audio"] = {
        "file": fname,
        "alt_es": f"Canto de {meta['name_es']}",
        "author": info["author"],
        "source_url": info["source_url"],
        "license": info["license"],
        "license_url": info["license_url"],
    }

    with open(meta_path, "w", encoding="utf-8") as f:
        json.dump(meta, f, indent=2, ensure_ascii=False)
        f.write("\n")
    return True


def main():
    arg = sys.argv[1] if len(sys.argv) > 1 else None
    dirs = [arg] if arg else sorted(
        d for d in os.listdir(BIRDS_DIR) if os.path.isdir(os.path.join(BIRDS_DIR, d))
    )

    ok, none, fail = 0, 0, 0
    for d in dirs:
        try:
            result = process_species(d)
            if result:
                ok += 1
            else:
                none += 1
        except Exception as e:
            print(f"  ERROR ({d}): {e}")
            fail += 1
        time.sleep(0.25)  # be polite to the API

    print(f"\n=== Done: {ok} con audio, {none} sin audio disponible, {fail} con error (de {len(dirs)}) ===")
    sys.exit(1 if fail > 0 else 0)


if __name__ == "__main__":
    main()
