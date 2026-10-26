package egress

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Options locates an existing bucket. Only gateway processes receive its credentials.
type S3Options struct {
	Endpoint, Region, Bucket, Prefix string
	AccessKey, SecretKey             string
	ForcePathStyle                   bool
}

type s3Cache struct {
	client        *minio.Client
	bucket        string
	prefix        string
	uploads       chan string
	reads, writes s3Circuit
}

// UseS3 shares completed downloads through S3. Local fills remain usable if an
// upload fails; their directory and budget still belong to this process alone.
// The bucket is not contacted at startup, so its availability cannot stop serve.
// o is as package config checked the settings it comes from.
func (c *Cache) UseS3(o S3Options) error {
	if c.MaxEntryBytes <= 0 || c.MaxEntryBytes > 5<<30 {
		return errors.New("egress: cache: S3 entry limit must be positive and at most 5Gi")
	}
	c.MaxBytes = maxFills * c.MaxEntryBytes
	u, _ := url.Parse(o.Endpoint)
	creds := credentials.New(&s3Credentials{Provider: &credentials.IAM{}})
	if o.AccessKey != "" {
		creds = credentials.NewStaticV4(o.AccessKey, o.SecretKey, "")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: s3LookupTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = s3LookupTimeout
	transport.TLSHandshakeTimeout = s3LookupTimeout
	lookup := minio.BucketLookupAuto
	if o.ForcePathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds: creds, Secure: u.Scheme == upgradedScheme, Region: o.Region, BucketLookup: lookup,
		Transport: &cacheTransport{base: transport, cache: c}, MaxRetries: 1,
	})
	if err != nil {
		return fmt.Errorf("egress: cache: S3 client: %w", err)
	}
	c.s3 = &s3Cache{client: client, bucket: o.Bucket, prefix: strings.TrimSuffix(o.Prefix, "/") + "/v1/"}
	c.s3.prefix = strings.TrimPrefix(c.s3.prefix, "/")
	c.s3.uploads = make(chan string, s3UploadQueue)
	return nil
}

// load is the entry under key to serve, this replica's copy before the
// bucket's.
func (c *Cache) load(ctx context.Context, key string) (_ *cacheBody, _ cacheEntry, loadErr error) {
	f, entry, offset, err := c.open(key)
	if err == nil && c.MaxEntryBytes > 0 && entry.Size > c.MaxEntryBytes {
		_ = f.Close()
		err = os.ErrNotExist
	}
	if err == nil {
		reader := struct {
			io.ReadSeeker
			io.Closer
		}{io.NewSectionReader(f, offset, entry.Size), f}
		return &cacheBody{ReadSeekCloser: reader, source: "local"}, entry, nil
	}
	if c.s3 == nil {
		return nil, cacheEntry{}, err
	}
	attempt, allowed := c.s3.reads.allow(time.Now())
	if !allowed {
		return nil, cacheEntry{}, errS3CircuitOpen
	}
	started := time.Now()
	complete := true
	defer func() {
		result := operationResult(loadErr)
		circuitErr := loadErr
		if errors.Is(loadErr, os.ErrNotExist) {
			result, circuitErr = cacheMiss, nil
		} else if loadErr != nil {
			c.warn("s3_lookup", key, loadErr)
		}
		if complete {
			if ctx.Err() != nil {
				circuitErr = context.Canceled
			}
			c.s3.reads.complete(attempt, circuitErr, time.Now())
		}
		c.metrics.CacheOperation("s3_lookup", result, time.Since(started), 0)
	}()
	lookupCtx, cancel := context.WithTimeout(ctx, s3LookupTimeout)
	info, err := c.s3.client.StatObject(lookupCtx, c.s3.bucket, c.s3.prefix+key, minio.StatObjectOptions{})
	cancel()
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, cacheEntry{}, os.ErrNotExist
		}
		return nil, cacheEntry{}, err
	}
	entry, ok := c.s3Entry(info, key)
	if !ok {
		return nil, cacheEntry{}, os.ErrNotExist
	}
	// The bounded metadata request cannot own the streaming body's context.
	// Explicit If-Match preserves the revision across the SDK's lazy reads/seeks.
	opts := minio.GetObjectOptions{}
	if err := opts.SetMatchETag(info.ETag); err != nil {
		return nil, cacheEntry{}, err
	}
	obj, err := c.s3.client.GetObject(ctx, c.s3.bucket, c.s3.prefix+key, opts)
	if err != nil {
		return nil, cacheEntry{}, err
	}
	complete = false
	return &cacheBody{ReadSeekCloser: obj, cache: c, source: "s3", key: key, finish: func(err error) {
		if ctx.Err() != nil {
			err = context.Canceled
		}
		c.s3.reads.complete(attempt, err, time.Now())
	}}, entry, nil
}

// s3Entry is what an object's metadata records of the entry under key,
// and false for an object that does not hold a sound entry for it.
func (c *Cache) s3Entry(info minio.ObjectInfo, key string) (cacheEntry, bool) {
	var entry cacheEntry
	raw, err := base64.StdEncoding.DecodeString(info.Metadata.Get("X-Amz-Meta-Kritika-Entry"))
	if err == nil {
		err = json.Unmarshal(raw, &entry)
	}
	d, named := ParseDigest(entry.Digest)
	if err != nil || entry.Size < 0 || entry.Size != info.Size || !named || d.key() != key ||
		info.ETag == "" || (c.MaxEntryBytes > 0 && entry.Size > c.MaxEntryBytes) {
		return cacheEntry{}, false
	}
	return entry, true
}

// s3Gone reports whether reading an object failed because it went, or was
// replaced, since it was looked up.
func s3Gone(err error) bool {
	var resp minio.ErrorResponse
	return errors.As(err, &resp) && (resp.Code == "NoSuchKey" || resp.Code == "PreconditionFailed")
}

func (c *Cache) upload(ctx context.Context, key string) (uploadErr error) {
	f, entry, offset, err := c.open(key)
	if err != nil {
		c.metrics.CacheUploadSkipped("entry_unavailable")
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	metadata := base64.StdEncoding.EncodeToString(raw)
	if len(metadata)+len("kritika-entry") > 2048 {
		c.metrics.CacheUploadSkipped("entry_unavailable")
		return errors.New("egress: cache: entry metadata exceeds the S3 metadata limit")
	}
	attempt, allowed := c.s3.writes.allow(time.Now())
	if !allowed {
		c.metrics.CacheUploadSkipped("circuit_open")
		return errS3CircuitOpen
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, uploadDeadline(entry.Size))
	defer cancel()
	started := time.Now()
	// Another replica may have published the entry already, the same body
	// under the same digest: then this upload is not made. The check spends
	// from the upload's own budget, and failing it does not stop the
	// upload, which the circuit bounds.
	statCtx, cancelStat := context.WithTimeout(ctx, s3LookupTimeout)
	info, err := c.s3.client.StatObject(statCtx, c.s3.bucket, c.s3.prefix+key, minio.StatObjectOptions{})
	cancelStat()
	if err == nil {
		if _, ok := c.s3Entry(info, key); ok {
			c.metrics.CacheUploadSkipped("present")
			c.s3.writes.complete(attempt, nil, time.Now())
			return nil
		}
	}
	defer func() {
		circuitErr := uploadErr
		if parent.Err() != nil {
			circuitErr = context.Canceled
		}
		c.s3.writes.complete(attempt, circuitErr, time.Now())
		c.metrics.CacheOperation("s3_upload", operationResult(uploadErr), time.Since(started), 0)
		if uploadErr != nil {
			c.warn("s3_upload", key, uploadErr)
		}
	}()
	_, err = c.s3.client.PutObject(ctx, c.s3.bucket, c.s3.prefix+key, io.NewSectionReader(f, offset, entry.Size), entry.Size,
		minio.PutObjectOptions{
			UserMetadata: map[string]string{"kritika-entry": metadata},
			ContentType:  "application/octet-stream", DisableMultipart: true, SendContentMd5: true,
			// Content-MD5 verifies the body without requiring aws-chunked signing.
			DisableContentSha256: true,
		})
	if err != nil {
		return fmt.Errorf("egress: cache: S3 upload: %w", err)
	}
	return nil
}
