package stalwartadmin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testToken = "s3cret-admin-token"

type jmapRequest struct {
	method string
	args   json.RawMessage
	auth   string
}

// jmapFake is a Stalwart JMAP endpoint. answer returns the method name and
// arguments of the single method response; status other than 0 or 200 is
// sent as a bare HTTP status instead.
type jmapFake struct {
	t      *testing.T
	mu     sync.Mutex
	reqs   []jmapRequest
	answer func(method string, args json.RawMessage) (status int, name string, resp any)
}

func (f *jmapFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/jmap" {
		f.t.Errorf("request %s %s, want POST /jmap", r.Method, r.URL.Path)
	}
	var body struct {
		Using       []string             `json:"using"`
		MethodCalls [][3]json.RawMessage `json:"methodCalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.MethodCalls) != 1 {
		f.t.Errorf("bad JMAP request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var method, callID string
	_ = json.Unmarshal(body.MethodCalls[0][0], &method)
	_ = json.Unmarshal(body.MethodCalls[0][2], &callID)
	f.mu.Lock()
	f.reqs = append(f.reqs, jmapRequest{method: method, args: body.MethodCalls[0][1], auth: r.Header.Get("Authorization")})
	status, name, resp := f.answer(method, body.MethodCalls[0][1])
	f.mu.Unlock()
	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"methodResponses": []any{[]any{name, resp, callID}},
		"sessionState":    "s1",
	})
}

// requests returns the calls Stalwart received so far.
func (f *jmapFake) requests() []jmapRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jmapRequest(nil), f.reqs...)
}

// newTestClient returns a Client whose token file holds testToken and whose
// Stalwart is fake.
func newTestClient(t *testing.T, answer func(method string, args json.RawMessage) (int, string, any)) (*Client, *jmapFake) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stalwart-admin.token")
	if err := os.WriteFile(path, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &jmapFake{t: t, answer: answer}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &Client{URL: srv.URL, TokenPath: path, HTTP: srv.Client()}, f
}

// ok answers every call with the same arguments under the called method.
func ok(resp any) func(string, json.RawMessage) (int, string, any) {
	return func(m string, _ json.RawMessage) (int, string, any) { return 200, m, resp }
}

func basic(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:"+token))
}

func TestClient_AuthenticatesAsAdminWithTheTokenFile(t *testing.T) {
	c, f := newTestClient(t, ok(map[string]any{"ids": []string{}}))
	if _, err := c.Query(context.Background(), "MtaOutboundThrottle", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.requests()[0].auth; got != basic(testToken) {
		t.Fatalf("Authorization = %q, want Basic admin:<token>", got)
	}
}

// A rotation rewrites the token file; the next call must use the new token
// without a panel restart.
func TestClient_ReadsTheTokenOnEveryCall(t *testing.T) {
	c, f := newTestClient(t, ok(map[string]any{"ids": []string{}}))
	if _, err := c.Query(context.Background(), "MtaOutboundThrottle", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.TokenPath, []byte("rotated-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(context.Background(), "MtaOutboundThrottle", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.requests()[1].auth; got != basic("rotated-token") {
		t.Fatalf("second call Authorization = %q, want the rotated token", got)
	}
}

func TestClient_UnreadableOrEmptyTokenFailsBeforeCalling(t *testing.T) {
	c, f := newTestClient(t, ok(map[string]any{"ids": []string{}}))
	if err := os.WriteFile(c.TokenPath, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(context.Background(), "MtaOutboundThrottle", nil, nil); err == nil {
		t.Fatal("empty token accepted")
	}
	c.TokenPath = filepath.Join(t.TempDir(), "missing")
	if _, err := c.Query(context.Background(), "MtaOutboundThrottle", nil, nil); err == nil {
		t.Fatal("missing token file accepted")
	}
	if len(f.requests()) != 0 {
		t.Fatalf("Stalwart called without a token: %v", f.requests())
	}
}

func TestClient_Query_QueriesThenGetsTheProperties(t *testing.T) {
	c, f := newTestClient(t, func(m string, args json.RawMessage) (int, string, any) {
		switch m {
		case "x:DmarcExternalReport/query":
			return 200, m, map[string]any{"ids": []string{"a1", "b2"}}
		case "x:DmarcExternalReport/get":
			return 200, m, map[string]any{"list": []any{
				map[string]any{"id": "a1", "receivedAt": "2026-09-28T22:10:21Z"},
				map[string]any{"id": "b2", "receivedAt": "2026-09-28T22:11:00Z"},
			}, "notFound": []string{}}
		}
		return 200, "error", map[string]any{"type": "unknownMethod"}
	})
	objs, err := c.Query(context.Background(), "DmarcExternalReport", map[string]any{"domain": "example.com"}, []string{"receivedAt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || !strings.Contains(string(objs[1]), "b2") {
		t.Fatalf("objs = %s", objs)
	}
	if got := string(f.requests()[0].args); got != `{"filter":{"domain":"example.com"}}` {
		t.Errorf("query args = %s", got)
	}
	if got := string(f.requests()[1].args); got != `{"ids":["a1","b2"],"properties":["receivedAt"]}` {
		t.Errorf("get args = %s", got)
	}
}

func TestClient_Query_NoMatchesIsNoObjectsAndNoGet(t *testing.T) {
	c, f := newTestClient(t, ok(map[string]any{"ids": []string{}}))
	objs, err := c.Query(context.Background(), "DmarcExternalReport", nil, nil)
	if err != nil || objs == nil || len(objs) != 0 {
		t.Fatalf("objs = %v, err = %v; want an empty, non-nil slice", objs, err)
	}
	if len(f.requests()) != 1 || string(f.requests()[0].args) != `{}` {
		t.Fatalf("requests = %v, want one unfiltered query", f.requests())
	}
}

func TestClient_RejectsBadInputBeforeCalling(t *testing.T) {
	c, f := newTestClient(t, ok(map[string]any{}))
	ctx := context.Background()
	if _, err := c.Query(ctx, "Principal/set", nil, nil); err == nil {
		t.Error("type with a slash accepted")
	}
	if _, err := c.Query(ctx, "dmarc", nil, nil); err == nil {
		t.Error("lowercase type accepted")
	}
	if _, err := c.Query(ctx, "DmarcExternalReport", map[string]any{"bad key": 1}, nil); err == nil {
		t.Error("bad filter key accepted")
	}
	if _, err := c.Query(ctx, "DmarcExternalReport", nil, []string{""}); err == nil {
		t.Error("empty property accepted")
	}
	for _, id := range []string{"", "-rf", "a/b", "a b", "../etc/passwd", strings.Repeat("a", 65)} {
		if _, err := c.Get(ctx, "MtaOutboundThrottle", id); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
	if len(f.requests()) != 0 {
		t.Fatalf("Stalwart called for rejected input: %v", f.requests())
	}
}

// A wrong token must be an error that says so, never "not found": the
// throttle code would otherwise create a duplicate or report a delete done.
func TestClient_RejectedTokenIsAnErrorNotNotFound(t *testing.T) {
	c, _ := newTestClient(t, func(string, json.RawMessage) (int, string, any) { return http.StatusUnauthorized, "", nil })
	_, err := c.Get(context.Background(), "MtaOutboundThrottle", "abc")
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v; want a 401 error", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token leaked into error: %v", err)
	}
}

func TestClient_JMAPErrorResponseIsAnError(t *testing.T) {
	c, _ := newTestClient(t, func(string, json.RawMessage) (int, string, any) {
		return 200, "error", map[string]any{"type": "unknownMethod", "description": "x:MtaOutboundThrottle/get"}
	})
	_, err := c.Get(context.Background(), "MtaOutboundThrottle", "abc")
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "unknownMethod") {
		t.Fatalf("err = %v; want the JMAP error", err)
	}
}

func TestClient_ResponseForAnotherMethodIsAnError(t *testing.T) {
	c, _ := newTestClient(t, func(string, json.RawMessage) (int, string, any) {
		return 200, "x:Other/get", map[string]any{"list": []any{}}
	})
	if _, err := c.Get(context.Background(), "MtaOutboundThrottle", "abc"); err == nil {
		t.Fatal("response for another method accepted")
	}
}

// How Stalwart reports an id it does not have (pinned on .60).
func TestClient_MissingIDIsErrNotFound(t *testing.T) {
	c, _ := newTestClient(t, func(m string, _ json.RawMessage) (int, string, any) {
		switch m {
		case "x:MtaOutboundThrottle/get":
			return 200, m, map[string]any{"list": []any{}, "notFound": []string{"zzz"}}
		default: // set
			return 200, m, map[string]any{
				"notUpdated":   map[string]any{"zzz": map[string]any{"type": "notFound"}},
				"notDestroyed": map[string]any{"zzz": map[string]any{"type": "notFound"}},
			}
		}
	})
	ctx := context.Background()
	if _, err := c.Get(ctx, "MtaOutboundThrottle", "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if err := c.Update(ctx, "MtaOutboundThrottle", "zzz", map[string]any{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update: %v", err)
	}
	if err := c.Delete(ctx, "MtaOutboundThrottle", "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete: %v", err)
	}
}

func TestClient_GetWithNeitherObjectNorNotFoundIsAnError(t *testing.T) {
	c, _ := newTestClient(t, ok(map[string]any{"list": []any{}, "notFound": []string{}}))
	_, err := c.Get(context.Background(), "MtaOutboundThrottle", "zzz")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v; want a plain error", err)
	}
}

func TestClient_OtherSetFailuresAreErrors(t *testing.T) {
	c, _ := newTestClient(t, ok(map[string]any{
		"notCreated":   map[string]any{"c": map[string]any{"type": "invalidPatch", "description": "Invalid value type for object", "properties": []string{"rate"}}},
		"notUpdated":   map[string]any{"abc": map[string]any{"type": "forbidden"}},
		"notDestroyed": map[string]any{"abc": map[string]any{"type": "forbidden"}},
	}))
	ctx := context.Background()
	if _, err := c.Create(ctx, "MtaOutboundThrottle", map[string]any{}); err == nil || !strings.Contains(err.Error(), "invalidPatch") || !strings.Contains(err.Error(), "rate") {
		t.Errorf("create: %v", err)
	}
	if err := c.Update(ctx, "MtaOutboundThrottle", "abc", map[string]any{}); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("update: %v", err)
	}
	if err := c.Delete(ctx, "MtaOutboundThrottle", "abc"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("delete: %v", err)
	}
}

// Stalwart explains a validationFailed in validationErrors, not in
// description. The admin sees this text as the row's error, so it must name
// the field and the limit.
func TestClient_ValidationErrorsAreInTheMessage(t *testing.T) {
	c, _ := newTestClient(t, ok(map[string]any{
		"notCreated": map[string]any{"c": map[string]any{
			"type":             "validationFailed",
			"validationErrors": []any{map[string]any{"type": "MaxValue", "property": "count", "required": 1000000}},
		}},
	}))
	_, err := c.Create(context.Background(), "MtaOutboundThrottle", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "count") || !strings.Contains(err.Error(), "1000000") {
		t.Fatalf("err = %v; want the property and the limit", err)
	}
}

func TestClient_UnconfirmedWritesAreErrors(t *testing.T) {
	c, _ := newTestClient(t, ok(map[string]any{}))
	ctx := context.Background()
	if _, err := c.Create(ctx, "MtaOutboundThrottle", map[string]any{}); err == nil {
		t.Error("create without an id accepted")
	}
	if err := c.Update(ctx, "MtaOutboundThrottle", "abc", map[string]any{}); err == nil {
		t.Error("update without confirmation accepted")
	}
	if err := c.Delete(ctx, "MtaOutboundThrottle", "abc"); err == nil {
		t.Error("delete without confirmation accepted")
	}
	if err := c.ReloadSettings(ctx); err == nil {
		t.Error("reload without confirmation accepted")
	}
}

func TestClient_Create_ReturnsTheAssignedID(t *testing.T) {
	c, f := newTestClient(t, ok(map[string]any{"created": map[string]any{"c": map[string]any{"id": "jg3dhs22amqa"}}}))
	id, err := c.Create(context.Background(), "MtaOutboundThrottle", map[string]any{"x": 1})
	if err != nil || id != "jg3dhs22amqa" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if f.requests()[0].method != "x:MtaOutboundThrottle/set" || string(f.requests()[0].args) != `{"create":{"c":{"x":1}}}` {
		t.Fatalf("request = %s %s", f.requests()[0].method, f.requests()[0].args)
	}
}

func TestClient_Create_RejectsAnIDThatCouldNotBeUsedAgain(t *testing.T) {
	c, _ := newTestClient(t, ok(map[string]any{"created": map[string]any{"c": map[string]any{"id": "../x"}}}))
	if _, err := c.Create(context.Background(), "MtaOutboundThrottle", map[string]any{}); err == nil {
		t.Fatal("invalid created id accepted")
	}
}

func TestClient_UpdateAndDelete_SendTheID(t *testing.T) {
	c, f := newTestClient(t, ok(map[string]any{"updated": map[string]any{"abc": nil}, "destroyed": []string{"abc"}}))
	ctx := context.Background()
	if err := c.Update(ctx, "MtaOutboundThrottle", "abc", map[string]any{"enable": true}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "MtaOutboundThrottle", "abc"); err != nil {
		t.Fatal(err)
	}
	if got := string(f.requests()[0].args); got != `{"update":{"abc":{"enable":true}}}` {
		t.Errorf("update args = %s", got)
	}
	if got := string(f.requests()[1].args); got != `{"destroy":["abc"]}` {
		t.Errorf("destroy args = %s", got)
	}
}

func TestClient_Get_ReturnsTheObject(t *testing.T) {
	c, f := newTestClient(t, ok(map[string]any{"list": []any{map[string]any{"id": "singleton", "enable": true}}, "notFound": []string{}}))
	raw, err := c.Get(context.Background(), "ReportSettings", "singleton")
	if err != nil || !strings.Contains(string(raw), `"enable":true`) {
		t.Fatalf("raw = %s, err = %v", raw, err)
	}
	if f.requests()[0].method != "x:ReportSettings/get" || string(f.requests()[0].args) != `{"ids":["singleton"]}` {
		t.Fatalf("request = %s %s", f.requests()[0].method, f.requests()[0].args)
	}
}

func TestClient_ReloadSettings(t *testing.T) {
	c, f := newTestClient(t, ok(map[string]any{"created": map[string]any{"reload": map[string]any{"id": "bvlwbvp"}}}))
	if err := c.ReloadSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.requests()[0].method != "x:Action/set" || string(f.requests()[0].args) != `{"create":{"reload":{"@type":"ReloadSettings"}}}` {
		t.Fatalf("request = %s %s", f.requests()[0].method, f.requests()[0].args)
	}
}
