// Package imagefit bounds the pixel dimensions of images before they enter
// model history.
//
// Anthropic accepts images up to 8000px per side, but once a request carries
// more than 20 images every image is capped at 2000px per side. Because images
// accumulate in history (user pictures and Read-tool results alike), a single
// oversize Retina screenshot sent early can brick a session later: every
// subsequent request fails with "At least one of the image dimensions exceed
// max allowed size for many-image requests". Scaling at ingestion keeps every
// image within the strict cap so the request-level count never matters.
// Providers downscale further server-side anyway (Anthropic to ~1568px on the
// long edge), so nothing the model would see is lost.
package imagefit

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decode support; no encoder exists
)

// MaxEdge is the strict per-side pixel cap applied to every ingested image.
const MaxEdge = 2000

// jpegQuality balances size against fidelity for re-encoded JPEG sources.
const jpegQuality = 85

// Result describes a fitted image.
type Result struct {
	Data      []byte
	MediaType string
	Width     int // dimensions after fitting
	Height    int
	Resized   bool
}

// Fit returns data re-encoded so neither side exceeds maxEdge. Images already
// within bounds are returned unchanged (same bytes, same media type). JPEG
// sources stay JPEG; PNG, GIF, and WebP sources are re-encoded as PNG when
// resized (GIF animation is flattened to its first frame). Undecodable data is
// an error so callers can decide whether to pass it through.
func Fit(data []byte, mediaType string, maxEdge int) (Result, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("decode image header: %w", err)
	}
	if cfg.Width <= maxEdge && cfg.Height <= maxEdge {
		return Result{Data: data, MediaType: mediaType, Width: cfg.Width, Height: cfg.Height}, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("decode image: %w", err)
	}
	w, h := cfg.Width, cfg.Height
	if w >= h {
		h = max(1, h*maxEdge/w)
		w = maxEdge
	} else {
		w = max(1, w*maxEdge/h)
		h = maxEdge
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	var out bytes.Buffer
	outType := "image/png"
	switch format {
	case "jpeg":
		outType = "image/jpeg"
		err = jpeg.Encode(&out, dst, &jpeg.Options{Quality: jpegQuality})
	default:
		err = png.Encode(&out, dst)
	}
	if err != nil {
		return Result{}, fmt.Errorf("encode fitted image: %w", err)
	}
	return Result{Data: out.Bytes(), MediaType: outType, Width: w, Height: h, Resized: true}, nil
}

// ensure the gif decoder is linked even if no other importer registers it.
var _ = gif.Decode
