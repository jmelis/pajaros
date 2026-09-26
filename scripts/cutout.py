#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "pillow>=10,<13",
#   "rembg[cpu]>=2.0.69,<3",
# ]
# ///
"""cutout.py — Remove the background from each species' poster image, for the
printed poster (organic floating-bird layout instead of boxed photos).

Output: birds/<Genus species>/poster-cutout.png (RGBA, transparent background,
cropped to the bird's silhouette) plus a poster_cutout_aspect field written
into that species' metadata.json (used to size its cell in the print poster's
collage layout). scripts/bake.sh picks both up automatically if present.

Not part of the zero-dependency toolchain — this is a rare, one-off,
local-only maintenance step (never runs in CI, never ships to the site).
Dependencies are declared above and managed by uv's persistent cache:

  uv run scripts/cutout.py                 # all species
  uv run scripts/cutout.py --missing       # only species without a cutout
  uv run scripts/cutout.py "Turdus merula" # a single species

The first run downloads the background-removal model to ~/.rembg/models/.
Both the uv environment and model cache are reused by later runs.
"""

import io
import json
import os
import sys

from PIL import Image
from rembg import new_session, remove

ROOT = os.path.join(os.path.dirname(__file__), "..")
BIRDS_DIR = os.path.join(ROOT, "birds")
MAX_DIM = 1200    # cap output size — these are display cutouts, not archival masters
PAD_FRAC = 0.03   # small breathing room kept around the tight content bbox


def process_species(dir_name, session):
    dir_path = os.path.join(BIRDS_DIR, dir_name)
    src_path = os.path.join(dir_path, "principal.jpg")
    dest_path = os.path.join(dir_path, "poster-cutout.png")
    meta_path = os.path.join(dir_path, "metadata.json")

    if not os.path.isfile(src_path):
        print(f"  SKIP {dir_name}: no principal.jpg")
        return False

    with open(src_path, "rb") as f:
        src_bytes = f.read()

    out_bytes = remove(src_bytes, session=session)
    img = Image.open(io.BytesIO(out_bytes)).convert("RGBA")

    # Crop to the bird's actual silhouette, not the full photo's canvas — the
    # print layout sizes each cell from this aspect ratio, so empty transparent
    # margins around a small subject would otherwise waste the cell's space.
    bbox = img.split()[-1].getbbox()
    if bbox:
        x0, y0, x1, y1 = bbox
        pad_x, pad_y = round((x1 - x0) * PAD_FRAC), round((y1 - y0) * PAD_FRAC)
        img = img.crop((
            max(0, x0 - pad_x), max(0, y0 - pad_y),
            min(img.width, x1 + pad_x), min(img.height, y1 + pad_y),
        ))

    if max(img.size) > MAX_DIM:
        ratio = MAX_DIM / max(img.size)
        img = img.resize((round(img.width * ratio), round(img.height * ratio)), Image.LANCZOS)

    img.save(dest_path, optimize=True)

    with open(meta_path, encoding="utf-8") as f:
        meta = json.load(f)
    meta["poster_cutout_aspect"] = round(img.width / img.height, 4)
    with open(meta_path, "w", encoding="utf-8") as f:
        json.dump(meta, f, indent=2, ensure_ascii=False)
        f.write("\n")

    print(f"  OK {dir_name}: {img.size[0]}x{img.size[1]} -> {os.path.getsize(dest_path)//1024}KB")
    return True


def main():
    args = sys.argv[1:]
    all_dirs = sorted(
        d for d in os.listdir(BIRDS_DIR) if os.path.isdir(os.path.join(BIRDS_DIR, d))
    )
    if not args:
        dirs = all_dirs
    elif args == ["--missing"]:
        dirs = [
            d for d in all_dirs
            if not os.path.isfile(os.path.join(BIRDS_DIR, d, "poster-cutout.png"))
        ]
    elif len(args) == 1 and not args[0].startswith("-"):
        dirs = args
    else:
        print(f"Usage: {sys.argv[0]} [--missing | 'Genus species']", file=sys.stderr)
        sys.exit(2)

    ok = 0
    session = new_session() if dirs else None
    for d in dirs:
        print(f"--- {d} ---")
        if process_species(d, session):
            ok += 1

    print(f"\n=== Done: {ok}/{len(dirs)} ===")


if __name__ == "__main__":
    main()
