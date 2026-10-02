package mailbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 drops boxes into the user's own S3-compatible bucket: Cloudflare R2,
// AWS S3, Backblaze B2, MinIO, and so on. The receiver gets presigned links,
// never the keys.
type S3 struct {
	Endpoint  string `json:"endpoint"` // host[:port], e.g. <account>.r2.cloudflarestorage.com
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Insecure  bool   `json:"insecure,omitempty"` // plain http, for local testing only
}

// MaxPresign is the longest presigned link S3 and R2 allow (7 days).
const MaxPresign = 7 * 24 * time.Hour

func (s S3) Kind() string { return "s3" }

func (s S3) client() (*minio.Client, error) {
	ep := strings.TrimPrefix(strings.TrimPrefix(s.Endpoint, "https://"), "http://")
	region := s.Region
	if region == "" {
		region = "auto" // what R2 expects; others ignore it or use their own
	}
	return minio.New(ep, &minio.Options{
		Creds:  credentials.NewStaticV4(s.AccessKey, s.SecretKey, ""),
		Secure: !s.Insecure,
		Region: region,
	})
}

func (s S3) Drop(ctx context.Context, boxPath string, ttl time.Duration) (Ticket, error) {
	if ttl <= 0 || ttl > MaxPresign {
		ttl = MaxPresign
	}
	c, err := s.client()
	if err != nil {
		return Ticket{}, err
	}
	id := make([]byte, 8)
	rand.Read(id)
	obj := strings.Trim(s.Prefix, "/")
	if obj != "" {
		obj += "/"
	}
	obj += "tote-" + hex.EncodeToString(id) + ".tote"

	if _, err := c.FPutObject(ctx, s.Bucket, obj, boxPath,
		minio.PutObjectOptions{ContentType: "application/octet-stream"}); err != nil {
		return Ticket{}, fmt.Errorf("upload to your bucket failed: %w", err)
	}
	get, err := c.PresignedGetObject(ctx, s.Bucket, obj, ttl, url.Values{})
	if err != nil {
		return Ticket{}, err
	}
	del, err := c.Presign(ctx, "DELETE", s.Bucket, obj, ttl, url.Values{})
	if err != nil {
		return Ticket{}, err
	}
	return Ticket{V: 1, Get: get.String(), Del: del.String(), Exp: time.Now().Add(ttl).UTC().Truncate(time.Second)}, nil
}
