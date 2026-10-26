//go:build integration

package egress

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func TestS3Integration(t *testing.T) {
	endpoint := os.Getenv("KRITIKA_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("KRITIKA_TEST_S3_ENDPOINT is required for S3 integration tests")
	}
	opts := S3Options{
		Endpoint: endpoint, Region: os.Getenv("KRITIKA_TEST_S3_REGION"),
		Bucket: os.Getenv("KRITIKA_TEST_S3_BUCKET"), ForcePathStyle: true,
		AccessKey: os.Getenv("KRITIKA_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("KRITIKA_TEST_S3_SECRET_KEY"),
		Prefix: fmt.Sprintf("kritika-test-%d/", time.Now().UnixNano()),
	}
	c := &Cache{Dir: t.TempDir()}
	useS3(t, c, opts)
	exists, err := c.s3.client.BucketExists(t.Context(), opts.Bucket)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		if err := c.s3.client.MakeBucket(t.Context(), opts.Bucket, minio.MakeBucketOptions{Region: opts.Region}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		// Test contexts are canceled before cleanup; only this test's prefix is removed.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for obj := range c.s3.client.ListObjects(ctx, opts.Bucket, minio.ListObjectsOptions{Prefix: opts.Prefix, Recursive: true}) {
			if obj.Err != nil {
				t.Error(obj.Err)
				break
			}
			if err := c.s3.client.RemoveObject(ctx, opts.Bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
				t.Error(err)
			}
		}
		if !exists {
			if err := c.s3.client.RemoveBucket(ctx, opts.Bucket); err != nil {
				t.Error(err)
			}
		}
	})
	t.Run("replicas", func(t *testing.T) { testS3CacheSharesDownloads(t, opts) })
	t.Run("concurrent", func(t *testing.T) { testS3CacheConcurrentFills(t, opts) })
	t.Run("revision", func(t *testing.T) {
		key := keyOf("old")
		if err := c.store(key, entryOf("https://example.com/revision", "old"), strings.NewReader("old"), nil); err != nil {
			t.Fatal(err)
		}
		if err := c.upload(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		reader := &Cache{Dir: t.TempDir()}
		useS3(t, reader, opts)
		f, _, err := reader.load(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		// Another revision of the object, as a bucket can be written apart
		// from kritika.
		if _, err := c.s3.client.PutObject(t.Context(), c.s3.bucket, c.s3.prefix+key, strings.NewReader("new"), 3, minio.PutObjectOptions{}); err != nil {
			t.Fatal(err)
		}
		if b, err := io.ReadAll(f); err == nil || len(b) != 0 {
			t.Fatalf("read a replacement revision: %q, %v", b, err)
		}
	})
}
