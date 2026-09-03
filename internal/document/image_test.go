package document

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestValidateImageDimensionsAcceptsRegularImage(t *testing.T) {
	t.Parallel()

	if err := ValidateImageDimensions(encodePNG(t, 40, 30)); err != nil {
		t.Fatalf("expected regular image to pass, got %v", err)
	}
}

func TestValidateImageDimensionsRejectsPixelBomb(t *testing.T) {
	t.Parallel()

	bomb := pngWithDeclaredSize(t, 40000, 40000)
	if len(bomb) > 1024 {
		t.Fatalf("expected the crafted bomb to stay small, got %d bytes", len(bomb))
	}

	err := ValidateImageDimensions(bomb)
	if err == nil {
		t.Fatalf("expected the oversized image to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds the maximum") {
		t.Fatalf("expected a resolution limit error, got %v", err)
	}

	// The header check has to fire before the decoder allocates the pixel buffer,
	// so the reported failure must be the limit and not a decode error.
	_, _, decodeErr := DecodeImage(bomb)
	if decodeErr == nil || !strings.Contains(decodeErr.Error(), "exceeds the maximum") {
		t.Fatalf("expected DecodeImage to refuse the oversized image, got %v", decodeErr)
	}
}

func TestValidateImageDimensionsRejectsUndecodableContent(t *testing.T) {
	t.Parallel()

	if err := ValidateImageDimensions([]byte("not an image")); err == nil {
		t.Fatalf("expected an error for content without an image header")
	}
}

func TestDecodeImageReturnsDecodedImage(t *testing.T) {
	t.Parallel()

	decoded, format, err := DecodeImage(encodePNG(t, 12, 8))
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	if format != "png" {
		t.Fatalf("unexpected format %q", format)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 12 || bounds.Dy() != 8 {
		t.Fatalf("unexpected bounds %v", bounds)
	}
}

func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.White)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// pngWithDeclaredSize rewrites the IHDR header of a tiny PNG so that it claims a
// huge resolution while the compressed payload stays a few hundred bytes, which
// is how a decompression bomb reaches the decoder.
func pngWithDeclaredSize(t *testing.T, width, height uint32) []byte {
	t.Helper()

	content := encodePNG(t, 1, 1)
	const ihdrTypeOffset = 12
	const ihdrDataOffset = 16
	if len(content) < ihdrDataOffset+13+4 {
		t.Fatalf("unexpected png layout, got %d bytes", len(content))
	}
	if header := string(content[ihdrTypeOffset:ihdrDataOffset]); header != "IHDR" {
		t.Fatalf("unexpected first chunk %q", header)
	}

	binary.BigEndian.PutUint32(content[ihdrDataOffset:], width)
	binary.BigEndian.PutUint32(content[ihdrDataOffset+4:], height)
	checksum := crc32.ChecksumIEEE(content[ihdrTypeOffset : ihdrDataOffset+13])
	binary.BigEndian.PutUint32(content[ihdrDataOffset+13:], checksum)
	return content
}
