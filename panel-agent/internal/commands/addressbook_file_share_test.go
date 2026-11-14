package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAddressbookACLForLevel(t *testing.T) {
	if r := addressbookACLForLevel("read"); !r["mayRead"] || r["mayWrite"] || r["mayDelete"] {
		t.Errorf("read = %v", r)
	}
	if r := addressbookACLForLevel("readwrite"); !r["mayRead"] || !r["mayWrite"] || r["mayShare"] || r["mayDelete"] {
		t.Errorf("readwrite = %v", r)
	}
	if r := addressbookACLForLevel("admin"); !r["mayWrite"] || !r["mayShare"] || !r["mayDelete"] {
		t.Errorf("admin = %v", r)
	}
	if r := addressbookACLForLevel("bogus"); len(r) != 1 || !r["mayRead"] {
		t.Errorf("unknown must be read-only, got %v", r)
	}
}

func TestFileACLForLevel(t *testing.T) {
	if r := fileACLForLevel("read"); !r["mayRead"] || r["mayAddChildren"] || r["mayDelete"] {
		t.Errorf("read = %v", r)
	}
	rw := fileACLForLevel("readwrite")
	for _, k := range []string{"mayRead", "mayAddChildren", "mayModifyContent", "mayRename"} {
		if !rw[k] {
			t.Errorf("readwrite missing %s: %v", k, rw)
		}
	}
	if rw["mayDelete"] || rw["mayShare"] {
		t.Errorf("readwrite must not grant delete/share: %v", rw)
	}
	if r := fileACLForLevel("admin"); !r["mayDelete"] || !r["mayShare"] {
		t.Errorf("admin = %v", r)
	}
}

func TestShareSetHandlers_Validation(t *testing.T) {
	for name, h := range map[string]func(context.Context, json.RawMessage) (any, error){
		"addressbook": addressbookShareSetHandler,
		"file":        fileShareSetHandler,
	} {
		if _, err := h(context.Background(), json.RawMessage(``)); err == nil {
			t.Errorf("%s: empty params should error", name)
		}
		if _, err := h(context.Background(), json.RawMessage(`{"owner_email":"bad"}`)); err == nil {
			t.Errorf("%s: invalid email should error", name)
		}
	}
}

func TestShareSetHandlers_Registered(t *testing.T) {
	for _, cmd := range []string{"addressbook.share_set", "file.share_set"} {
		if _, ok := Default.handlers[cmd]; !ok {
			t.Errorf("%s not registered", cmd)
		}
	}
}

// Stalwart 1.0 refuses a FileNode create without "@type" (seen live:
// "@type is required on creation"). A host with no folder yet must get one
// created as a Directory, or nothing is ever shared.
func TestEnsureRootFileNode_CreatesDirectory(t *testing.T) {
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.MethodCalls) != 1 {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var name string
		_ = json.Unmarshal(req.MethodCalls[0][0], &name)
		var args struct {
			Create map[string]map[string]any `json:"create"`
		}
		_ = json.Unmarshal(req.MethodCalls[0][1], &args)
		var result any
		switch name {
		case "FileNode/get":
			result = map[string]any{"list": []any{}}
		case "FileNode/set":
			created = args.Create["n"]
			if created["@type"] != "Directory" {
				result = map[string]any{"notCreated": map[string]any{"n": map[string]any{
					"type": "invalidProperties", "description": "@type is required on creation.",
				}}}
			} else {
				result = map[string]any{"created": map[string]any{"n": map[string]any{"id": "b"}}}
			}
		default:
			http.Error(w, "unexpected "+name, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"methodResponses": []any{[]any{name, result, "c0"}},
		})
	}))
	t.Cleanup(srv.Close)
	prevURL, prevToken := stalwartAdminURLFunc, stalwartAdminTokenFunc
	stalwartAdminURLFunc = func() string { return srv.URL }
	stalwartAdminTokenFunc = func() (string, error) { return "test-token", nil }
	t.Cleanup(func() { stalwartAdminURLFunc, stalwartAdminTokenFunc = prevURL, prevToken })

	id, err := ensureRootFileNode(context.Background(), "rr", "Scans")
	if err != nil {
		t.Fatalf("ensureRootFileNode: %v", err)
	}
	if id != "b" {
		t.Fatalf("node id = %q, want b", id)
	}
	if created["name"] != "Scans" {
		t.Fatalf("created name = %v, want Scans", created["name"])
	}
}
