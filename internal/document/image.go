package document

import (
	"bytes"
	"fmt"
	"image"

	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
)

// MaxImagePixels caps how many pixels an uploaded image may decode to. A heavily
// compressed file that stays well under the upload size limit can still declare
// enormous dimensions, and decoding it allocates at least 4 bytes per pixel more
// than once, so the header is inspected before any pixel buffer is allocated.
// 50 megapixels still covers a 600 dpi A4 scan.
const MaxImagePixels = 50_000_000

// DecodeImage decodes content once its declared dimensions are known to fit
// within MaxImagePixels.
func DecodeImage(content []byte) (image.Image, string, error) {
	if err := ValidateImageDimensions(content); err != nil {
		return nil, "", err
	}
	return image.Decode(bytes.NewReader(content))
}

// ValidateImageDimensions rejects images whose header declares more pixels than
// MaxImagePixels allows, without decoding the pixel data.
func ValidateImageDimensions(content []byte) error {
	imageConfig, _, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("failed to inspect image header: %w", err)
	}
	if imageConfig.Width <= 0 || imageConfig.Height <= 0 {
		return fmt.Errorf("uploaded image reports invalid dimensions %dx%d", imageConfig.Width, imageConfig.Height)
	}
	if int64(imageConfig.Width)*int64(imageConfig.Height) > MaxImagePixels {
		return fmt.Errorf("uploaded image resolution %dx%d exceeds the maximum of %d pixels", imageConfig.Width, imageConfig.Height, MaxImagePixels)
	}
	return nil
}
