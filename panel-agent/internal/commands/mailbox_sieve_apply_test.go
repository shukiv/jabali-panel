package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// A stateful fake Stalwart that models the subset of the SieveScript store the
// GH #1795 handler drives: standard SieveScript create/update/destroy with
// onSuccessActivateScript (Stalwart's one-active-script-per-account rule), plus
// the loopback blob upload. It also serves the x:SieveUserScript (legacy) store
// so destroyLegacyUserScripts has somewhere to look.
//
// The point of a stateful fake (vs. per-method canned returns) is that a SECOND
// apply against the same account exercises the UPDATE branch of
// setActiveManagedScript — the path a fresh-box create never hits, and the one a
// real RFC 9661 server could refuse. The fake asserts the update lands cleanly
// (empty NotUpdated) and the account still has exactly one active jabali-managed
// script.

type sieveScriptRec struct {
	id     string
	name   string
	blobID string
	active bool
}

type sieveStore struct {
	mu sync.Mutex
	// standard SieveScripts by id
	scripts map[string]*sieveScriptRec
	// x:SieveUserScript legacy objects by id
	legacy map[string]bool
	// uploaded blob content by blobId
	blobs   map[string]string
	nextID  int
	nextBlb int
}

func newSieveStore() *sieveStore {
	return &sieveStore{
		scripts: map[string]*sieveScriptRec{},
		legacy:  map[string]bool{},
		blobs:   map[string]string{},
	}
}

// activeManaged returns the single active jabali-managed script, or nil.
func (s *sieveStore) activeManaged() *sieveScriptRec {
	for _, r := range s.scripts {
		if r.active && r.name == managedScriptName {
			return r
		}
	}
	return nil
}

func (s *sieveStore) byName(name string) *sieveScriptRec {
	for _, r := range s.scripts {
		if r.name == name {
			return r
		}
	}
	return nil
}

// newSieveFakeServer wires a combined httptest.Server: the JMAP method endpoint
// at /jmap and the blob upload at /jmap/upload/<acct>. Returns the server; the
// caller wireJMAP()s it.
func newSieveFakeServer(t *testing.T, store *sieveStore) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != jmapAdminUser || p == "" {
			http.Error(w, "fake: missing basic auth", http.StatusUnauthorized)
			return
		}
		// Blob upload endpoint.
		if strings.HasPrefix(r.URL.Path, "/jmap/upload/") {
			body := readAllString(r)
			store.mu.Lock()
			store.nextBlb++
			blobID := "blob-" + strconv.Itoa(store.nextBlb)
			store.blobs[blobID] = body
			store.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"blobId": blobID, "size": len(body)})
			return
		}
		if r.URL.Path != jmapAPIPath {
			http.Error(w, "fake: wrong path "+r.URL.Path, http.StatusNotFound)
			return
		}
		var req jmapRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.MethodCalls) != 1 {
			http.Error(w, "fake: bad body", http.StatusBadRequest)
			return
		}
		call := req.MethodCalls[0]
		raw := toRaw(call.Args)
		result, jmapErr := store.dispatch(call.Name, raw)
		resp := jmapResponseBody{MethodResponses: make([]jmapMethodCall, 1)}
		if jmapErr != nil {
			resp.MethodResponses[0] = jmapMethodCall{Name: "error", Args: jmapErr, CallID: call.CallID}
		} else {
			resp.MethodResponses[0] = jmapMethodCall{Name: call.Name, Args: result, CallID: call.CallID}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func (s *sieveStore) dispatch(method string, raw json.RawMessage) (any, *jmapFakeError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case "x:Domain/query":
		return jmapQueryResult{IDs: []string{"dom-1"}, Total: 1}, nil
	case "x:Account/query":
		return jmapQueryResult{IDs: []string{"acct-1"}, Total: 1}, nil
	case "x:SieveUserScript/get":
		list := []json.RawMessage{}
		for id := range s.legacy {
			list = append(list, json.RawMessage(`{"id":"`+id+`"}`))
		}
		return jmapGetResult{List: list}, nil
	case "x:SieveUserScript/set":
		var a struct {
			Destroy []string `json:"destroy"`
		}
		_ = json.Unmarshal(raw, &a)
		for _, id := range a.Destroy {
			delete(s.legacy, id)
		}
		return jmapSetResult{Destroyed: a.Destroy}, nil
	case "SieveScript/get":
		list := []json.RawMessage{}
		for _, r := range s.scripts {
			list = append(list, json.RawMessage(`{"id":"`+r.id+`","name":"`+r.name+`","isActive":`+boolStr(r.active)+`}`))
		}
		return jmapGetResult{List: list}, nil
	case "SieveScript/set":
		return s.sieveSet(raw)
	}
	return nil, &jmapFakeError{Type: "unknownMethod", Description: method}
}

func (s *sieveStore) sieveSet(raw json.RawMessage) (any, *jmapFakeError) {
	var a struct {
		Create                  map[string]json.RawMessage `json:"create"`
		Update                  map[string]json.RawMessage `json:"update"`
		Destroy                 []string                   `json:"destroy"`
		OnSuccessActivateScript string                     `json:"onSuccessActivateScript"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, &jmapFakeError{Type: "invalidArguments", Description: err.Error()}
	}
	res := jmapSetResult{
		Created: map[string]json.RawMessage{},
		Updated: map[string]json.RawMessage{},
	}
	activateID := a.OnSuccessActivateScript
	for key, rawVal := range a.Create {
		var v struct {
			Name   string `json:"name"`
			BlobID string `json:"blobId"`
		}
		_ = json.Unmarshal(rawVal, &v)
		s.nextID++
		id := "sv-" + strconv.Itoa(s.nextID)
		s.scripts[id] = &sieveScriptRec{id: id, name: v.Name, blobID: v.BlobID}
		res.Created[key] = json.RawMessage(`{"id":"` + id + `"}`)
		// #<createKey> reference in onSuccessActivateScript resolves to this id.
		if activateID == "#"+key {
			activateID = id
		}
	}
	for id, rawVal := range a.Update {
		rec := s.scripts[id]
		if rec == nil {
			return jmapSetResult{NotUpdated: map[string]json.RawMessage{id: json.RawMessage(`{"type":"notFound"}`)}}, nil
		}
		var v struct {
			BlobID string `json:"blobId"`
		}
		_ = json.Unmarshal(rawVal, &v)
		if v.BlobID != "" {
			rec.blobID = v.BlobID
		}
		res.Updated[id] = json.RawMessage(`{}`)
	}
	for _, id := range a.Destroy {
		delete(s.scripts, id)
	}
	res.Destroyed = a.Destroy
	// Activation deactivates every other script (one-active rule).
	if activateID != "" {
		if _, ok := s.scripts[activateID]; ok {
			for _, r := range s.scripts {
				r.active = r.id == activateID
			}
		}
	}
	return res, nil
}

// TestMailboxSieveApply_CreateThenUpdateInPlace is the priority-3 box-parity
// check in a mock: first apply CREATES the composite; a second apply with the
// same desired state must take the UPDATE branch and land cleanly, leaving
// exactly one active jabali-managed script whose blob carries both redirects and
// the vacation action. It also asserts the never-run x:SieveUserScript store is
// left empty.
func TestMailboxSieveApply_CreateThenUpdateInPlace(t *testing.T) {
	store := newSieveStore()
	// Seed a legacy x:SieveUserScript object — the handler must destroy it.
	store.legacy["legacy-1"] = true
	srv := newSieveFakeServer(t, store)
	defer srv.Close()
	wireJMAP(t, srv)

	params := json.RawMessage(`{
		"mailbox_email":"user@example.com",
		"externals":[{"target":"a@out.org","keep_copy":false},{"target":"b@out.org","keep_copy":true}],
		"autoresponder":{"enabled":true,"subject":"Away","text_body":"Back Monday"}
	}`)

	// First apply → create.
	if _, err := mailboxSieveApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	assertSingleActiveManaged(t, store, "after create")
	store.mu.Lock()
	legacyLeft := len(store.legacy)
	blob := store.blobs[store.activeManaged().blobID]
	firstID := store.activeManaged().id
	store.mu.Unlock()
	if legacyLeft != 0 {
		t.Errorf("x:SieveUserScript not emptied: %d objects left", legacyLeft)
	}
	if !strings.Contains(blob, `redirect "a@out.org";`) || !strings.Contains(blob, `redirect :copy "b@out.org";`) {
		t.Errorf("composite missing redirects:\n%s", blob)
	}
	if !strings.Contains(blob, "vacation :mime") {
		t.Errorf("composite missing vacation action:\n%s", blob)
	}

	// Second apply, same state → UPDATE branch, not a second create.
	if _, err := mailboxSieveApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("second apply (update-in-place): %v", err)
	}
	assertSingleActiveManaged(t, store, "after update")
	store.mu.Lock()
	secondID := store.activeManaged().id
	store.mu.Unlock()
	if secondID != firstID {
		t.Errorf("update-in-place created a new script id (%s → %s) instead of updating", firstID, secondID)
	}
}

// TestMailboxSieveApply_EmptyDestroysOnlyJabaliOwned proves the tightened
// destroy scope: with no forwards and no autoresponder the handler removes the
// jabali-managed composite AND the legacy native "vacation" script, but leaves a
// tenant-authored script untouched.
func TestMailboxSieveApply_EmptyDestroysOnlyJabaliOwned(t *testing.T) {
	store := newSieveStore()
	store.scripts["s-managed"] = &sieveScriptRec{id: "s-managed", name: managedScriptName, active: true}
	store.scripts["s-vacation"] = &sieveScriptRec{id: "s-vacation", name: "vacation"}
	store.scripts["s-tenant"] = &sieveScriptRec{id: "s-tenant", name: "my-own-filter"}
	srv := newSieveFakeServer(t, store)
	defer srv.Close()
	wireJMAP(t, srv)

	params := json.RawMessage(`{"mailbox_email":"user@example.com","externals":[],"autoresponder":{"enabled":false}}`)
	if _, err := mailboxSieveApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("empty apply: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.scripts["s-managed"]; ok {
		t.Error("jabali-managed composite not destroyed on empty state")
	}
	if _, ok := store.scripts["s-vacation"]; ok {
		t.Error("legacy native 'vacation' script not destroyed on empty state")
	}
	if _, ok := store.scripts["s-tenant"]; !ok {
		t.Error("tenant-authored script was destroyed — destroy scope too wide")
	}
}

func assertSingleActiveManaged(t *testing.T, store *sieveStore, when string) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	active := 0
	var managedActive bool
	for _, r := range store.scripts {
		if r.active {
			active++
			if r.name == managedScriptName {
				managedActive = true
			}
		}
	}
	if active != 1 {
		t.Errorf("%s: expected exactly 1 active script, got %d", when, active)
	}
	if !managedActive {
		t.Errorf("%s: the active script is not %q", when, managedScriptName)
	}
	if store.byName(managedScriptName) == nil {
		t.Errorf("%s: no %q script present", when, managedScriptName)
	}
}

// --- small helpers ---------------------------------------------------

func readAllString(r *http.Request) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

func toRaw(v any) json.RawMessage {
	if rm, ok := v.(json.RawMessage); ok {
		return rm
	}
	b, _ := json.Marshal(v)
	return b
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
