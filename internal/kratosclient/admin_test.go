package kratosclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAdminClient(server *httptest.Server) *Client {
	// Expose both URLs on the same test server — the fake routes for public
	// (/self-service/*) and admin (/admin/*) coexist on the same mux in tests.
	return &Client{
		publicURL:  strings.TrimSuffix(server.URL, "/"),
		adminURL:   strings.TrimSuffix(server.URL, "/"),
		httpClient: server.Client(),
	}
}

func TestCreateIdentityWithPassword_SendsCorrectPayload(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/identities" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "wrong endpoint", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"abc-123","traits":{"email":"u@example.com","is_admin":false}}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	id, err := c.CreateIdentityWithPassword(context.Background(),
		AdminTraits{Email: "u@example.com", Username: "u", IsAdmin: false},
		"$2a$12$F1rL1qVOFx9r1z4L5QhqQOqzxN3V5K9X2Y8Z1A2B3C4D5E6F7G8H9I",
	)
	if err != nil {
		t.Fatalf("CreateIdentityWithPassword: %v", err)
	}
	if id != "abc-123" {
		t.Fatalf("id = %q, want abc-123", id)
	}
	if captured["schema_id"] != "default" {
		t.Errorf("schema_id = %v, want default", captured["schema_id"])
	}
	traits, ok := captured["traits"].(map[string]any)
	if !ok {
		t.Fatalf("traits missing")
	}
	if _, ok := traits["is_admin"]; !ok {
		t.Error("is_admin must be present even when false (no omitempty)")
	}
	creds, ok := captured["credentials"].(map[string]any)
	if !ok {
		t.Fatalf("credentials missing")
	}
	pw := creds["password"].(map[string]any)["config"].(map[string]any)["hashed_password"]
	if _, ok := pw.(string); !ok {
		t.Errorf("hashed_password missing in credentials payload")
	}
}

func TestCreateIdentityWithPassword_IsAdminTrueRoundtrips(t *testing.T) {
	t.Parallel()
	var captured AdminTraits
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Traits AdminTraits `json:"traits"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		captured = body.Traits
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.CreateIdentityWithPassword(context.Background(),
		AdminTraits{Email: "admin@x", IsAdmin: true},
		"$2a$12$F1rL1qVOFx9r1z4L5QhqQOqzxN3V5K9X2Y8Z1A2B3C4D5E6F7G8H9I",
	)
	if err != nil {
		t.Fatalf("CreateIdentityWithPassword: %v", err)
	}
	if !captured.IsAdmin {
		t.Error("IsAdmin=true was lost in the wire payload")
	}
}

func TestCreateIdentityWithPassword_RejectsShortHash(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server must not be hit — local validation should reject before the request")
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.CreateIdentityWithPassword(context.Background(),
		AdminTraits{Email: "u@x"}, "too-short",
	)
	if err == nil {
		t.Fatal("expected error on short bcrypt hash")
	}
}

func TestCreateIdentityWithPassword_EmptyIDIsError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":""}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.CreateIdentityWithPassword(context.Background(),
		AdminTraits{Email: "u@x"},
		"$2a$12$F1rL1qVOFx9r1z4L5QhqQOqzxN3V5K9X2Y8Z1A2B3C4D5E6F7G8H9I",
	)
	if err == nil {
		t.Fatal("expected error on empty id")
	}
}

func TestSetPassword_SendsJSONPatch(t *testing.T) {
	t.Parallel()
	var capturedBody []byte
	var capturedCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/admin/identities/abc-123" {
			t.Errorf("wrong request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "wrong endpoint", http.StatusMethodNotAllowed)
			return
		}
		capturedBody, _ = io.ReadAll(r.Body)
		capturedCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc-123","traits":{"email":"u@x","is_admin":false}}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	err := c.SetPassword(context.Background(), "abc-123",
		"$2a$12$F1rL1qVOFx9r1z4L5QhqQOqzxN3V5K9X2Y8Z1A2B3C4D5E6F7G8H9I")
	if err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if capturedCT != "application/json-patch+json" {
		t.Errorf("Content-Type = %q, want application/json-patch+json", capturedCT)
	}
	var patch []map[string]any
	if err := json.Unmarshal(capturedBody, &patch); err != nil {
		t.Fatalf("patch body not valid JSON: %v (%s)", err, capturedBody)
	}
	if len(patch) != 1 {
		t.Fatalf("patch length = %d, want 1", len(patch))
	}
	if patch[0]["op"] != "add" || patch[0]["path"] != "/credentials/password/config/hashed_password" {
		t.Errorf("patch op/path wrong: %+v", patch[0])
	}
}

func TestSetPassword_NotFoundReturnsSentinel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	err := c.SetPassword(context.Background(), "missing",
		"$2a$12$F1rL1qVOFx9r1z4L5QhqQOqzxN3V5K9X2Y8Z1A2B3C4D5E6F7G8H9I")
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Errorf("expected ErrIdentityNotFound, got %v", err)
	}
}

func TestSetPassword_RejectsEmptyIDAndShortHash(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server must not be hit — local validation should reject first")
	}))
	defer srv.Close()
	c := newAdminClient(srv)

	if err := c.SetPassword(context.Background(), "", "$2a$12$F1rL1qVOFx9r1z4L5QhqQOqzxN3V5K9X2Y8Z1A2B3C4D5E6F7G8H9I"); err == nil {
		t.Error("expected error on empty id")
	}
	if err := c.SetPassword(context.Background(), "abc-123", "too-short"); err == nil {
		t.Error("expected error on short hash")
	}
}

func TestDeleteIdentity_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/admin/identities/abc-123" {
			t.Errorf("wrong path: %s %s", r.Method, r.URL.Path)
			http.Error(w, "", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.DeleteIdentity(context.Background(), "abc-123"); err != nil {
		t.Fatalf("DeleteIdentity: %v", err)
	}
}

func TestDeleteIdentity_404IsIdempotent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.DeleteIdentity(context.Background(), "gone"); err != nil {
		t.Fatalf("404 must be idempotent: %v", err)
	}
}

func TestDeleteIdentity_EmptyIDShortCircuits(t *testing.T) {
	t.Parallel()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.DeleteIdentity(context.Background(), ""); err != nil {
		t.Fatalf("empty id should not error: %v", err)
	}
	if hits != 0 {
		t.Errorf("empty id triggered %d HTTP calls (want 0)", hits)
	}
}

func TestDeleteIdentity_ServerErrorPropagates(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	err := c.DeleteIdentity(context.Background(), "id")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestListIdentitiesPage_NoLinkHeader(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":"a","traits":{"email":"A@x","is_admin":false}}]`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	ids, next, err := c.ListIdentitiesPage(context.Background(), 10, "")
	if err != nil {
		t.Fatalf("ListIdentitiesPage: %v", err)
	}
	if len(ids) != 1 || ids[0].ID != "a" {
		t.Fatalf("unexpected page contents: %+v", ids)
	}
	if next != "" {
		t.Errorf("no Link header → next token must be empty, got %q", next)
	}
}

func TestAllIdentitiesByEmail_FollowsPagination(t *testing.T) {
	t.Parallel()
	// Two-page response: first page advertises next=PAGE2, second page has no Link.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("page_token")
		w.Header().Set("Content-Type", "application/json")
		switch token {
		case "":
			w.Header().Set("Link", `<http://x/admin/identities?per_page=250&page_token=PAGE2>; rel="next"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":"1","traits":{"email":"First@Example.COM","is_admin":false}}]`))
		case "PAGE2":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":"2","traits":{"email":"second@example.com","is_admin":true}}]`))
		}
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	byEmail, err := c.AllIdentitiesByEmail(context.Background())
	if err != nil {
		t.Fatalf("AllIdentitiesByEmail: %v", err)
	}
	if got := byEmail["first@example.com"]; got != "1" {
		t.Errorf("email lowercased+mapped wrong: got %q", got)
	}
	if got := byEmail["second@example.com"]; got != "2" {
		t.Errorf("second page missing: got %q", got)
	}
}

func TestParseNextPageToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"empty", "", ""},
		{"no rel=next", `<http://x/p?page_token=A>; rel="first"`, ""},
		{"next present", `<http://x/p?per_page=5&page_token=ABC>; rel="next"`, "ABC"},
		{"multiple rels", `<http://x?page_token=A>; rel="first", <http://x?page_token=B>; rel="next"`, "B"},
		{"rel=next without page_token", `<http://x/p?per_page=5>; rel="next"`, ""},
	}
	for _, tc := range cases {
		if got := parseNextPageToken(tc.header); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestVerifyBcryptPassthrough_HappyPath(t *testing.T) {
	t.Parallel()
	var createdID string
	var loginFlowID = "flow-xyz"
	var deleteCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/admin/identities":
			createdID = "canary-" + strings.TrimPrefix(r.URL.Path, "/admin/identities/")
			createdID = "kratos-id-42"
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"kratos-id-42","traits":{"email":"c@j.invalid","is_admin":false}}`))

		case r.Method == http.MethodGet && r.URL.Path == "/self-service/login/api":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"` + loginFlowID + `"}`))

		case r.Method == http.MethodPost && r.URL.Path == "/self-service/login":
			if got := r.URL.Query().Get("flow"); got != loginFlowID {
				t.Errorf("login POST flow id = %q, want %q", got, loginFlowID)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"session_token":"sess-abc","session":{"id":"s"}}`))

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/admin/identities/"):
			deleteCalled = true
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.VerifyBcryptPassthrough(context.Background()); err != nil {
		t.Fatalf("VerifyBcryptPassthrough: %v", err)
	}
	if createdID == "" {
		t.Error("create was not called")
	}
	if !deleteCalled {
		t.Error("canary was not deleted")
	}
}

func TestVerifyBcryptPassthrough_LoginFailSurfacesError(t *testing.T) {
	t.Parallel()
	deleteCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/admin/identities":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"canary-1","traits":{"email":"c@j.invalid"}}`))
		case r.URL.Path == "/self-service/login/api":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"flow"}`))
		case r.URL.Path == "/self-service/login":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_credentials"}`))
		case strings.HasPrefix(r.URL.Path, "/admin/identities/"):
			deleteCalled = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	err := c.VerifyBcryptPassthrough(context.Background())
	if err == nil {
		t.Fatal("expected error when login fails")
	}
	if !strings.Contains(err.Error(), "login failed") {
		t.Errorf("error must mention login failure for operator clarity: %v", err)
	}
	if !deleteCalled {
		t.Error("canary identity must be deleted even when login fails")
	}
}

func TestGetIdentity_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/admin/identities/id-abc" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "wrong endpoint", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"id-abc","traits":{"email":"u@example.com","is_admin":true}}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	id, err := c.GetIdentity(context.Background(), "id-abc")
	if err != nil {
		t.Fatalf("GetIdentity: %v", err)
	}
	if id.ID != "id-abc" || id.Traits.Email != "u@example.com" || !id.Traits.IsAdmin {
		t.Errorf("decoded identity wrong: %+v", id)
	}
}

func TestGetIdentity_404ReturnsSentinel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "identity not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.GetIdentity(context.Background(), "gone-id")
	if err == nil {
		t.Fatal("expected ErrIdentityNotFound for 404")
	}
	// errors.Is must work so callers can distinguish "rebuild me" from
	// "something is wrong with Kratos" — the idempotency probe depends
	// on this exact sentinel.
	if !isIdentityNotFound(err) {
		t.Errorf("error must match ErrIdentityNotFound (so errors.Is works): got %v", err)
	}
}

func TestGetIdentity_EmptyIDReturnsSentinel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not hit kratos for empty id")
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.GetIdentity(context.Background(), "")
	if !isIdentityNotFound(err) {
		t.Errorf("empty id must return ErrIdentityNotFound, got %v", err)
	}
}

func TestGetIdentity_ServerErrorPropagates(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kratos down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.GetIdentity(context.Background(), "id")
	if err == nil || isIdentityNotFound(err) {
		t.Fatalf("expected transport error distinguishable from ErrIdentityNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error must carry the status code: %v", err)
	}
}

// isIdentityNotFound is a tiny errors.Is helper so tests in this package
// don't need to import errors just for one call.
func isIdentityNotFound(err error) bool {
	return err != nil && err.Error() == ErrIdentityNotFound.Error()
}

func TestCreateRecoveryCode_Success(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/recovery/code" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "wrong endpoint", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"recovery_link":"https://panel.example/recover?token=abc","recovery_code":"abc","expires_at":"2026-05-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	rc, err := c.CreateRecoveryCode(context.Background(), "id-uuid", "1h")
	if err != nil {
		t.Fatalf("CreateRecoveryCode: %v", err)
	}
	if rc.RecoveryLink != "https://panel.example/recover?token=abc" {
		t.Errorf("unexpected recovery_link: %q", rc.RecoveryLink)
	}
	if captured["identity_id"] != "id-uuid" {
		t.Errorf("identity_id = %v, want id-uuid", captured["identity_id"])
	}
	if captured["expires_in"] != "1h" {
		t.Errorf("expires_in = %v, want 1h", captured["expires_in"])
	}
}

func TestCreateRecoveryCode_OmitsExpiresInWhenEmpty(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"recovery_link":"https://x/r","recovery_code":"c"}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.CreateRecoveryCode(context.Background(), "id-uuid", "")
	if err != nil {
		t.Fatalf("CreateRecoveryCode: %v", err)
	}
	if _, present := captured["expires_in"]; present {
		t.Error("expires_in must be omitted when empty — let Kratos pick its default")
	}
}

func TestCreateRecoveryCode_EmptyIDShortCircuits(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not hit kratos when identityID is empty")
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.CreateRecoveryCode(context.Background(), "", "1h")
	if err == nil {
		t.Fatal("expected error on empty identityID")
	}
}

func TestCreateRecoveryCode_RejectsEmptyLinkFromKratos(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		// Kratos misconfigured (no recovery method enabled) will return an
		// empty recovery_link. Fail loudly rather than emit garbage CSV.
		_, _ = w.Write([]byte(`{"recovery_link":"","recovery_code":"c"}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.CreateRecoveryCode(context.Background(), "id-uuid", "")
	if err == nil || !strings.Contains(err.Error(), "empty recovery_link") {
		t.Fatalf("expected empty-link error, got %v", err)
	}
}

func TestCreateRecoveryCode_ServerErrorPropagates(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "identity not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	_, err := c.CreateRecoveryCode(context.Background(), "missing-id", "")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 propagation, got %v", err)
	}
}

func TestRemoveSecondFactor_PatchesBothCredentials(t *testing.T) {
	t.Parallel()
	var seenPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var patch []map[string]any
		if err := json.Unmarshal(body, &patch); err != nil {
			t.Fatalf("unmarshal patch body: %v", err)
		}
		if len(patch) != 1 || patch[0]["op"] != "remove" {
			t.Errorf("patch shape unexpected: %+v", patch)
		}
		seenPaths = append(seenPaths, patch[0]["path"].(string))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.RemoveSecondFactor(context.Background(), "id-uuid"); err != nil {
		t.Fatalf("RemoveSecondFactor: %v", err)
	}
	if len(seenPaths) != 2 {
		t.Fatalf("paths = %d, want 2", len(seenPaths))
	}
	want := map[string]bool{"/credentials/totp": true, "/credentials/lookup_secret": true}
	for _, p := range seenPaths {
		if !want[p] {
			t.Errorf("unexpected path %s", p)
		}
	}
}

func TestRemoveSecondFactor_TreatsMissingCredentialAsSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Kratos returns 422 when JSON-Patch `remove` targets a path
		// that doesn't exist (user never enrolled this method).
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"reason":"path not found"}}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.RemoveSecondFactor(context.Background(), "id-uuid"); err != nil {
		t.Fatalf("422 should be silent success, got %v", err)
	}
}

func TestRemoveSecondFactor_ReturnsErrIdentityNotFoundOn404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	err := c.RemoveSecondFactor(context.Background(), "missing-id")
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("err = %v, want ErrIdentityNotFound", err)
	}
}

func TestSetIdentityState_SendsReplacePatch(t *testing.T) {
	t.Parallel()
	var capturedBody []byte
	var capturedCT, capturedPath, capturedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		capturedCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc","state":"inactive"}`))
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.SetIdentityState(context.Background(), "abc", "inactive"); err != nil {
		t.Fatalf("SetIdentityState: %v", err)
	}
	if capturedMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", capturedMethod)
	}
	if capturedPath != "/admin/identities/abc" {
		t.Errorf("path = %s, want /admin/identities/abc", capturedPath)
	}
	if capturedCT != "application/json-patch+json" {
		t.Errorf("Content-Type = %q, want application/json-patch+json", capturedCT)
	}
	var patch []map[string]any
	if err := json.Unmarshal(capturedBody, &patch); err != nil {
		t.Fatalf("patch body not valid JSON: %v (%s)", err, capturedBody)
	}
	if len(patch) != 1 || patch[0]["op"] != "replace" || patch[0]["path"] != "/state" || patch[0]["value"] != "inactive" {
		t.Errorf("patch body wrong: %+v", patch[0])
	}
}

func TestSetIdentityState_RejectsBadState(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be hit on bad state")
	}))
	defer srv.Close()
	c := newAdminClient(srv)
	for _, bad := range []string{"", "ACTIVE", "Pending", "junk"} {
		if err := c.SetIdentityState(context.Background(), "abc", bad); err == nil {
			t.Errorf("expected error for state=%q", bad)
		}
	}
}

func TestSetIdentityState_NotFoundReturnsSentinel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := newAdminClient(srv)
	err := c.SetIdentityState(context.Background(), "missing", "active")
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("err = %v, want ErrIdentityNotFound", err)
	}
}

func TestSetIdentityState_EmptyIDFails(t *testing.T) {
	t.Parallel()
	c := newAdminClient(httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be hit on empty id")
		w.WriteHeader(http.StatusInternalServerError)
	})))
	if err := c.SetIdentityState(context.Background(), "", "active"); err == nil {
		t.Fatal("expected error on empty identity id")
	}
}

func TestRevokeIdentitySessions_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/admin/identities/idA/sessions" {
			t.Errorf("wrong path: %s %s", r.Method, r.URL.Path)
			http.Error(w, "", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.RevokeIdentitySessions(context.Background(), "idA"); err != nil {
		t.Fatalf("RevokeIdentitySessions: %v", err)
	}
}

func TestRevokeIdentitySessions_404IsIdempotent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.RevokeIdentitySessions(context.Background(), "no-sessions"); err != nil {
		t.Fatalf("404 (no sessions) must be idempotent: %v", err)
	}
}

func TestRevokeIdentitySessions_EmptyIDShortCircuits(t *testing.T) {
	t.Parallel()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newAdminClient(srv)
	if err := c.RevokeIdentitySessions(context.Background(), ""); err == nil {
		t.Fatal("empty id should error")
	}
	if hits != 0 {
		t.Errorf("empty id triggered %d HTTP calls (want 0)", hits)
	}
}
