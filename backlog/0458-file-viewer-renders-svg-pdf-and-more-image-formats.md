---
id: "0458"
title: File viewer renders SVG, PDF, and more image formats (web + iOS)
status: in_review
priority: 3
created: "2026-10-10"
updated: "2026-10-10"
depends_on: []
spec_refs: []
---

## Description
ReadFile now returns bytes for PDFs and for AVIF/HEIC/HEIF/TIFF (by extension). The web viewer renders SVG as a `data:` `<img>` with a Rendered/Source toggle, adds BMP/ICO/AVIF/HEIC/TIFF raster previews (with a fallback when the browser can't decode them), and frames PDFs (CSP `frame-src blob:`). On iOS, SVG renders in a script-disabled WKWebView with a "Show source" toggle, PDFs use PDFKit, and other images use UIImage.

The owner confirmed the iOS viewer on device on 2026-11-27 (s_4c87f95b0a1299e2). Still in review: open a multipage PDF in the file viewer in desktop Chrome, Firefox and Safari and confirm the pages render; a frame loading without a CSP error is not sufficient.

## Acceptance criteria

## Work log
- 2026-11-10: iOS side checked in an owner-requested simulator review: SVG renders with its inline script not run and "Show source" works, PDF renders both pages, and HEIC, AVIF, TIFF, BMP, WebP and GIF render. PASS. The web side was not re-checked in this pass.
