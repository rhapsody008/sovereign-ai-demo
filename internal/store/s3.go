package store

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Options struct {
	Endpoint           string
	UseTLS             bool
	InsecureSkipVerify bool
	Bucket             string
	AccessKey          string
	SecretKey          string
	Region             string
}

// S3 is a Store backed by Nutanix Objects or any S3-compatible service.
type S3 struct {
	c      *minio.Client
	bucket string

	// Records are immutable, so cache parsed ones to keep list polls cheap.
	mu    sync.Mutex
	cache map[string]Record
}

func NewS3(o S3Options) (*S3, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if o.InsecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // lab self-signed certs
	}
	c, err := minio.New(o.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure:       o.UseTLS,
		Region:       o.Region,
		Transport:    tr,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, err
	}
	return &S3{c: c, bucket: o.Bucket, cache: map[string]Record{}}, nil
}

func (s *S3) Backend() string { return "objects" }

func (s *S3) PutRecord(ctx context.Context, r Record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.PutObject(ctx, "records/"+r.ID+".json", data, "application/json")
}

func (s *S3) recordKeys(ctx context.Context) ([]string, error) {
	var keys []string
	for obj := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: "records/"}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		if strings.HasSuffix(obj.Key, ".json") {
			keys = append(keys, obj.Key)
		}
	}
	return keys, nil
}

func (s *S3) ListRecords(ctx context.Context, limit int) ([]Record, error) {
	keys, err := s.recordKeys(ctx)
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	out := make([]Record, 0, limit)
	for _, k := range keys {
		if len(out) >= limit {
			break
		}
		s.mu.Lock()
		r, ok := s.cache[k]
		s.mu.Unlock()
		if !ok {
			data, _, err := s.GetObject(ctx, k)
			if err != nil {
				continue
			}
			if json.Unmarshal(data, &r) != nil {
				continue
			}
			s.mu.Lock()
			s.cache[k] = r
			s.mu.Unlock()
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *S3) PutObject(ctx context.Context, key string, data []byte, ct string) error {
	_, err := s.c.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: ct})
	return err
}

func (s *S3) GetObject(ctx context.Context, key string) ([]byte, string, error) {
	obj, err := s.c.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", err
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	info, _ := obj.Stat()
	return data, info.ContentType, nil
}

func (s *S3) CountRecords(ctx context.Context) (int, error) {
	keys, err := s.recordKeys(ctx)
	return len(keys), err
}

func (s *S3) Ping(ctx context.Context) error {
	ok, err := s.c.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !ok {
		return &missingBucketError{s.bucket}
	}
	return nil
}

// EnsureBucket creates the bucket if it does not exist yet.
func (s *S3) EnsureBucket(ctx context.Context, region string) error {
	ok, err := s.c.BucketExists(ctx, s.bucket)
	if err != nil || ok {
		return err
	}
	return s.c.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: region})
}

type missingBucketError struct{ bucket string }

func (e *missingBucketError) Error() string { return "bucket " + e.bucket + " does not exist" }
