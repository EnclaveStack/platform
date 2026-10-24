package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// partSize is the multipart chunk: uploads of unknown length are sent 16 MiB at a time.
const partSize = 16 << 20

// S3Opener reaches real S3-compatible storage.
type S3Opener struct{}

type s3Store struct {
	c      *minio.Client
	bucket string
}

// Open implements Opener.
func (S3Opener) Open(_ context.Context, t Target) (Store, error) {
	u, err := url.Parse(t.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad S3 endpoint %q", t.Endpoint)
	}
	c, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(t.AccessKey, t.SecretKey, ""), Secure: u.Scheme == "https", Region: t.Region})
	if err != nil {
		return nil, err
	}
	return &s3Store{c: c, bucket: t.Bucket}, nil
}

// friendly turns S3 errors into messages a person can act on; it never includes credentials.
func friendly(err error) error {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		switch resp.Code {
		case "NoSuchBucket":
			return fmt.Errorf("the bucket %s doesn't exist at the destination", resp.BucketName)
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return fmt.Errorf("the destination rejected the credentials (%s)", resp.Code)
		case "NoSuchKey":
			return ErrNotFound
		}
		return fmt.Errorf("the destination answered %s: %s", resp.Code, resp.Message)
	}
	return err
}

func (s *s3Store) Put(ctx context.Context, key string, r io.Reader, meta map[string]string) (Object, error) {
	h := newHashingReader(r)
	// Unknown length: minio-go uploads in parts and aborts the multipart upload when the reader fails.
	info, err := s.c.PutObject(ctx, s.bucket, key, h, -1, minio.PutObjectOptions{PartSize: partSize, UserMetadata: meta, ContentType: ContentType(key)})
	if err != nil {
		return Object{}, friendly(err)
	}
	return Object{Key: key, Size: info.Size, SHA256: h.sum(), Modified: info.LastModified}, nil
}

func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if _, err := s.c.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err != nil {
		return nil, friendly(err)
	}
	o, err := s.c.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, friendly(err)
	}
	return o, nil
}

func (s *s3Store) Head(ctx context.Context, key string) (ObjectHead, error) {
	info, err := s.c.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectHead{}, friendly(err)
	}
	meta := make(map[string]string, len(info.UserMetadata))
	for k, v := range info.UserMetadata {
		meta[strings.ToLower(k)] = v
	}
	return ObjectHead{Object: Object{Key: key, Size: info.Size, Modified: info.LastModified}, ContentType: info.ContentType, Meta: meta}, nil
}

func (s *s3Store) Browse(ctx context.Context, prefix, after string, max int) ([]string, []Object, bool, error) {
	var folders []string
	var objects []Object
	cctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops the listing once a page is full
	for o := range s.c.ListObjects(cctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, StartAfter: after}) {
		if o.Err != nil {
			return nil, nil, false, friendly(o.Err)
		}
		if len(folders)+len(objects) == max {
			return folders, objects, true, nil
		}
		if strings.HasSuffix(o.Key, "/") {
			folders = append(folders, o.Key)
		} else {
			objects = append(objects, Object{Key: o.Key, Size: o.Size, Modified: o.LastModified})
		}
	}
	return folders, objects, false, nil
}

func (s *s3Store) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, friendly(o.Err)
		}
		out = append(out, Object{Key: o.Key, Size: o.Size, Modified: o.LastModified})
	}
	return out, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	if strings.HasSuffix(key, "/") {
		return fmt.Errorf("refusing to delete %q", key)
	}
	return friendly(s.c.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}
