package main

import (
	"bytes"
	"image"
	"image/jpeg"
	_ "image/png" // registers the PNG decoder with image.Decode

	"golang.org/x/image/draw"
)

// maxImageWidth is what we ask Wikimedia to pre-scale thumbnails to (see
// images.go's ResolveImages and cache.go's saveImages) — plenty for both the
// ~140px Browse cards and the full-screen Learn card this project renders,
// even at high DPI. capImageWidth is the local backstop for the rare case a
// downloaded image is wider than that anyway (Wikimedia won't upscale past
// the source, but a mismatched/odd source — or an iNaturalist original —
// could still come back larger); we'd rather resize once here than serve and
// store an oversized file indefinitely.
const maxImageWidth = 1600

const resizedJPEGQuality = 85

// capImageWidth returns data unchanged if it's already at or under
// maxWidth, otherwise decodes it, scales it down to maxWidth (preserving
// aspect ratio), and re-encodes as JPEG.
func capImageWidth(data []byte, maxWidth int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	srcBounds := img.Bounds()
	if srcBounds.Dx() <= maxWidth {
		return data, nil
	}

	newHeight := srcBounds.Dy() * maxWidth / srcBounds.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, maxWidth, newHeight))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, srcBounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: resizedJPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
