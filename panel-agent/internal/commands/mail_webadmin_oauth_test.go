package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// fakeOAuthStalwart is an in-memory Stalwart answering x:OAuthClient/get and
// /set the way 1.0.2 does (refusals inside a 200 response).
type fakeOAuthStalwart struct {
	clients      map[string]stalwartOAuthClient
	next         int
	sets         int
	refuseCreate bool
}

func newFakeOAuthStalwart(t *testing.T, seed ...stalwartOAuthClient) *fakeOAuthStalwart {
	t.Helper()
	f := &fakeOAuthStalwart{clients: map[string]stalwartOAuthClient{}}
	for _, c := range seed {
		f.clients[c.ID] = c
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var name string
		_ = json.Unmarshal(req.MethodCalls[0][0], &name)
		var out any
		switch name {
		case "x:OAuthClient/get":
			list := []stalwartOAuthClient{}
			for _, c := range f.clients {
				list = append(list, c)
			}
			out = map[string]any{"list": list}
		case "x:OAuthClient/set":
			f.sets++
			var args struct {
				Create  map[string]stalwartOAuthClient `json:"create"`
				Destroy []string                       `json:"destroy"`
			}
			_ = json.Unmarshal(req.MethodCalls[0][1], &args)
			res := map[string]any{}
			for _, id := range args.Destroy {
				delete(f.clients, id)
			}
			for key, c := range args.Create {
				if f.refuseCreate {
					res["notCreated"] = map[string]any{key: map[string]string{"type": "invalidProperties"}}
					continue
				}
				f.next++
				c.ID = fmt.Sprintf("n%d", f.next)
				f.clients[c.ID] = c
				res["created"] = map[string]any{key: map[string]string{"id": c.ID}}
			}
			out = res
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{name, out, "c0"}}})
	}))
	t.Cleanup(srv.Close)
	prevURL, prevToken := stalwartAdminURLFunc, stalwartAdminTokenFunc
	stalwartAdminURLFunc = func() string { return srv.URL }
	stalwartAdminTokenFunc = func() (string, error) { return "test-token", nil }
	t.Cleanup(func() { stalwartAdminURLFunc, stalwartAdminTokenFunc = prevURL, prevToken })
	return f
}

func (f *fakeOAuthStalwart) ours() []stalwartOAuthClient {
	var out []stalwartOAuthClient
	for _, c := range f.clients {
		if c.Description == webadminOAuthDescription {
			out = append(out, c)
		}
	}
	return out
}

func TestWebadminOAuth_CreatesTheClientForTheDoor(t *testing.T) {
	f := newFakeOAuthStalwart(t)
	if err := syncWebadminOAuthClient(context.Background(), "Panel.Example.com", 8449, true); err != nil {
		t.Fatal(err)
	}
	got := f.ours()
	if len(got) != 1 {
		t.Fatalf("panel clients = %d, want 1", len(got))
	}
	c := got[0]
	if c.ClientID != "https://panel.example.com:8449/admin/oauth-client.json" {
		t.Fatalf("clientId = %q", c.ClientID)
	}
	if !c.RedirectURIs["https://panel.example.com:8449/admin/oauth/callback"] || len(c.RedirectURIs) != 1 {
		t.Fatalf("redirectUris = %v", c.RedirectURIs)
	}
	if !c.PostLogoutRedirectURIs["https://panel.example.com:8449/admin/login"] {
		t.Fatalf("postLogoutRedirectUris = %v", c.PostLogoutRedirectURIs)
	}
}

func TestWebadminOAuth_KeepsAMatchingClient(t *testing.T) {
	cur := webadminOAuthClient("panel.example.com", 8449)
	cur.ID = "a"
	f := newFakeOAuthStalwart(t, cur)
	if err := syncWebadminOAuthClient(context.Background(), "panel.example.com", 8449, true); err != nil {
		t.Fatal(err)
	}
	if f.sets != 0 {
		t.Fatalf("x:OAuthClient/set called %d times for a client already in place", f.sets)
	}
}

// A hostname change leaves the old client behind; a client someone else
// created is never touched.
func TestWebadminOAuth_ReplacesOnlyThePanelsOwnClients(t *testing.T) {
	old := webadminOAuthClient("old.example.com", 8449)
	old.ID = "a"
	sameIDOtherRedirect := webadminOAuthClient("panel.example.com", 8449)
	sameIDOtherRedirect.ID = "b"
	sameIDOtherRedirect.RedirectURIs = map[string]bool{"https://elsewhere.example/cb": true}
	foreign := stalwartOAuthClient{ID: "c", ClientID: "thunderbird", Description: "Thunderbird",
		RedirectURIs: map[string]bool{"http://127.0.0.1/cb": true}}
	f := newFakeOAuthStalwart(t, old, sameIDOtherRedirect, foreign)

	if err := syncWebadminOAuthClient(context.Background(), "panel.example.com", 8449, true); err != nil {
		t.Fatal(err)
	}
	got := f.ours()
	if len(got) != 1 || got[0].ClientID != "https://panel.example.com:8449/admin/oauth-client.json" ||
		!got[0].RedirectURIs["https://panel.example.com:8449/admin/oauth/callback"] {
		t.Fatalf("panel clients = %+v, want only the current one", got)
	}
	if _, ok := f.clients["c"]; !ok {
		t.Fatal("a client the panel did not create was removed")
	}
}

func TestWebadminOAuth_DoorOffRemovesOnlyThePanelsClient(t *testing.T) {
	cur := webadminOAuthClient("panel.example.com", 8449)
	cur.ID = "a"
	foreign := stalwartOAuthClient{ID: "c", ClientID: "thunderbird", Description: "Thunderbird"}
	f := newFakeOAuthStalwart(t, cur, foreign)
	dropWebadminOAuthClient(context.Background())
	if len(f.ours()) != 0 {
		t.Fatalf("panel clients left after the door closed: %+v", f.ours())
	}
	if _, ok := f.clients["c"]; !ok {
		t.Fatal("a client the panel did not create was removed")
	}
}

// Stalwart reports a refused create inside a 200 response; that must fail the
// apply, or the door opens on a sign-in that cannot work.
func TestWebadminOAuth_RefusedCreateFails(t *testing.T) {
	f := newFakeOAuthStalwart(t)
	f.refuseCreate = true
	if err := syncWebadminOAuthClient(context.Background(), "panel.example.com", 8449, true); err == nil {
		t.Fatal("a refused create returned no error")
	}
}

// The bug at the handler's layer: opening the door registers the client its
// sign-in needs, and closing it removes the client.
func TestMailWebadminApply_RegistersAndRemovesTheOAuthClient(t *testing.T) {
	f := newFakeOAuthStalwart(t)
	sa, se := t.TempDir(), t.TempDir()
	oldA, oldE := mailVhostSitesAvailable, mailVhostSitesEnabled
	mailVhostSitesAvailable, mailVhostSitesEnabled = sa, se
	defer func() { mailVhostSitesAvailable, mailVhostSitesEnabled = oldA, oldE }()
	oldH := stalwartAdminHtpasswd
	stalwartAdminHtpasswd = filepath.Join(t.TempDir(), "htpasswd")
	defer func() { stalwartAdminHtpasswd = oldH }()
	oldReload := nginxTestAndReload
	nginxTestAndReload = func(context.Context) error { return nil }
	defer func() { nginxTestAndReload = oldReload }()

	on, _ := json.Marshal(map[string]any{"enabled": true, "server_name": "panel.example.com", "port": 8449,
		"ssl_cert_path": "/etc/jabali/tls/panel.crt", "ssl_key_path": "/etc/jabali/tls/panel.key"})
	if _, err := mailWebadminApplyHandler(context.Background(), on); err != nil {
		t.Fatal(err)
	}
	if got := f.ours(); len(got) != 1 || got[0].ClientID != "https://panel.example.com:8449/admin/oauth-client.json" {
		t.Fatalf("after opening the door, panel clients = %+v", got)
	}
	off, _ := json.Marshal(map[string]any{"enabled": false})
	if _, err := mailWebadminApplyHandler(context.Background(), off); err != nil {
		t.Fatal(err)
	}
	if len(f.ours()) != 0 {
		t.Fatalf("after closing the door, panel clients = %+v", f.ours())
	}
}
