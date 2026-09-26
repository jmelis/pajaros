#!/usr/bin/env python3
"""cutout.py — Remove the background from each species' poster image, for the
printed poster (organic floating-bird layout instead of boxed photos).

Output: birds/<Genus species>/poster-cutout.png (RGBA, transparent background).
scripts/bake.sh picks these up automatically if present.

Not part of the zero-dependency toolchain — this is a rare, one-off,
local-only maintenance step (never runs in CI, never ships to the site) that
needs rembg (ML background removal) and Pillow. Use a throwaway venv:

  python3 -m venv .venv
  .venv/bin/pip install rembg pillow
  .venv/bin/python scripts/cutout.py                # all species
  .venv/bin/python scripts/cutout.py "Turdus merula" # a single species

First run downloads a ~1GB model to ~/.rembg/ (cached after that).
"""

import io
import os
import sys

from PIL import Image
from rembg import remove

ROOT = os.path.join(os.path.dirname(__file__), "..")
BIRDS_DIR = os.path.join(ROOT, "birds")
MAX_DIM = 1200  # cap output size — these are display cutouts, not archival masters


def process_species(dir_name):
    dir_path = os.path.join(BIRDS_DIR, dir_name)
    src_path = os.path.join(dir_path, "principal.jpg")
    dest_path = os.path.join(dir_path, "poster-cutout.png")

    if not os.path.isfile(src_path):
        print(f"  SKIP {dir_name}: no principal.jpg")
        return False

    with open(src_path, "rb") as f:
        src_bytes = f.read()

    out_bytes = remove(src_bytes)
    img = Image.open(io.BytesIO(out_bytes)).convert("RGBA")

    if max(img.size) > MAX_DIM:
        ratio = MAX_DIM / max(img.size)
        img = img.resize((round(img.width * ratio), round(img.height * ratio)), Image.LANCZOS)

    img.save(dest_path, optimize=True)
    print(f"  OK {dir_name}: {img.size[0]}x{img.size[1]} -> {os.path.getsize(dest_path)//1024}KB")
    return True


def main():
    arg = sys.argv[1] if len(sys.argv) > 1 else None
    dirs = [arg] if arg else sorted(
        d for d in os.listdir(BIRDS_DIR) if os.path.isdir(os.path.join(BIRDS_DIR, d))
    )

    ok = 0
    for d in dirs:
        print(f"--- {d} ---")
        if process_species(d):
            ok += 1

    print(f"\n=== Done: {ok}/{len(dirs)} ===")


if __name__ == "__main__":
    main()
