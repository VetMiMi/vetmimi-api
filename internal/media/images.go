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

// encoded is an image re-encoded for storage. Re-encoding drops all
// metadata, so the camera, time and location of the photo are gone.
type encoded struct {
	width    int
	height   int
	original []byte
	sizes    map[int32][]byte
	widths   []int32 // largest first
}

const (
	MaxUploadBytes = 20 << 20
	// maxPixels keeps one decoded image, and the upright copy a rotated phone
	// photo needs, within the api container's 192 MB (deploy/compose).
	maxPixels = 16_000_000
	// Every stored file is a JPEG: the standard library has no WebP encoder.
	webQuality      = 82
	originalQuality = 90
)

// webWidths are the web sizes made from each image, never wider than it.
var webWidths = []int{1600, 800, 400}

// decoding allows one image in memory at a time: a 16-megapixel photo
// turned upright takes about 90 MB.
var decoding = make(chan struct{}, 1)

var (
	errNotAnImage    = apperr.New(apperr.UnsupportedMediaType, "Upload a JPEG, PNG or WebP image.")
	errTooManyPixels = apperr.New(apperr.PayloadTooLarge, "The image is over 16 megapixels.")
)

// encode turns a JPEG, PNG or WebP upright by its EXIF orientation and
// re-encodes it as the original and the web sizes.
func encode(ctx context.Context, data []byte) (encoded, error) {
	err := checkImage(data)
	if err != nil {
		return encoded{}, err
	}
	select {
	case decoding <- struct{}{}:
		defer func() { <-decoding }()
	case <-ctx.Done():
		return encoded{}, ctx.Err()
	}

	decoded, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return encoded{}, errNotAnImage
	}
	decoded = onWhite(decoded)
	result := encoded{
		width:  decoded.Bounds().Dx(),
		height: decoded.Bounds().Dy(),
		sizes:  map[int32][]byte{},
	}
	result.original, err = jpegBytes(decoded, originalQuality)
	if err != nil {
		return encoded{}, err
	}
	result.widths = widthsFor(result.width)
	for _, width := range result.widths {
		sized := decoded
		if int(width) < result.width {
			sized = imaging.Resize(decoded, int(width), 0, imaging.Lanczos)
		}
		result.sizes[width], err = jpegBytes(sized, webQuality)
		if err != nil {
			return encoded{}, err
		}
	}
	return result, nil
}

// checkImage reads only the header, so a bad upload is refused before it is decoded.
func checkImage(data []byte) error {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !slices.Contains([]string{"jpeg", "png", "webp"}, format) {
		return errNotAnImage
	}
	if config.Width*config.Height > maxPixels {
		return errTooManyPixels
	}
	return nil
}

// widthsFor returns the web widths for an image this wide, largest first:
// narrower images are never enlarged.
func widthsFor(imageWidth int) []int32 {
	var widths []int32
	for _, target := range webWidths {
		width := int32(min(target, imageWidth))
		if !slices.Contains(widths, width) {
			widths = append(widths, width)
		}
	}
	return widths
}

// onWhite flattens transparency onto white; JPEG cannot hold it and would turn it black.
func onWhite(img image.Image) image.Image {
	opaque, ok := img.(interface{ Opaque() bool })
	if ok && opaque.Opaque() {
		return img
	}
	bounds := img.Bounds()
	return imaging.Overlay(imaging.New(bounds.Dx(), bounds.Dy(), color.White), img, image.Point{}, 1)
}

func jpegBytes(img image.Image, quality int) ([]byte, error) {
	var buffer bytes.Buffer
	err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: quality})
	if err != nil {
		return nil, fmt.Errorf("media: encode: %w", err)
	}
	return buffer.Bytes(), nil
}
