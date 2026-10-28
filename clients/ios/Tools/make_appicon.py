#!/usr/bin/env python3
"""Generate the ycc iOS app icon (clients/ios/App/Assets.xcassets/AppIcon.appiconset).

The icon is a terminal prompt, the same mark as the web favicon: a
square-capped, miter-joined chevron in off-white (#E3E5DF) and a square cursor
block in acid chartreuse (#C6F432, the accent fill) on a flat near-black
(#0B0C0B) tile. No gradient, no glow. Geometry is on the favicon's 1024 grid:

    chevron  M270 300 L546 486 L270 672, stroke 104, square caps, miter join
    cursor   x=600 y=620 w=220 h=108

iOS applies the corner mask itself, so the tile is a full square here.
Regenerate with:

    python3 clients/ios/Tools/make_appicon.py

Requires Pillow. The rendered PNG is committed, so this script only needs to run
when the mark changes.
"""

from __future__ import annotations

import math
import pathlib

from PIL import Image, ImageDraw

SIZE = 1024
# Render oversampled and downscale for clean anti-aliased diagonals.
SCALE = 4
OUT = (
    pathlib.Path(__file__).resolve().parents[1]
    / "App/Assets.xcassets/AppIcon.appiconset/icon-1024.png"
)

BACKGROUND = (0x0B, 0x0C, 0x0B)  # #0B0C0B
CHEVRON = (0xE3, 0xE5, 0xDF)     # #E3E5DF
CURSOR = (0xC6, 0xF4, 0x32)      # #C6F432 — Theme.accentFill

CHEVRON_POINTS = [(270, 300), (546, 486), (270, 672)]
STROKE = 104
CURSOR_RECT = (600, 620, 220, 108)  # x, y, w, h


Point = tuple[float, float]


def _unit(dx: float, dy: float) -> Point:
    length = math.hypot(dx, dy)
    return dx / length, dy / length


def _intersect(p: Point, d: Point, q: Point, e: Point) -> Point:
    """Intersection of the lines p + t*d and q + s*e."""
    denom = d[0] * e[1] - d[1] * e[0]
    t = ((q[0] - p[0]) * e[1] - (q[1] - p[1]) * e[0]) / denom
    return p[0] + t * d[0], p[1] + t * d[1]


def stroke_outline(points: list[Point], width: float) -> list[Point]:
    """Outline polygon of a two-segment polyline with square caps and a miter
    join (what SVG draws for stroke-linecap=square, stroke-linejoin=miter)."""
    p0, apex, p2 = points
    h = width / 2
    d1 = _unit(apex[0] - p0[0], apex[1] - p0[1])
    d2 = _unit(p2[0] - apex[0], p2[1] - apex[1])
    n1 = (-d1[1], d1[0])
    n2 = (-d2[1], d2[0])

    def side(sign: float) -> tuple[Point, Point, Point]:
        start = (p0[0] - d1[0] * h + sign * n1[0] * h, p0[1] - d1[1] * h + sign * n1[1] * h)
        miter = _intersect(
            (p0[0] + sign * n1[0] * h, p0[1] + sign * n1[1] * h), d1,
            (apex[0] + sign * n2[0] * h, apex[1] + sign * n2[1] * h), d2,
        )
        end = (p2[0] + d2[0] * h + sign * n2[0] * h, p2[1] + d2[1] * h + sign * n2[1] * h)
        return start, miter, end

    l0, lm, l2 = side(1)
    r0, rm, r2 = side(-1)
    return [l0, lm, l2, r2, rm, r0]


def main() -> None:
    big = SIZE * SCALE
    image = Image.new("RGB", (big, big), BACKGROUND)
    draw = ImageDraw.Draw(image)

    scaled = [(x * SCALE, y * SCALE) for x, y in CHEVRON_POINTS]
    draw.polygon(stroke_outline(scaled, STROKE * SCALE), fill=CHEVRON)

    x, y, w, h = CURSOR_RECT
    draw.rectangle(
        [x * SCALE, y * SCALE, (x + w) * SCALE - 1, (y + h) * SCALE - 1], fill=CURSOR
    )

    image = image.resize((SIZE, SIZE), Image.LANCZOS)
    OUT.parent.mkdir(parents=True, exist_ok=True)
    image.save(OUT, "PNG")
    print(f"wrote {OUT} ({OUT.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
