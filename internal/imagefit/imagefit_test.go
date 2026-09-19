package imagefit

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func encode(t *testing.T, w, h int, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y += 97 {
		for x := 0; x < w; x += 89 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var out bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&out, img, nil)
	default:
		err = png.Encode(&out, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func dims(t *testing.T, data []byte) (int, int, string) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height, format
}

func TestFitLeavesSmallImagesUntouched(t *testing.T) {
	data := encode(t, 640, 480, "png")
	res, err := Fit(data, "image/png", MaxEdge)
	if err != nil {
		t.Fatal(err)
	}
	if res.Resized || !bytes.Equal(res.Data, data) || res.MediaType != "image/png" || res.Width != 640 || res.Height != 480 {
		t.Fatalf("small image altered: %+v", res)
	}
}

func TestFitScalesLongEdgeAndKeepsAspect(t *testing.T) {
	for _, tc := range []struct {
		format        string
		inW, inH      int
		wantW, wantH  int
		wantMediaType string
	}{
		{inW: 2588, inH: 690, format: "png", wantW: 2000, wantH: 533, wantMediaType: "image/png"},
		{inW: 1179, inH: 2556, format: "png", wantW: 922, wantH: 2000, wantMediaType: "image/png"},
		{inW: 3000, inH: 3000, format: "jpeg", wantW: 2000, wantH: 2000, wantMediaType: "image/jpeg"},
	} {
		data := encode(t, tc.inW, tc.inH, tc.format)
		res, err := Fit(data, "image/"+tc.format, MaxEdge)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Resized || res.Width != tc.wantW || res.Height != tc.wantH || res.MediaType != tc.wantMediaType {
			t.Fatalf("%dx%d %s: got %dx%d %s resized=%v", tc.inW, tc.inH, tc.format, res.Width, res.Height, res.MediaType, res.Resized)
		}
		gotW, gotH, gotFormat := dims(t, res.Data)
		if gotW != tc.wantW || gotH != tc.wantH || "image/"+gotFormat != tc.wantMediaType {
			t.Fatalf("%dx%d %s: encoded %dx%d %s", tc.inW, tc.inH, tc.format, gotW, gotH, gotFormat)
		}
	}
}

func TestFitRejectsUndecodable(t *testing.T) {
	if _, err := Fit([]byte("not an image"), "image/png", MaxEdge); err == nil {
		t.Fatal("expected decode error")
	}
}
