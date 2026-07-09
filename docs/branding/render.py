#!/usr/bin/env python3
"""Rasterize the Hermod brand SVGs to PNG.

The SVGs are the canonical marks; this script derives the PNGs from them so the
geometry and palette live in exactly one place. It understands only the stroke
vocabulary the marks use (M x y V y2 / M x y L x2 y2, round caps) — it is a
renderer for this brand, not a general SVG engine.

Usage:  python3 docs/branding/render.py
Requires: Pillow
"""

import re
import xml.etree.ElementTree as ET
from pathlib import Path

from PIL import Image, ImageDraw

SVG_NS = "{http://www.w3.org/2000/svg}"
VIEWBOX = 64
SUPERSAMPLE = 4

HERE = Path(__file__).parent
OUTPUTS = {
    "hermod-mark.svg": "hermod-mark-256.png",
    "hermod-mark-dark.svg": "hermod-mark-256-dark.png",
}
SIZE = 256

SEGMENT = re.compile(
    r"M\s*([\d.]+)[\s,]+([\d.]+)\s*(?:V\s*([\d.]+)|L\s*([\d.]+)[\s,]+([\d.]+))"
)


def parse_strokes(svg_path: Path):
    """Yield (x1, y1, x2, y2, color, width) for each stroked path."""
    root = ET.parse(svg_path).getroot()
    group = root.find(f"{SVG_NS}g")
    width = float(group.get("stroke-width"))
    for path in group.findall(f"{SVG_NS}path"):
        m = SEGMENT.fullmatch(path.get("d").strip())
        if not m:
            raise ValueError(f"{svg_path.name}: unsupported path {path.get('d')!r}")
        x1, y1 = float(m.group(1)), float(m.group(2))
        if m.group(3) is not None:
            x2, y2 = x1, float(m.group(3))
        else:
            x2, y2 = float(m.group(4)), float(m.group(5))
        yield x1, y1, x2, y2, path.get("stroke"), width


def render(svg_path: Path, png_path: Path, size: int) -> None:
    canvas = size * SUPERSAMPLE
    scale = canvas / VIEWBOX
    img = Image.new("RGBA", (canvas, canvas), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    for x1, y1, x2, y2, color, width in parse_strokes(svg_path):
        w = width * scale
        p1 = (x1 * scale, y1 * scale)
        p2 = (x2 * scale, y2 * scale)
        draw.line([p1, p2], fill=color, width=round(w))
        for cx, cy in (p1, p2):  # round caps
            r = w / 2
            draw.ellipse([cx - r, cy - r, cx + r, cy + r], fill=color)
    img.resize((size, size), Image.LANCZOS).save(png_path)
    print(f"{svg_path.name} -> {png_path.name} ({size}x{size})")


if __name__ == "__main__":
    for svg_name, png_name in OUTPUTS.items():
        render(HERE / svg_name, HERE / png_name, SIZE)
