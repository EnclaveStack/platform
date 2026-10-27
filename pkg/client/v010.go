package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/enclavestack/platform/pkg/api"
)

// Invoke starts a run of a function with body (any JSON value, or nil) as its event (L3-002 §18.1). With
// wait, it waits up to a minute for the run to end; Status says whether it did.
func (c *Client) Invoke(ctx context.Context, id string, body any, wait bool) (api.RunDetail, error) {
	var out api.RunDetail
	req := &request{method: http.MethodPost, path: "/api/v1/resources/" + url.PathEscape(id) + "/invoke", body: body}
	if wait {
		req.query = url.Values{"wait": {"true"}}
	}
	return out, c.do(ctx, req, &out)
}

// ListPreviews returns an app's previews (L3-002 §18.2).
func (c *Client) ListPreviews(ctx context.Context, id string) ([]api.Preview, error) {
	var out api.PreviewList
	err := c.do(ctx, &request{method: http.MethodGet, path: "/api/v1/resources/" + url.PathEscape(id) + "/previews"}, &out)
	return out.Items, err
}

// CreatePreview makes (or redeploys) a preview of an app from a Git ref, and returns the preview app.
func (c *Client) CreatePreview(ctx context.Context, id string, req api.CreatePreview) (api.Resource, error) {
	var out api.Resource
	return out, c.do(ctx, &request{method: http.MethodPost, path: "/api/v1/resources/" + url.PathEscape(id) + "/previews", body: req}, &out)
}
