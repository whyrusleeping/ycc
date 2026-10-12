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

Status is in_review until the iOS app is built and checked on a device: open an .svg (rendered by default, Show source works), a .pdf, and a .heic. Also check a PDF in desktop Chrome, Firefox, and Safari.

## Acceptance criteria

## Work log
