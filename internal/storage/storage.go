// Package storage wraps the S3-compatible object store used for attachments,
// thumbnails and export files. The bucket is private: clients never receive
// long-lived credentials, only short-lived presigned URLs scoped to one object.
//
// The implementation uses the MinIO client, which speaks the S3 API, so the
// same code works against MinIO in development and any S3-compatible service in
// production.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrObjectNotFound is returned when an object key does not exist.
var ErrObjectNotFound = errors.New("storage: object not found")

// Config configures the S3 client and bucket.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// ObjectInfo is the metadata returned by Head.
type ObjectInfo struct {
	Key         string
	Size        int64
	ContentType string
	ETag        string
}

// Backend is the object storage contract used by the attachment, thumbnail
// and export services. Tests substitute an in-memory fake.
type Backend interface {
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error)
	Head(ctx context.Context, key string) (ObjectInfo, error)
	GetRange(ctx context.Context, key string, n int64) ([]byte, error)
	Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Delete(ctx context.Context, key string) error
}

// S3 is a MinIO/S3-backed Backend.
type S3 struct {
	client *minio.Client
	bucket string
}

// New creates an S3 client. It does not connect; the first operation does.
func New(cfg Config) (*S3, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("storage: endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("storage: bucket is required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("storage: access key and secret key are required")
	}

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: create client: %w", err)
	}
	return &S3{client: client, bucket: cfg.Bucket}, nil
}

// EnsureBucket creates the private bucket when it does not exist. It is safe
// to call on every process start.
func (s *S3) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("storage: check bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		// A concurrent process may have created it between the check and the
		// make; treat that as success.
		var resp minio.ErrorResponse
		if errors.As(err, &resp) && resp.Code == "BucketAlreadyOwnedByYou" {
			return nil
		}
		return fmt.Errorf("storage: make bucket: %w", err)
	}
	return nil
}

// PresignPut returns a short-lived URL the client uses to upload one object
// directly to storage.
func (s *S3) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.client.PresignedPutObject(ctx, s.bucket, key, ttl)
	if err != nil {
		return "", fmt.Errorf("storage: presign put: %w", err)
	}
	return u.String(), nil
}

// PresignGet returns a short-lived URL for one object. contentDisposition is
// sent as a response override so downloads have a safe filename.
func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error) {
	params := url.Values{}
	if contentDisposition != "" {
		params.Set("response-content-disposition", contentDisposition)
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, params)
	if err != nil {
		return "", fmt.Errorf("storage: presign get: %w", err)
	}
	return u.String(), nil
}

// Head returns object metadata without downloading the body.
func (s *S3) Head(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return ObjectInfo{}, ErrObjectNotFound
		}
		return ObjectInfo{}, fmt.Errorf("storage: head: %w", err)
	}
	return toObjectInfo(key, info), nil
}

// GetRange reads at most n bytes from the start of an object. It is used to
// sniff the content type of an upload without downloading the whole file.
func (s *S3) GetRange(ctx context.Context, key string, n int64) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("storage: get range: %w", err)
	}
	defer obj.Close()

	buf := make([]byte, n)
	total := int64(0)
	for total < n {
		r, readErr := obj.Read(buf[total:])
		total += int64(r)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if isNotFound(readErr) {
				return nil, ErrObjectNotFound
			}
			return nil, fmt.Errorf("storage: read range: %w", readErr)
		}
	}
	return buf[:total], nil
}

// Open streams an object. The caller must close the returned reader.
func (s *S3) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, ObjectInfo{}, ErrObjectNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("storage: open: %w", err)
	}
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, ObjectInfo{}, ErrObjectNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("storage: stat open: %w", err)
	}
	return obj, toObjectInfo(key, info), nil
}

// Put uploads an object. When size is negative the client streams the reader
// through a multipart upload with a bounded part size, so memory stays flat
// regardless of the object size.
func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	opts := minio.PutObjectOptions{
		ContentType: contentType,
		PartSize:    16 << 20,
	}
	if _, err := s.client.PutObject(ctx, s.bucket, key, r, size, opts); err != nil {
		return fmt.Errorf("storage: put: %w", err)
	}
	return nil
}

// Delete removes one object. A missing object is not an error: deletion is
// idempotent so retention jobs can safely re-run.
func (s *S3) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("storage: delete: %w", err)
	}
	return nil
}

func toObjectInfo(key string, info minio.ObjectInfo) ObjectInfo {
	return ObjectInfo{
		Key:         key,
		Size:        info.Size,
		ContentType: info.ContentType,
		ETag:        strings.Trim(info.ETag, `"`),
	}
}

func isNotFound(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		switch resp.Code {
		case "NoSuchKey", "NoSuchObject", "NoSuchBucket", "NotFound":
			return true
		}
	}
	return errors.Is(err, os.ErrNotExist)
}
