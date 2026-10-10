package media

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/config"
)

// Store is the S3-compatible bucket: originals under the private originals/
// prefix, web sizes under public/, which MEDIA_PUBLIC_URL serves.
type Store struct {
	client *s3.Client
	bucket string
}

// NewStore returns nil when no media endpoint is configured; uploads are then
// refused. Without keys it signs with the EC2 instance role, as on the live host.
func NewStore(cfg config.Config) *Store {
	if cfg.MediaS3Endpoint == "" {
		return nil
	}
	var creds aws.CredentialsProvider = aws.NewCredentialsCache(ec2rolecreds.New())
	if cfg.MediaS3AccessKey != "" {
		creds = credentials.NewStaticCredentialsProvider(cfg.MediaS3AccessKey, cfg.MediaS3SecretKey, "")
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.MediaS3Endpoint),
		Region:       cfg.MediaS3Region,
		UsePathStyle: true,
		Credentials:  creds,
		// Not every S3-compatible provider accepts the checksums the SDK adds by default.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Store{client: client, bucket: cfg.MediaS3Bucket}
}

// put stores a JPEG. A key's content never changes, so browsers may cache it for a year.
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
		_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
		if err != nil {
			return err
		}
	}
	return nil
}

func originalKey(id pgtype.UUID) string { return "originals/" + id.String() + ".jpg" }

func publicKey(id pgtype.UUID, width int32) string { return "public/" + webPath(id, width) }

// webPath is where a web size lives, relative to the public/ prefix.
func webPath(id pgtype.UUID, width int32) string {
	return id.String() + "/" + strconv.Itoa(int(width)) + ".jpg"
}

// keys are every object an item has.
func keys(id pgtype.UUID, widths []int32) []string {
	out := []string{originalKey(id)}
	for _, w := range widths {
		out = append(out, publicKey(id, w))
	}
	return out
}

type Size struct {
	Width int32
	URL   string
}

// Sizes are an item's web sizes, largest first, served from publicURL (MEDIA_PUBLIC_URL).
func Sizes(publicURL string, id pgtype.UUID, widths []int32) []Size {
	base := strings.TrimSuffix(publicURL, "/") + "/"
	out := make([]Size, len(widths))
	for i, w := range widths {
		out[i] = Size{Width: w, URL: base + webPath(id, w)}
	}
	return out
}
