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

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
)

const (
	MaxUploadBytes = 20 << 20
	// maxPixels stops a small file from claiming a huge canvas (~200 MB decoded).
	maxPixels = 50_000_000
	// Every stored file is a JPEG: the standard library has no WebP encoder.
	webQuality      = 82
	originalQuality = 90
)

// webWidths are the web sizes made from each image, never wider than it.
var webWidths = []int{1600, 800, 400}

// decoding allows one image in memory at a time: a 50-megapixel photo
// decodes to about 200 MB and the live host has 2 GB.
var decoding = make(chan struct{}, 1)

var (
	errNotAnImage = apperr.New(apperr.UnsupportedMediaType, "Upload a JPEG, PNG or WebP image.")
	errTooManyPx  = apperr.New(apperr.PayloadTooLarge, "The image is over 50 megapixels.")
)

// encoded is an image re-encoded for storage. Re-encoding drops all
// metadata, so the camera, time and location of the photo are gone.
type encoded struct {
	width, height int
	original      []byte
	sizes         map[int32][]byte
	widths        []int32 // largest first
}

// encode turns a JPEG, PNG or WebP upright by its EXIF orientation and
// re-encodes it as the original and the web sizes.
func encode(ctx context.Context, data []byte) (encoded, error) {
	if err := checkImage(data); err != nil {
		return encoded{}, err
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
	out := encoded{width: img.Bounds().Dx(), height: img.Bounds().Dy(), sizes: map[int32][]byte{}}
	out.original, err = jpegBytes(img, originalQuality)
	if err != nil {
		return encoded{}, err
	}
	out.widths = widthsFor(out.width)
	for _, w := range out.widths {
		sized := img
		if int(w) < out.width {
			sized = imaging.Resize(img, int(w), 0, imaging.Lanczos)
		}
		out.sizes[w], err = jpegBytes(sized, webQuality)
		if err != nil {
			return encoded{}, err
		}
	}
	return out, nil
}

// checkImage reads only the header, so a bad upload is refused before it is decoded.
func checkImage(data []byte) error {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !slices.Contains([]string{"jpeg", "png", "webp"}, format) {
		return errNotAnImage
	}
	if cfg.Width*cfg.Height > maxPixels {
		return errTooManyPx
	}
	return nil
}

// widthsFor returns the web widths for an image this wide, largest first:
// narrower images are never enlarged.
func widthsFor(width int) []int32 {
	var out []int32
	for _, target := range webWidths {
		w := int32(min(target, width))
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// onWhite flattens transparency onto white; JPEG cannot hold it and would turn it black.
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
