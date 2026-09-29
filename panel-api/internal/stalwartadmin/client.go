// Package stalwartadmin talks to Stalwart's management API: JMAP on the
// loopback admin port, with Stalwart's config objects as x:<Type> methods
// (x:MtaOutboundThrottle/get, …/set, x:Action/set). The throttle reconciler
// (M47 Wave 3) writes MtaOutboundThrottle objects through it.
//
// Auth is HTTP Basic as "admin" with the token in
// /etc/jabali-panel/stalwart-admin.token (0640 jabali:jabali-mail), the
// panel's Stalwart management credential (ADR-0103, ADR-0142), as for
// mailscan. The token is read on every call, so a rotation
// (mail.admin_cred.manage) takes effect without a panel restart.
//
// It speaks HTTP in-process rather than running stalwart-cli because the
// panel's AppArmor profile does not let it exec stalwart-cli. The request
// and response shapes below were checked against Stalwart on the .60 test
// box. The agent (mailbox_jmap.go) and mailscan carry their own JMAP
// clients; see the consolidation TODO in mailscan/client.go.
package stalwartadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// DefaultURL is the loopback admin HTTP endpoint Stalwart binds (per
// M25 unix-socket lockdown — 127.0.0.1 only, no public exposure).
const DefaultURL = "http://127.0.0.1:8446"

// DefaultTokenPath holds the admin token. JABALI_STALWART_ADMIN_TOKEN_PATH
// overrides it, as for mailscan.
const DefaultTokenPath = "/etc/jabali-panel/stalwart-admin.token"

const envTokenPath = "JABALI_STALWART_ADMIN_TOKEN_PATH"

// adminUser is the Basic-auth user the token belongs to.
const adminUser = "admin"

// DefaultTimeout caps each request. 30s leaves headroom on a slow admin
// port without stranding a reconcile tick.
const DefaultTimeout = 30 * time.Second

const jmapPath = "/jmap"

// maxResponseBytes bounds a response body read into memory.
const maxResponseBytes = 8 << 20

// getBatch is how many ids one x:<Type>/get asks for (Stalwart's
// maxObjectsInGet is 500).
const getBatch = 256

// queryPage is how many ids one x:<Type>/query asks for; maxQueryIDs stops a
// runaway listing.
const (
	queryPage   = 1000
	maxQueryIDs = 100_000
)

var jmapUsing = []string{"urn:ietf:params:jmap:core", "urn:stalwart:jmap"}

// ErrNotFound marks a get, update or delete of an id Stalwart does not have.
var ErrNotFound = errors.New("stalwart object not found")

// Client calls Stalwart's management API. Construct via NewClient.
type Client struct {
	// URL is the admin HTTP endpoint (no trailing slash). Default: DefaultURL.
	URL string
	// TokenPath is read on every call. Default: DefaultTokenPath.
	TokenPath string
	// HTTP sends the requests. Default: a client with DefaultTimeout.
	HTTP *http.Client
}

// NewClient returns a Client for the local Stalwart. It reads nothing yet:
// the token is read on every call.
func NewClient() *Client {
	path := os.Getenv(envTokenPath)
	if path == "" {
		path = DefaultTokenPath
	}
	return &Client{
		URL:       DefaultURL,
		TokenPath: path,
		HTTP:      &http.Client{Timeout: DefaultTimeout},
	}
}

// Query returns the objects of typeName that match filter (nil: all of
// them). properties names the fields to return (nil: every field); the id
// is always included.
func (c *Client) Query(ctx context.Context, typeName string, filter map[string]any, properties []string) ([]json.RawMessage, error) {
	for _, p := range properties {
		if err := validateField(p); err != nil {
			return nil, err
		}
	}
	ids, err := c.QueryIDs(ctx, typeName, filter)
	if err != nil {
		return nil, err
	}
	return c.GetMany(ctx, typeName, ids, properties)
}

// QueryIDs returns the ids of every object of typeName that matches filter
// (nil: all of them), paging through Stalwart's query results. An id that
// could not be passed back to Stalwart safely (see validateID) is left out.
func (c *Client) QueryIDs(ctx context.Context, typeName string, filter map[string]any) ([]string, error) {
	if err := validateTypeName(typeName); err != nil {
		return nil, err
	}
	for k := range filter {
		if err := validateField(k); err != nil {
			return nil, err
		}
	}
	ids := []string{}
	listed := 0
	for {
		qargs := map[string]any{"position": listed, "limit": queryPage, "calculateTotal": true}
		if filter != nil {
			qargs["filter"] = filter
		}
		var qr struct {
			IDs   []string `json:"ids"`
			Total *int     `json:"total"`
		}
		if err := c.call(ctx, "x:"+typeName+"/query", qargs, &qr); err != nil {
			return nil, err
		}
		listed += len(qr.IDs)
		if listed > maxQueryIDs {
			return nil, fmt.Errorf("stalwartadmin: %s/query: more than %d objects", typeName, maxQueryIDs)
		}
		for _, id := range qr.IDs {
			if validateID(id) == nil {
				ids = append(ids, id)
			}
		}
		done := len(qr.IDs) == 0 || len(qr.IDs) < queryPage
		if qr.Total != nil {
			done = len(qr.IDs) == 0 || listed >= *qr.Total
		}
		if done {
			return ids, nil
		}
	}
}

// GetMany fetches the objects with the given ids. properties names the
// fields to return (nil: every field); the id is always included. An id
// Stalwart no longer has is left out.
func (c *Client) GetMany(ctx context.Context, typeName string, ids, properties []string) ([]json.RawMessage, error) {
	if err := validateTypeName(typeName); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := validateID(id); err != nil {
			return nil, err
		}
	}
	for _, p := range properties {
		if err := validateField(p); err != nil {
			return nil, err
		}
	}
	objs := []json.RawMessage{}
	for start := 0; start < len(ids); start += getBatch {
		gargs := map[string]any{"ids": ids[start:min(start+getBatch, len(ids))]}
		if properties != nil {
			gargs["properties"] = properties
		}
		var gr getResult
		if err := c.call(ctx, "x:"+typeName+"/get", gargs, &gr); err != nil {
			return nil, err
		}
		objs = append(objs, gr.List...)
	}
	return objs, nil
}

// Get fetches one object by id, with every field. It returns ErrNotFound
// when Stalwart has no such object.
func (c *Client) Get(ctx context.Context, typeName, id string) (json.RawMessage, error) {
	if err := validateTypeName(typeName); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	var gr getResult
	if err := c.call(ctx, "x:"+typeName+"/get", map[string]any{"ids": []string{id}}, &gr); err != nil {
		return nil, err
	}
	for _, raw := range gr.List {
		var o struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &o) == nil && o.ID == id {
			return raw, nil
		}
	}
	if slices.Contains(gr.NotFound, id) {
		return nil, ErrNotFound
	}
	return nil, fmt.Errorf("stalwartadmin: %s/get returned neither %s nor notFound", typeName, id)
}

// Create makes a new object and returns the id Stalwart assigned.
func (c *Client) Create(ctx context.Context, typeName string, payload any) (string, error) {
	if err := validateTypeName(typeName); err != nil {
		return "", err
	}
	var sr setResult
	if err := c.call(ctx, "x:"+typeName+"/set", map[string]any{"create": map[string]any{"c": payload}}, &sr); err != nil {
		return "", err
	}
	if e, ok := sr.NotCreated["c"]; ok {
		return "", fmt.Errorf("stalwartadmin: create %s: %s", typeName, e)
	}
	var created struct {
		ID string `json:"id"`
	}
	if raw, ok := sr.Created["c"]; !ok || json.Unmarshal(raw, &created) != nil || validateID(created.ID) != nil {
		return "", fmt.Errorf("stalwartadmin: create %s: no valid id in the response", typeName)
	}
	return created.ID, nil
}

// Update replaces the given fields of an existing object. It returns
// ErrNotFound when Stalwart has no such object.
func (c *Client) Update(ctx context.Context, typeName, id string, payload any) error {
	if err := validateTypeName(typeName); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	var sr setResult
	if err := c.call(ctx, "x:"+typeName+"/set", map[string]any{"update": map[string]any{id: payload}}, &sr); err != nil {
		return err
	}
	if e, ok := sr.NotUpdated[id]; ok {
		if e.Type == "notFound" {
			return ErrNotFound
		}
		return fmt.Errorf("stalwartadmin: update %s %s: %s", typeName, id, e)
	}
	if _, ok := sr.Updated[id]; !ok {
		return fmt.Errorf("stalwartadmin: update %s %s: not confirmed", typeName, id)
	}
	return nil
}

// Delete removes one object by id. It returns ErrNotFound when Stalwart has
// no such object.
func (c *Client) Delete(ctx context.Context, typeName, id string) error {
	if err := validateTypeName(typeName); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	var sr setResult
	if err := c.call(ctx, "x:"+typeName+"/set", map[string]any{"destroy": []string{id}}, &sr); err != nil {
		return err
	}
	if e, ok := sr.NotDestroyed[id]; ok {
		if e.Type == "notFound" {
			return ErrNotFound
		}
		return fmt.Errorf("stalwartadmin: delete %s %s: %s", typeName, id, e)
	}
	for _, d := range sr.Destroyed {
		if d == id {
			return nil
		}
	}
	return fmt.Errorf("stalwartadmin: delete %s %s: not confirmed", typeName, id)
}

// ReloadSettings makes changed config objects (throttles among them) take
// effect without restarting Stalwart.
func (c *Client) ReloadSettings(ctx context.Context) error {
	var sr setResult
	args := map[string]any{"create": map[string]any{"reload": map[string]any{"@type": "ReloadSettings"}}}
	if err := c.call(ctx, "x:Action/set", args, &sr); err != nil {
		return err
	}
	if e, ok := sr.NotCreated["reload"]; ok {
		return fmt.Errorf("stalwartadmin: reload settings: %s", e)
	}
	if _, ok := sr.Created["reload"]; !ok {
		return errors.New("stalwartadmin: reload settings: not confirmed")
	}
	return nil
}

type getResult struct {
	List     []json.RawMessage `json:"list"`
	NotFound []string          `json:"notFound"`
}

type setResult struct {
	Created      map[string]json.RawMessage `json:"created"`
	Updated      map[string]json.RawMessage `json:"updated"`
	Destroyed    []string                   `json:"destroyed"`
	NotCreated   map[string]setError        `json:"notCreated"`
	NotUpdated   map[string]setError        `json:"notUpdated"`
	NotDestroyed map[string]setError        `json:"notDestroyed"`
}

// setError is a JMAP SetError (RFC 8620 §5.3). Stalwart explains a
// validationFailed in ValidationErrors, e.g.
// {"type":"MaxValue","property":"count","required":1000000}.
type setError struct {
	Type             string   `json:"type"`
	Description      string   `json:"description"`
	Properties       []string `json:"properties"`
	ValidationErrors []struct {
		Type     string          `json:"type"`
		Property string          `json:"property"`
		Required json.RawMessage `json:"required"`
	} `json:"validationErrors"`
}

func (e setError) String() string {
	s := e.Type
	if e.Description != "" {
		s += ": " + e.Description
	}
	if len(e.Properties) > 0 {
		s += " (" + strings.Join(e.Properties, ", ") + ")"
	}
	for _, v := range e.ValidationErrors {
		s += "; " + v.Property + ": " + v.Type
		if len(v.Required) > 0 {
			s += " " + string(v.Required)
		}
	}
	return s
}

// call sends one JMAP method call and decodes its arguments into out.
func (c *Client) call(ctx context.Context, method string, args, out any) error {
	token, err := c.readToken()
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"using":       jmapUsing,
		"methodCalls": []any{[]any{method, args, "c0"}},
	})
	if err != nil {
		return fmt.Errorf("stalwartadmin: marshal %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+jmapPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("stalwartadmin: %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(adminUser, token)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("stalwartadmin: %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("stalwartadmin: %s: Stalwart rejected the admin token (HTTP 401)", method)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("stalwartadmin: %s: HTTP %d", method, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("stalwartadmin: %s: read response: %w", method, err)
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("stalwartadmin: %s: response larger than %d bytes", method, maxResponseBytes)
	}
	var parsed struct {
		MethodResponses [][3]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("stalwartadmin: %s: unparseable response: %w", method, err)
	}
	if len(parsed.MethodResponses) != 1 {
		return fmt.Errorf("stalwartadmin: %s: %d method responses, want 1", method, len(parsed.MethodResponses))
	}
	mr := parsed.MethodResponses[0]
	var name string
	if err := json.Unmarshal(mr[0], &name); err != nil {
		return fmt.Errorf("stalwartadmin: %s: bad method response name: %w", method, err)
	}
	if name == "error" {
		var e setError
		_ = json.Unmarshal(mr[1], &e)
		return fmt.Errorf("stalwartadmin: %s: JMAP error %s", method, e)
	}
	if name != method {
		return fmt.Errorf("stalwartadmin: %s: response is for %q", method, name)
	}
	if err := json.Unmarshal(mr[1], out); err != nil {
		return fmt.Errorf("stalwartadmin: %s: decode response: %w", method, err)
	}
	return nil
}

func (c *Client) readToken() (string, error) {
	b, err := os.ReadFile(c.TokenPath) //nolint:gosec // operator-owned path; 0640 jabali:jabali-mail
	if err != nil {
		return "", fmt.Errorf("stalwartadmin: read admin token: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("stalwartadmin: admin token at %s is empty", c.TokenPath)
	}
	return token, nil
}

// validateTypeName accepts a Stalwart schema type (CamelCase,
// [A-Z][A-Za-z0-9]*). It becomes part of the method name, so nothing else
// may reach Stalwart as one.
func validateTypeName(t string) error {
	if t == "" {
		return errors.New("stalwartadmin: empty type name")
	}
	first := t[0]
	if !(first >= 'A' && first <= 'Z') {
		return fmt.Errorf("stalwartadmin: type name %q must be CamelCase", t)
	}
	for _, r := range t {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("stalwartadmin: type name %q contains illegal char %q", t, r)
		}
	}
	return nil
}

// validateField accepts a property name: a lowercase letter, then letters
// and digits (e.g. receivedAt).
func validateField(f string) error {
	if f == "" || !(f[0] >= 'a' && f[0] <= 'z') {
		return fmt.Errorf("stalwartadmin: invalid field %q", f)
	}
	for _, r := range f {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("stalwartadmin: invalid field %q", f)
		}
	}
	return nil
}

// validateID accepts anything that looks like a Stalwart id: a short
// alphanumeric token (e.g. "jg1nyykmahqa", "singleton").
func validateID(id string) error {
	if id == "" || len(id) > 64 || id[0] == '-' {
		return fmt.Errorf("stalwartadmin: invalid id %q", id)
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return fmt.Errorf("stalwartadmin: id %q contains illegal char %q", id, r)
		}
	}
	return nil
}
