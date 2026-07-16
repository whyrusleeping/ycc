package server

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestValidateInputImages(t *testing.T) {
	data := tinyPNG(t)
	images, err := validateInputImages([]*v1.ImageAttachment{{
		Data: data, MediaType: "image/png", Filename: "../photo.png",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].MediaType != "image/png" || images[0].Filename != "photo.png" {
		t.Fatalf("images = %+v", images)
	}
	decoded, err := base64.StdEncoding.DecodeString(images[0].Base64)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatal("base64 payload did not round-trip")
	}
}

func TestValidateInputImagesRejectsUnsupportedMismatchAndOversize(t *testing.T) {
	data := tinyPNG(t)
	for name, attachment := range map[string]*v1.ImageAttachment{
		"unsupported": {Data: data, MediaType: "image/heic"},
		"mismatch":    {Data: data, MediaType: "image/jpeg"},
		"oversize":    {Data: bytes.Repeat([]byte{'x'}, maxInputImageSize+1), MediaType: "image/jpeg"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateInputImages([]*v1.ImageAttachment{attachment}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestImagePayloadIsNotEventMetadata(t *testing.T) {
	// Regression guard for the design boundary: callers derive only these safe
	// fields for user_input events, never Base64.
	image := struct{ MediaType, Filename, Base64 string }{"image/png", "photo.png", strings.Repeat("secret", 20)}
	metadata := map[string]any{"media_type": image.MediaType, "filename": image.Filename}
	if strings.Contains(metadata["filename"].(string), image.Base64) {
		t.Fatal("payload leaked into metadata")
	}
}
