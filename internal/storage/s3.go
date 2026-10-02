package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// DefaultOpTimeout bounds each S3 call. Without a limit, a stalled network
// connection would block the caller (and, through it, Buffer.Close) forever.
const DefaultOpTimeout = 30 * time.Second

// S3Config describes an S3-compatible bucket. The same code talks to AWS S3,
// Cloudflare R2, MinIO and RustFS; only these values differ.
type S3Config struct {
	// Endpoint is the S3 API URL, e.g. "http://localhost:9000" for local
	// RustFS. Leave empty for real AWS S3.
	Endpoint string
	// Region is required by the signing protocol. Most S3-compatible servers
	// ignore its value; Cloudflare R2 uses "auto". Defaults to "us-east-1".
	Region string
	Bucket string
	// AccessKey/SecretKey are static credentials. If both are empty, the
	// standard AWS credential chain is used (env vars, shared config, IAM role).
	AccessKey string
	SecretKey string
	// PathStyle puts the bucket in the URL path (http://host/bucket/key)
	// instead of the hostname (http://bucket.host/key). Self-hosted servers
	// need this because "bucket.localhost" doesn't resolve.
	PathStyle bool
	// OpTimeout bounds each call; zero means DefaultOpTimeout.
	OpTimeout time.Duration
}

// S3 stores each object as an object in an S3-compatible bucket.
type S3 struct {
	client  *s3.Client
	bucket  string
	timeout time.Duration
}

var _ Storage = (*S3)(nil)

// NewS3 builds a client for cfg. It does not contact the server; use
// EnsureBucket or the first real call to find out if the settings are right.
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("storage: S3Config.Bucket is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.OpTimeout == 0 {
		cfg.OpTimeout = DefaultOpTimeout
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("storage: load S3 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.PathStyle
		// Only send checksum headers when the API requires them. Newer SDKs
		// add them to every request by default, which some S3-compatible
		// servers reject.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return &S3{client: client, bucket: cfg.Bucket, timeout: cfg.OpTimeout}, nil
}

// EnsureBucket creates the bucket if it does not exist yet.
func (s *S3) EnsureBucket(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("storage: check bucket %q: %w", s.bucket, err)
	}
	if _, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &s.bucket}); err != nil {
		return fmt.Errorf("storage: create bucket %q: %w", s.bucket, err)
	}
	return nil
}

// DeleteBucket removes the (empty) bucket. It exists for tests and tooling;
// Strata itself never deletes buckets.
func (s *S3) DeleteBucket(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if _, err := s.client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &s.bucket}); err != nil {
		return fmt.Errorf("storage: delete bucket %q: %w", s.bucket, err)
	}
	return nil
}

// Put uploads the object. S3 makes a single PutObject atomic: readers see
// the old object or the complete new one, never a partial upload, so there is
// no temp-file-and-rename step to get right.
//
// The reader is read fully into memory first. Uploads need a known length
// and a replayable body (the SDK retries and signs the payload), and segments
// are already built in memory before they get here.
func (s *S3) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	// Read before starting the timeout, so a slow producer isn't charged to S3.
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("storage: put %q: read body: %w", key, err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	return nil
}

// Get returns a reader for the object. The timeout covers the whole
// download, and is released when the caller closes the reader.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		cancel()
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: get %q: %w", key, err)
	}
	return &cancelOnClose{ReadCloser: out.Body, cancel: cancel}, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// List returns every key with the given prefix, sorted. S3 returns at most
// 1000 keys per request, so this follows the pagination tokens.
func (s *S3) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var keys []string
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: &s.bucket,
		Prefix: &prefix,
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("storage: list %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Delete removes the object. S3 reports success for a missing key, which
// matches the interface's idempotent-delete rule.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("storage: delete %q: %w", key, err)
	}
	return nil
}

// isNotFound recognises "no such key / no such bucket" across servers, which
// word the error slightly differently.
func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return true
		}
	}
	return false
}
