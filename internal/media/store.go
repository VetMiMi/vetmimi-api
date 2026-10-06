package media

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

// Store keeps media objects in the S3-compatible bucket: the original under
// the private originals/ prefix and the web sizes under public/, which
// MEDIA_PUBLIC_URL serves.
type Store struct {
	client *s3.Client
	bucket string
}

// NewStore returns the bucket cfg names, or nil when cfg has no media
// endpoint, as in development without storage; uploads are then refused.
func NewStore(cfg platform.Config) *Store {
	if cfg.MediaS3Endpoint == "" {
		return nil
	}
	creds := aws.Credentials{AccessKeyID: cfg.MediaS3AccessKey, SecretAccessKey: cfg.MediaS3SecretKey}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.MediaS3Endpoint),
		Region:       cfg.MediaS3Region,
		UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return creds, nil
		}),
		// Not every S3-compatible provider accepts the checksums the SDK
		// adds by default.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Store{client: client, bucket: cfg.MediaS3Bucket}
}

// put stores a JPEG. Keys never change content, so browsers may keep the
// web sizes for a year.
func (s *Store) put(ctx context.Context, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(s.bucket),
		Key:          aws.String(key),
		Body:         bytes.NewReader(body),
		ContentType:  aws.String("image/jpeg"),
		CacheControl: aws.String("public, max-age=31536000, immutable"),
	})
	return err
}

// remove deletes keys; deleting a missing key succeeds.
func (s *Store) remove(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket), Key: aws.String(key),
		}); err != nil {
			return err
		}
	}
	return nil
}

func originalKey(id pgtype.UUID) string { return "originals/" + id.String() + ".jpg" }

func sizePath(id pgtype.UUID, width int32) string {
	return id.String() + "/" + strconv.Itoa(int(width)) + ".jpg"
}

// keys are every object an item has.
func keys(id pgtype.UUID, widths []int32) []string {
	out := []string{originalKey(id)}
	for _, w := range widths {
		out = append(out, "public/"+sizePath(id, w))
	}
	return out
}

// Size is one web size of an image and where the public reads it.
type Size struct {
	Width int32
	URL   string
}

// Sizes are the web sizes of item id, largest first, at publicURL, the base
// MEDIA_PUBLIC_URL serves the public/ prefix from.
func Sizes(publicURL string, id pgtype.UUID, widths []int32) []Size {
	base := strings.TrimSuffix(publicURL, "/") + "/"
	out := make([]Size, len(widths))
	for i, w := range widths {
		out[i] = Size{Width: w, URL: base + sizePath(id, w)}
	}
	return out
}
