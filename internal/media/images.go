package media

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // registers PNG with image.Decode
	"slices"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // registers WebP with image.Decode

	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

const (
	// MaxUploadBytes is the largest image accepted.
	MaxUploadBytes = 20 << 20
	// maxPixels refuses images that would decode to more than about 200 MB,
	// so a small file cannot claim a huge canvas.
	maxPixels = 50_000_000
	// The standard library has no WebP encoder, so every stored file is a
	// JPEG: quality 82 for the web sizes, 90 for the kept original.
	webQuality      = 82
	originalQuality = 90
)

// webWidths are the web sizes made from each image, never wider than it.
var webWidths = []int{1600, 800, 400}

// decoding allows one image in memory at a time: a 50-megapixel photo
// decodes to about 200 MB and the live host has 2 GB.
var decoding = make(chan struct{}, 1)

// encoded is an image re-encoded for storage. Re-encoding writes no
// metadata, so the camera, time and location in the upload are gone.
type encoded struct {
	width, height int
	original      []byte
	sizes         map[int32][]byte
	widths        []int32 // largest first
}

var (
	errNotAnImage = apperr.New(apperr.UnsupportedMediaType, "Upload a JPEG, PNG or WebP image.")
	errTooManyPx  = apperr.New(apperr.PayloadTooLarge, "The image is over 50 megapixels.")
)

// encode decodes data as a JPEG, PNG or WebP image, turned upright by its
// EXIF orientation, and re-encodes it as the original and the web sizes.
func encode(ctx context.Context, data []byte) (encoded, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !slices.Contains([]string{"jpeg", "png", "webp"}, format) {
		return encoded{}, errNotAnImage
	}
	if cfg.Width*cfg.Height > maxPixels {
		return encoded{}, errTooManyPx
	}
	select {
	case decoding <- struct{}{}:
		defer func() { <-decoding }()
	case <-ctx.Done():
		return encoded{}, ctx.Err()
	}

	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return encoded{}, errNotAnImage
	}
	img = onWhite(img)
	b := img.Bounds()
	out := encoded{width: b.Dx(), height: b.Dy(), sizes: map[int32][]byte{}}
	if out.original, err = jpegBytes(img, originalQuality); err != nil {
		return encoded{}, err
	}
	for _, target := range webWidths {
		w := int32(min(target, out.width))
		if slices.Contains(out.widths, w) {
			continue
		}
		sized := img
		if int(w) < out.width {
			sized = imaging.Resize(img, int(w), 0, imaging.Lanczos)
		}
		if out.sizes[w], err = jpegBytes(sized, webQuality); err != nil {
			return encoded{}, err
		}
		out.widths = append(out.widths, w)
	}
	return out, nil
}

// onWhite flattens transparency onto white, which JPEG cannot hold and
// would otherwise turn black.
func onWhite(img image.Image) image.Image {
	if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
		return img
	}
	b := img.Bounds()
	return imaging.Overlay(imaging.New(b.Dx(), b.Dy(), color.White), img, image.Point{}, 1)
}

func jpegBytes(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("media: encode: %w", err)
	}
	return buf.Bytes(), nil
}
