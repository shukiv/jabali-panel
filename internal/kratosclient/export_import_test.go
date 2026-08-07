package kratosclient

// GH #954: the export/import pair was doubly broken — export used a
// nonexistent `?export=true` param (credentials silently absent from every
// backup) and import POSTed to /admin/identities/import (a route Kratos
// doesn't have → 405 on every restore). These tests pin the REAL contract,
// verified against a live Kratos v26.2.0: export = list + per-identity
// re-fetch with include_credential=password; import = one
// POST /admin/identities per identity with the credential transformed into
// the create shape, 409 = already-present = skip.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExportIdentities_FetchesCredentials(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/identities":
			// List page: NO credentials here — that's the point.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"id-1","traits":{"email":"a@x.com"}},{"id":"id-2","traits":{"email":"b@x.com"}}]`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/identities/"):
			if r.URL.Query().Get("include_credential") != "password" {
				t.Errorf("per-identity fetch must ask include_credential=password, got %q", r.URL.RawQuery)
			}
			id := strings.TrimPrefix(r.URL.Path, "/admin/identities/")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + id + `","schema_id":"default","state":"active","traits":{"email":"` + id + `@x.com"},"credentials":{"password":{"type":"password","config":{"hashed_password":"$2a$10$hash-` + id + `"}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	exports, err := newAdminClient(srv).ExportIdentities(context.Background())
	if err != nil {
		t.Fatalf("ExportIdentities: %v", err)
	}
	if len(exports) != 2 {
		t.Fatalf("want 2 identities, got %d", len(exports))
	}
	for _, e := range exports {
		if hash := exportedPasswordHash(e.Credentials); !strings.HasPrefix(hash, "$2a$10$hash-") {
			t.Errorf("identity %s: credentials not captured (hash=%q)", e.ID, hash)
		}
	}
}

func TestImportIdentities_CreatesWithTransformedCredential(t *testing.T) {
	t.Parallel()
	var captured []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The ONLY correct route: POST /admin/identities (per identity).
		// /admin/identities/import does not exist in Kratos.
		if r.Method != http.MethodPost || r.URL.Path != "/admin/identities" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "no such route", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var p map[string]any
		_ = json.Unmarshal(body, &p)
		captured = append(captured, p)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"new-id"}`))
	}))
	defer srv.Close()

	ids := []ExportedIdentity{{
		ID:          "src-id",
		SchemaID:    "default",
		State:       "active",
		Traits:      json.RawMessage(`{"email":"a@x.com","username":"a"}`),
		Credentials: json.RawMessage(`{"password":{"type":"password","identifiers":["a@x.com"],"config":{"hashed_password":"$2a$10$abc"}}}`),
	}}
	if err := newAdminClient(srv).ImportIdentities(context.Background(), ids); err != nil {
		t.Fatalf("ImportIdentities: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("want 1 create call, got %d", len(captured))
	}
	p := captured[0]
	// Exported credential shape must be TRANSFORMED into the create shape —
	// only config.hashed_password survives; type/identifiers must not ride.
	creds, _ := p["credentials"].(map[string]any)
	pw, _ := creds["password"].(map[string]any)
	cfg, _ := pw["config"].(map[string]any)
	if cfg["hashed_password"] != "$2a$10$abc" {
		t.Errorf("hashed_password not carried: %v", p["credentials"])
	}
	if _, has := pw["type"]; has {
		t.Error("exported-only field 'type' must not be sent to create")
	}
	if _, has := p["id"]; has {
		t.Error("source identity id must not be sent to create (Kratos assigns)")
	}
}

func TestImportIdentities_NoCredential_OmitsCredentials(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"new-id"}`))
	}))
	defer srv.Close()

	// Old bundle: identity exported before credentials capture — no password.
	ids := []ExportedIdentity{{Traits: json.RawMessage(`{"email":"a@x.com"}`)}}
	if err := newAdminClient(srv).ImportIdentities(context.Background(), ids); err != nil {
		t.Fatalf("ImportIdentities: %v", err)
	}
	if _, has := captured["credentials"]; has {
		t.Error("credential-less export must create WITHOUT a credentials block")
	}
	if captured["schema_id"] != "default" {
		t.Errorf("empty schema_id must default, got %v", captured["schema_id"])
	}
}

func TestImportIdentities_ConflictIsSkip(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":409}}`, http.StatusConflict)
	}))
	defer srv.Close()

	ids := []ExportedIdentity{{Traits: json.RawMessage(`{"email":"a@x.com"}`)}}
	if err := newAdminClient(srv).ImportIdentities(context.Background(), ids); err != nil {
		t.Fatalf("409 must be treated as already-present (restore re-run), got %v", err)
	}
}

func TestIdentityHasPassword(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("include_credential") != "password" {
			t.Errorf("must ask include_credential=password, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/with-pw") {
			_, _ = w.Write([]byte(`{"id":"with-pw","credentials":{"password":{"config":{"hashed_password":"$2a$10$x"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"no-pw","credentials":{}}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if has, err := c.IdentityHasPassword(context.Background(), "with-pw"); err != nil || !has {
		t.Errorf("with-pw: want true, got %v %v", has, err)
	}
	if has, err := c.IdentityHasPassword(context.Background(), "no-pw"); err != nil || has {
		t.Errorf("no-pw: want false, got %v %v", has, err)
	}
	if _, err := c.IdentityHasPassword(context.Background(), ""); err == nil {
		t.Error("empty id must error")
	}
}
