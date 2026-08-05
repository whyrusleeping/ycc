#!/usr/bin/env python3
"""Generate the ycc iOS app icon (clients/ios/App/Assets.xcassets/AppIcon.appiconset).

The icon is a terminal prompt: a bold chevron plus a cursor bar on a dark
gradient, in the app's accent violet. Regenerate with:

    python3 clients/ios/Tools/make_appicon.py

Requires Pillow. The rendered PNG is committed, so this script only needs to run
when the mark changes.
"""

from __future__ import annotations

import pathlib

from PIL import Image, ImageDraw, ImageFilter

SIZE = 1024
OUT = (
    pathlib.Path(__file__).resolve().parents[1]
    / "App/Assets.xcassets/AppIcon.appiconset/icon-1024.png"
)

BG_TOP = (26, 29, 48)      # #1A1D30
BG_BOTTOM = (12, 13, 24)   # #0C0D18
VIOLET = (124, 107, 255)   # #7C6BFF — matches AccentColor
CYAN = (78, 205, 196)      # #4ECDC4


def gradient(size: int, top: tuple[int, int, int], bottom: tuple[int, int, int]) -> Image.Image:
    """A vertical linear gradient, drawn a row at a time."""
    image = Image.new("RGB", (size, size), top)
    draw = ImageDraw.Draw(image)
    for y in range(size):
        t = y / (size - 1)
        draw.line(
            [(0, y), (size, y)],
            fill=tuple(round(a + (b - a) * t) for a, b in zip(top, bottom)),
        )
    return image


def round_cap(draw: ImageDraw.ImageDraw, point: tuple[int, int], radius: int, fill) -> None:
    x, y = point
    draw.ellipse([x - radius, y - radius, x + radius, y + radius], fill=fill)


def main() -> None:
    image = gradient(SIZE, BG_TOP, BG_BOTTOM)

    # A soft violet bloom behind the mark so the icon has depth at small sizes.
    glow = Image.new("RGBA", (SIZE, SIZE), (0, 0, 0, 0))
    ImageDraw.Draw(glow).ellipse([180, 140, 860, 820], fill=VIOLET + (70,))
    glow = glow.filter(ImageFilter.GaussianBlur(140))
    image = Image.alpha_composite(image.convert("RGBA"), glow).convert("RGB")
    draw = ImageDraw.Draw(image)

    # A shell prompt: chevron then cursor bar, sharing a baseline.
    stroke = 88
    apex = (556, 486)
    top = (300, 316)
    bottom = (300, 656)
    draw.line([top, apex, bottom], fill=VIOLET, width=stroke, joint="curve")
    for point in (top, apex, bottom):
        round_cap(draw, point, stroke // 2, VIOLET)

    # The cursor bar, sitting on the chevron's baseline to its right.
    draw.rounded_rectangle([620, 662, 820, 746], radius=42, fill=CYAN)

    OUT.parent.mkdir(parents=True, exist_ok=True)
    image.save(OUT, "PNG")
    print(f"wrote {OUT} ({OUT.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
