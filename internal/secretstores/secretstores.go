// Package secretstores reads secrets from outside secret managers (ADR-041): HashiCorp Vault and OpenBao,
// 1Password Connect, AWS Secrets Manager, Doppler and Infisical, each over plain HTTP. Errors never carry
// the credentials they were called with.
package secretstores

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/sigv4"
	"github.com/enclavestack/platform/pkg/api"
)

// Store reads one secret store.
type Store interface {
	// Fetch returns the value at key, or one field of it.
	Fetch(ctx context.Context, key, field string) (string, error)
	// Test checks the store can be reached and the credentials work (EXT-07).
	Test(ctx context.Context) error
}

// ErrNotFound means the key (or field) doesn't exist in the store.
var ErrNotFound = errors.New("not found in the secret store")

// requestTimeout bounds one call (L3-007 §12); errorBodyLimit bounds what of a refusal reaches a message.
const (
	requestTimeout = 15 * time.Second
	errorBodyLimit = 512
)

// Defaults for providers whose address is fixed.
const (
	DopplerDefault = "https://api.doppler.com"
	AWSEndpoint    = "https://secretsmanager.%s.amazonaws.com"
)

// New builds the client of a store spec; credential is the store's credentials secret's value.
func New(spec api.SecretStoreSpec, credential string, client *http.Client, now func() time.Time) (Store, error) {
	if client == nil {
		client = &http.Client{}
	}
	h := &caller{http: client, scrub: []string{strings.TrimSpace(credential)}}
	credential = strings.TrimSpace(credential)
	switch spec.Provider {
	case api.ProviderVault:
		return &vault{caller: h, spec: *spec.Vault, token: credential}, nil
	case api.ProviderOnePassword:
		return &onePassword{caller: h, spec: *spec.OnePassword, token: credential}, nil
	case api.ProviderAWS:
		var c struct{ AccessKey, SecretKey string }
		if json.Unmarshal([]byte(credential), &c) != nil || c.AccessKey == "" || c.SecretKey == "" {
			return nil, errors.New(`the credentials secret must hold JSON {"accessKey": "…", "secretKey": "…"}`)
		}
		h.scrub = []string{c.SecretKey}
		endpoint := spec.AWS.Endpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf(AWSEndpoint, spec.AWS.Region)
		}
		return &aws{caller: h, endpoint: strings.TrimRight(endpoint, "/"), region: spec.AWS.Region, access: c.AccessKey, secret: c.SecretKey, now: now}, nil
	case api.ProviderDoppler:
		endpoint := spec.Doppler.Endpoint
		if endpoint == "" {
			endpoint = DopplerDefault
		}
		return &doppler{caller: h, endpoint: strings.TrimRight(endpoint, "/"), token: credential}, nil
	case api.ProviderInfisical:
		var c struct{ ClientID, ClientSecret string }
		if json.Unmarshal([]byte(credential), &c) != nil || c.ClientID == "" || c.ClientSecret == "" {
			return nil, errors.New(`the credentials secret must hold JSON {"clientId": "…", "clientSecret": "…"}`)
		}
		h.scrub = []string{c.ClientSecret}
		return &infisical{caller: h, spec: *spec.Infisical, id: c.ClientID, secret: c.ClientSecret}, nil
	}
	return nil, fmt.Errorf("unknown provider %q", spec.Provider)
}

// ForSpec builds a store's client from its stored spec, reading its credentials secret with credential.
func ForSpec(raw []byte, credential func(name string) (string, error), client *http.Client, now func() time.Time) (Store, api.SecretStoreSpec, error) {
	norm, _, err := api.NormalizeSpec(api.KindSecretStore, raw)
	if err != nil {
		return nil, api.SecretStoreSpec{}, fmt.Errorf("the store's spec is unreadable: %w", err)
	}
	spec := norm.(api.SecretStoreSpec)
	_, name := spec.CredentialSecret()
	cred, err := credential(name)
	if err != nil {
		return nil, spec, err
	}
	s, err := New(spec, cred, client, now)
	return s, spec, err
}

// caller makes JSON requests and turns refusals into errors without credentials.
type caller struct {
	http  *http.Client
	scrub []string
}

func (c *caller) do(ctx context.Context, method, target string, headers map[string]string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return c.clean(fmt.Errorf("the secret store can't be reached: %w", err))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > errorBodyLimit {
			msg = msg[:errorBodyLimit] + "…"
		}
		return c.clean(fmt.Errorf("the secret store answered %s: %s", res.Status, msg))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("the secret store's answer isn't JSON: %w", err)
	}
	return nil
}

// clean removes every credential from an error's text (L3-006 §9).
func (c *caller) clean(err error) error {
	msg := err.Error()
	for _, s := range c.scrub {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "<credential>")
		}
	}
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

// pick returns a field of a map of values: the named one, or the only one; several without a name is
// an error naming them (ADR-041).
func pick(values map[string]string, field string) (string, error) {
	if field != "" {
		v, ok := values[field]
		if !ok {
			return "", fmt.Errorf("%w: no field %q (fields: %s)", ErrNotFound, field, names(values))
		}
		return v, nil
	}
	if len(values) == 1 {
		for _, v := range values {
			return v, nil
		}
	}
	return "", fmt.Errorf("the secret has several fields (%s): name one", names(values))
}

func names(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// stringMap keeps string values and JSON-encodes the rest.
func stringMap(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = s
			continue
		}
		b, _ := json.Marshal(v)
		out[k] = string(b)
	}
	return out
}

// ─── Vault / OpenBao (KV version 2) ────────────────────────────────────────

type vault struct {
	*caller
	spec  api.VaultStore
	token string
}

func (v *vault) headers() map[string]string {
	h := map[string]string{"X-Vault-Token": v.token}
	if v.spec.Namespace != "" {
		h["X-Vault-Namespace"] = v.spec.Namespace
	}
	return h
}

func (v *vault) Fetch(ctx context.Context, key, field string) (string, error) {
	var out struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	path := v.spec.Address + "/v1/" + escapePath(v.spec.Mount) + "/data/" + escapePath(strings.Trim(key, "/"))
	if err := v.do(ctx, http.MethodGet, path, v.headers(), nil, &out); err != nil {
		return "", err
	}
	return pick(stringMap(out.Data.Data), field)
}

func (v *vault) Test(ctx context.Context) error {
	return v.do(ctx, http.MethodGet, v.spec.Address+"/v1/auth/token/lookup-self", v.headers(), nil, nil)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// ─── 1Password Connect ─────────────────────────────────────────────────────

type onePassword struct {
	*caller
	spec  api.OnePasswordStore
	token string
}

type opRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Title string `json:"title"`
}

func (o *onePassword) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + o.token}
}

// resolve finds the ID of a vault or item by ID or by name.
func (o *onePassword) resolve(ctx context.Context, listURL, filterKey, want string) (string, error) {
	var list []opRef
	q := url.Values{"filter": {fmt.Sprintf(`%s eq "%s"`, filterKey, strings.ReplaceAll(want, `"`, `\"`))}}
	if err := o.do(ctx, http.MethodGet, listURL+"?"+q.Encode(), o.headers(), nil, &list); err != nil {
		return "", err
	}
	if len(list) > 0 {
		return list[0].ID, nil
	}
	return want, nil // not found by name: it may be an ID
}

func (o *onePassword) Fetch(ctx context.Context, key, field string) (string, error) {
	base := o.spec.ConnectURL + "/v1/vaults"
	vaultID, err := o.resolve(ctx, base, "name", o.spec.Vault)
	if err != nil {
		return "", err
	}
	items := base + "/" + url.PathEscape(vaultID) + "/items"
	itemID, err := o.resolve(ctx, items, "title", key)
	if err != nil {
		return "", err
	}
	var item struct {
		Fields []struct {
			ID      string `json:"id"`
			Label   string `json:"label"`
			Purpose string `json:"purpose"`
			Value   string `json:"value"`
		} `json:"fields"`
	}
	if err := o.do(ctx, http.MethodGet, items+"/"+url.PathEscape(itemID), o.headers(), nil, &item); err != nil {
		return "", err
	}
	values := map[string]string{}
	for _, f := range item.Fields {
		if f.Value == "" {
			continue
		}
		if field != "" && (f.ID == field || f.Label == field) {
			return f.Value, nil
		}
		if field == "" && f.Purpose == "PASSWORD" {
			return f.Value, nil
		}
		values[f.Label] = f.Value
	}
	return pick(values, field)
}

func (o *onePassword) Test(ctx context.Context) error {
	_, err := o.resolve(ctx, o.spec.ConnectURL+"/v1/vaults", "name", o.spec.Vault)
	return err
}

// ─── AWS Secrets Manager ───────────────────────────────────────────────────

type aws struct {
	*caller
	endpoint, region, access, secret string
	now                              func() time.Time
}

func (a *aws) call(ctx context.Context, target string, in, out any) error {
	body, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager."+target)
	sigv4.Sign(req, body, a.access, a.secret, a.region, "secretsmanager", a.now())
	res, err := a.http.Do(req)
	if err != nil {
		return a.clean(fmt.Errorf("AWS Secrets Manager can't be reached: %w", err))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		if strings.Contains(e.Type, "ResourceNotFoundException") {
			return ErrNotFound
		}
		return a.clean(fmt.Errorf("AWS Secrets Manager answered %s: %s %s", res.Status, e.Type, e.Message))
	}
	return json.Unmarshal(raw, out)
}

func (a *aws) Fetch(ctx context.Context, key, field string) (string, error) {
	var out struct {
		SecretString *string `json:"SecretString"`
	}
	if err := a.call(ctx, "GetSecretValue", map[string]string{"SecretId": key}, &out); err != nil {
		return "", err
	}
	if out.SecretString == nil {
		return "", errors.New("the secret holds binary data, which EnclaveStack doesn't read")
	}
	if field == "" {
		return *out.SecretString, nil
	}
	var values map[string]any
	if json.Unmarshal([]byte(*out.SecretString), &values) != nil {
		return *out.SecretString, nil // a plain-text value ignores field (ADR-041)
	}
	return pick(stringMap(values), field)
}

func (a *aws) Test(ctx context.Context) error {
	var out map[string]any
	return a.call(ctx, "ListSecrets", map[string]any{"MaxResults": 1}, &out)
}

// ─── Doppler ───────────────────────────────────────────────────────────────

type doppler struct {
	*caller
	endpoint, token string
}

func (d *doppler) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + d.token}
}

func (d *doppler) Fetch(ctx context.Context, key, field string) (string, error) {
	var out struct {
		Value struct {
			Raw      *string `json:"raw"`
			Computed *string `json:"computed"`
		} `json:"value"`
	}
	if err := d.do(ctx, http.MethodGet, d.endpoint+"/v3/configs/config/secret?"+url.Values{"name": {key}}.Encode(), d.headers(), nil, &out); err != nil {
		return "", err
	}
	v := out.Value.Computed
	if v == nil {
		v = out.Value.Raw
	}
	if v == nil {
		return "", ErrNotFound
	}
	return fieldOf(*v, field)
}

func (d *doppler) Test(ctx context.Context) error {
	return d.do(ctx, http.MethodGet, d.endpoint+"/v3/configs/config/secrets/names", d.headers(), nil, nil)
}

// fieldOf reads a field of a JSON object value; without a field, the value itself.
func fieldOf(v, field string) (string, error) {
	if field == "" {
		return v, nil
	}
	var values map[string]any
	if json.Unmarshal([]byte(v), &values) != nil {
		return "", fmt.Errorf("%w: the value isn't a JSON object, so it has no field %q", ErrNotFound, field)
	}
	return pick(stringMap(values), field)
}

// ─── Infisical (universal auth) ────────────────────────────────────────────

type infisical struct {
	*caller
	spec       api.InfisicalStore
	id, secret string
}

func (i *infisical) login(ctx context.Context) (string, error) {
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	body, _ := json.Marshal(map[string]string{"clientId": i.id, "clientSecret": i.secret})
	if err := i.do(ctx, http.MethodPost, i.spec.SiteURL+"/api/v1/auth/universal-auth/login", map[string]string{"Content-Type": "application/json"}, body, &out); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", errors.New("the Infisical sign-in endpoint wasn't found: check the site URL")
		}
		return "", err
	}
	i.scrub = append(i.scrub, out.AccessToken)
	return out.AccessToken, nil
}

func (i *infisical) Fetch(ctx context.Context, key, field string) (string, error) {
	tok, err := i.login(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{"workspaceId": {i.spec.ProjectID}, "environment": {i.spec.Environment}, "secretPath": {i.spec.Path}}
	var out struct {
		Secret struct {
			SecretValue string `json:"secretValue"`
		} `json:"secret"`
	}
	if err := i.do(ctx, http.MethodGet, i.spec.SiteURL+"/api/v3/secrets/raw/"+url.PathEscape(key)+"?"+q.Encode(),
		map[string]string{"Authorization": "Bearer " + tok}, nil, &out); err != nil {
		return "", err
	}
	return fieldOf(out.Secret.SecretValue, field)
}

func (i *infisical) Test(ctx context.Context) error {
	_, err := i.login(ctx)
	return err
}
