// Package storage uploads rendered artifacts to Cloudflare R2.
package storage

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2 is S3-compatible but not regional, and rejects a real region name.
const region = "auto"

// Client uploads objects and reports the URL the site will link to.
type Client struct {
	s3         *s3.Client
	bucket     string
	publicBase string
}

// Endpoint is R2's S3 API host for an account.
func Endpoint(accountID string) string {
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
}

// New builds a client against R2's S3 API.
func New(accountID, accessKeyID, secretAccessKey, bucket, publicBase string) *Client {
	return &Client{
		s3: s3.New(s3.Options{
			Region:       region,
			BaseEndpoint: aws.String(Endpoint(accountID)),
			Credentials: credentials.NewStaticCredentialsProvider(
				accessKeyID, secretAccessKey, "",
			),
			UsePathStyle: true,
		}),
		bucket:     bucket,
		publicBase: publicBase,
	}
}

// Upload returns the public URL rather than a storage key, because every
// caller wants the thing the site will link to.
func (c *Client) Upload(
	ctx context.Context,
	key string,
	body []byte,
	contentType, cacheControl string,
) (string, error) {
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
		// R2 sets none of its own, and without one Vercel proxies every
		// request straight through to the bucket rather than caching it.
		CacheControl: aws.String(cacheControl),
	})
	if err != nil {
		return "", fmt.Errorf("uploading %s: %w", key, err)
	}

	slog.Info("uploaded", "key", key, "bytes", len(body))

	return fmt.Sprintf("%s/%s", c.publicBase, key), nil
}
