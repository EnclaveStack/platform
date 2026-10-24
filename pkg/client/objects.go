package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/enclavestack/platform/pkg/api"
)

// Bucket objects (L3-002 §14). Uploads and downloads stream and are not retried: a body can be read once.

// checkKey refuses a key the server would refuse; ".." and empty folders must not reach the URL, where
// they would be tidied into another key (L3-002 §14.2).
func checkKey(key string) error {
	if errs := api.ValidateObjectKey(key); len(errs) > 0 {
		return api.Validation(errs)
	}
	return nil
}

// objectPath escapes each folder of a key, keeping the "/" between them.
func objectPath(id, key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return resourcePath(id, "/objects/"+strings.Join(parts, "/"))
}

// ListObjects lists one folder of a bucket; cursor continues from a page's NextCursor.
func (c *Client) ListObjects(ctx context.Context, id, prefix, cursor string) (api.ObjectList, error) {
	q := url.Values{}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out api.ObjectList
	return out, c.do(ctx, &request{method: http.MethodGet, path: resourcePath(id, "/objects"), query: q}, &out)
}

// stream sends one request whose body or answer is a file.
func (c *Client) stream(ctx context.Context, method, path string, body io.Reader, size int64, header http.Header) (*http.Response, error) {
	hreq, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		hreq.ContentLength = size
	}
	c.identify(hreq)
	for k, v := range header {
		hreq.Header[k] = v
	}
	resp, err := c.HTTP.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &NetworkError{URL: c.BaseURL, Err: err}
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, decodeError(resp, b)
	}
	return resp, nil
}

// PutObject uploads size bytes from body to key with tags (size -1 when unknown).
func (c *Client) PutObject(ctx context.Context, id, key string, body io.Reader, size int64, tags map[string]string) (api.ObjectInfo, error) {
	if err := checkKey(key); err != nil {
		return api.ObjectInfo{}, err
	}
	h := http.Header{"Content-Type": {"application/octet-stream"}}
	for k, v := range tags {
		h.Set(api.ObjectTagHeaderPrefix+k, v)
	}
	resp, err := c.stream(ctx, http.MethodPut, objectPath(id, key), body, size, h)
	if err != nil {
		return api.ObjectInfo{}, err
	}
	defer resp.Body.Close()
	var out api.ObjectInfo
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// GetObject downloads an object; the caller closes the body. Info carries its size, media type and tags.
func (c *Client) GetObject(ctx context.Context, id, key string) (api.ObjectInfo, io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return api.ObjectInfo{}, nil, err
	}
	resp, err := c.stream(ctx, http.MethodGet, objectPath(id, key), nil, 0, nil)
	if err != nil {
		return api.ObjectInfo{}, nil, err
	}
	return objectInfo(key, resp), resp.Body, nil
}

// HeadObject returns an object's size, media type and tags without its bytes.
func (c *Client) HeadObject(ctx context.Context, id, key string) (api.ObjectInfo, error) {
	if err := checkKey(key); err != nil {
		return api.ObjectInfo{}, err
	}
	resp, err := c.stream(ctx, http.MethodHead, objectPath(id, key), nil, 0, nil)
	if err != nil {
		return api.ObjectInfo{}, err
	}
	resp.Body.Close()
	return objectInfo(key, resp), nil
}

func objectInfo(key string, resp *http.Response) api.ObjectInfo {
	info := api.ObjectInfo{Key: key, ContentType: resp.Header.Get("Content-Type"), Tags: map[string]string{}}
	info.Size, _ = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		info.Modified = t
	}
	for k, v := range resp.Header {
		if name, ok := strings.CutPrefix(k, api.ObjectTagHeaderPrefix); ok && len(v) > 0 {
			info.Tags[strings.ToLower(name)] = v[0]
		}
	}
	return info
}

// DeleteObject deletes an object; a missing one is already gone.
func (c *Client) DeleteObject(ctx context.Context, id, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	return c.do(ctx, &request{method: http.MethodDelete, path: objectPath(id, key)}, nil)
}
