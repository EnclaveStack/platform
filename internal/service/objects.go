package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Bucket objects (L2-013 OBJ-*, L3-002 §14): read and written with the project's object store keys,
// streamed both ways. Events name keys and sizes, never contents.

// bucketStore opens a ready bucket.
func (s *Resources) bucketStore(ctx context.Context, id string) (*domain.Resource, backup.Store, error) {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, notFound()
	}
	if err != nil {
		return nil, nil, err
	}
	if r.Kind != api.KindBucket {
		return nil, nil, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s has no objects; only buckets do.", r.Kind))
	}
	endpoint, _ := r.Outputs["endpoint"].(string)
	if (r.Phase != api.PhaseReady && r.Phase != api.PhaseUpdating) || endpoint == "" {
		return nil, nil, api.NewError(api.CodeActionNotSupported, "Wait until the bucket is ready.")
	}
	if s.Objects == nil {
		return nil, nil, api.NewError(api.CodeActionNotSupported, "This control plane can't reach its object store.")
	}
	raw, err := s.secretOf(ctx, api.DefaultProject, domain.ObjectStoreCredentialsSecret)
	if err != nil {
		return nil, nil, err
	}
	var keys struct{ AccessKey, SecretKey string }
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil, nil, fmt.Errorf("object store keys unreadable: %w", err)
	}
	st, err := s.Objects.Open(ctx, backup.Target{Endpoint: endpoint, Bucket: r.Name, AccessKey: keys.AccessKey, SecretKey: keys.SecretKey})
	if err != nil {
		return nil, nil, objectStoreError(err)
	}
	return r, st, nil
}

func objectStoreError(err error) error {
	if errors.Is(err, backup.ErrNotFound) {
		return api.NewError(api.CodeNotFound, "That object doesn't exist.")
	}
	return api.NewError(api.CodeEngineUnavailable, "The object store couldn't be reached: "+err.Error())
}

// ListObjects lists one folder of a bucket, one page at a time (OBJ-01).
func (s *Resources) ListObjects(ctx context.Context, id, prefix, cursor string, limit int) (api.ObjectList, error) {
	if errs := api.ValidateObjectPrefix(prefix); len(errs) > 0 {
		return api.ObjectList{}, api.Validation(errs)
	}
	if cursor != "" && !strings.HasPrefix(cursor, prefix) {
		return api.ObjectList{}, api.Validation([]api.FieldError{{Field: "cursor", Code: api.FieldInvalidFormat, Message: "Use the nextCursor of the page before."}})
	}
	_, st, err := s.bucketStore(ctx, id)
	if err != nil {
		return api.ObjectList{}, err
	}
	folders, objects, more, err := st.Browse(ctx, prefix, cursor, limit)
	if err != nil {
		return api.ObjectList{}, objectStoreError(err)
	}
	out := api.ObjectList{Prefix: prefix, Folders: append([]string{}, folders...), Items: make([]api.ObjectInfo, 0, len(objects))}
	last := ""
	for _, f := range folders {
		last = max(last, f)
	}
	for _, o := range objects {
		out.Items = append(out.Items, api.ObjectInfo{Key: o.Key, Size: o.Size, Modified: o.Modified.UTC(), ContentType: backup.ContentType(o.Key)})
		last = max(last, o.Key)
	}
	if more {
		out.NextCursor = &last
	}
	return out, nil
}

// PutObject uploads body to key with tags (OBJ-02).
func (s *Resources) PutObject(ctx context.Context, p Principal, id, key string, tags map[string]string, body io.Reader) (api.ObjectInfo, error) {
	errs := append(api.ValidateObjectKey(key), api.ValidateObjectTags(tags)...)
	if len(errs) > 0 {
		return api.ObjectInfo{}, api.Validation(errs)
	}
	r, st, err := s.bucketStore(ctx, id)
	if err != nil {
		return api.ObjectInfo{}, err
	}
	o, err := st.Put(ctx, key, body, tags)
	if err != nil {
		return api.ObjectInfo{}, objectStoreError(err)
	}
	_ = s.Store.AddEvent(ctx, r.ID, "object_uploaded", fmt.Sprintf("Uploaded %s (%s) by %s.", key, api.ByteSize(o.Size), p.Actor()), s.Now())
	s.Log.Info("object uploaded", "resource_id", r.ID, "key", key, "size", o.Size, "by", p.Key())
	return api.ObjectInfo{Key: key, Size: o.Size, Modified: o.Modified.UTC(), ContentType: backup.ContentType(key), Tags: tags}, nil
}

// GetObject opens an object for download (OBJ-03); the caller closes the body.
func (s *Resources) GetObject(ctx context.Context, id, key string) (api.ObjectInfo, io.ReadCloser, error) {
	if errs := api.ValidateObjectKey(key); len(errs) > 0 {
		return api.ObjectInfo{}, nil, api.Validation(errs)
	}
	_, st, err := s.bucketStore(ctx, id)
	if err != nil {
		return api.ObjectInfo{}, nil, err
	}
	h, err := st.Head(ctx, key)
	if err != nil {
		return api.ObjectInfo{}, nil, objectStoreError(err)
	}
	body, err := st.Get(ctx, key)
	if err != nil {
		return api.ObjectInfo{}, nil, objectStoreError(err)
	}
	return api.ObjectInfo{Key: key, Size: h.Size, Modified: h.Modified.UTC(), ContentType: h.ContentType, Tags: h.Meta}, body, nil
}

// DeleteObject deletes an object; a missing one is already gone (OBJ-03).
func (s *Resources) DeleteObject(ctx context.Context, p Principal, id, key string) error {
	if errs := api.ValidateObjectKey(key); len(errs) > 0 {
		return api.Validation(errs)
	}
	r, st, err := s.bucketStore(ctx, id)
	if err != nil {
		return err
	}
	if err := st.Delete(ctx, key); err != nil && !errors.Is(err, backup.ErrNotFound) {
		return objectStoreError(err)
	}
	_ = s.Store.AddEvent(ctx, r.ID, "object_deleted", fmt.Sprintf("Deleted %s by %s.", key, p.Actor()), s.Now())
	s.Log.Info("object deleted", "resource_id", r.ID, "key", key, "by", p.Key())
	return nil
}
