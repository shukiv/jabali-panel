package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// GH #1701 Slice 3: open_basedir and allow_url_fopen are security-sensitive.
// A package that says nothing leaves them admin-only, so a tenant changing
// either is refused with the directive named and nothing is written.
func TestPHPAdminValues_TenantWithoutPrivilegeIsRefused(t *testing.T) {
	for _, tc := range []struct {
		directive string
		body      map[string]any
	}{
		{"open_basedir", map[string]any{"php_memory_limit": "256M", "php_open_basedir": "{DOCROOT}"}},
		{"allow_url_fopen", map[string]any{"php_memory_limit": "256M", "php_allow_url_fopen": false}},
	} {
		t.Run(tc.directive, func(t *testing.T) {
			f := newPHPPolicyFixture(t, "")
			w := f.patch(t, tc.body)
			if w.Code != http.StatusForbidden {
				t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
			}
			var body struct {
				Directives []string `json:"directives"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if !reflect.DeepEqual(body.Directives, []string{tc.directive}) {
				t.Fatalf("directives = %v, want [%s]", body.Directives, tc.directive)
			}
			if len(f.domains.writes) != 0 {
				t.Fatalf("a refused PATCH must write nothing, got %d writes", len(f.domains.writes))
			}
		})
	}
}

// A tenant_privileged tenant may narrow open_basedir to folders in their own
// home, never reach outside it.
func TestPHPAdminValues_PrivilegedTenantOpenBasedir(t *testing.T) {
	const policy = `{"allow_url_fopen":"tenant_privileged","open_basedir":"tenant_privileged"}`
	for _, tc := range []struct {
		value, want string
		code        int
	}{
		{" {DOCROOT}:{TMP}:{DOCROOT} ", "{DOCROOT}:{TMP}", http.StatusOK},
		{"{DOCROOT}:/home/u1/lib", "{DOCROOT}:/home/u1/lib", http.StatusOK},
		{"{DOCROOT}:/usr/share/php", "", http.StatusBadRequest},
		{"/home/u1x", "", http.StatusBadRequest},
		{"/home/u1/../u2", "", http.StatusBadRequest},
		{"{DOCROOT}\nallow_url_fopen=1", "", http.StatusBadRequest},
	} {
		f := newPHPPolicyFixture(t, policy)
		w := f.patch(t, map[string]any{"php_memory_limit": "256M", "php_open_basedir": tc.value, "php_allow_url_fopen": false})
		if w.Code != tc.code {
			t.Fatalf("%q: want %d, got %d: %s", tc.value, tc.code, w.Code, w.Body.String())
		}
		if tc.code != http.StatusOK {
			if len(f.domains.writes) != 0 || !strings.Contains(w.Body.String(), "open_basedir") {
				t.Fatalf("%q: want a refused write naming open_basedir, got %d writes, body %s", tc.value, len(f.domains.writes), w.Body.String())
			}
			continue
		}
		s := f.domains.writes[0]
		if s.OpenBasedir == nil || *s.OpenBasedir != tc.want || s.AllowURLFopen == nil || *s.AllowURLFopen {
			t.Fatalf("%q: write = open_basedir %v allow_url_fopen %v, want %q and false", tc.value, s.OpenBasedir, s.AllowURLFopen, tc.want)
		}
	}
}

// An admin may add a path outside the owner's home, but never "/" or another
// user's home.
func TestPHPAdminValues_AdminOpenBasedir(t *testing.T) {
	for _, tc := range []struct {
		value string
		code  int
	}{
		{"{DOCROOT}:/usr/share/php", http.StatusOK},
		{"/", http.StatusBadRequest},
		{"/home", http.StatusBadRequest},
		{"{DOCROOT}:/home/u2", http.StatusBadRequest},
	} {
		f := newPHPPolicyFixture(t, "")
		*f.admin = true
		w := f.patch(t, map[string]any{"php_memory_limit": "256M", "php_open_basedir": tc.value})
		if w.Code != tc.code {
			t.Fatalf("%q: want %d, got %d: %s", tc.value, tc.code, w.Code, w.Body.String())
		}
	}
}

// The page sends a locked value back as it is. An admin's path outside the
// tenant's home, sent back unchanged with a permitted change, is neither
// refused by the policy nor re-checked against the tenant's rules.
func TestPHPAdminValues_LockedAdminValueSentBackUnchanged(t *testing.T) {
	f := newPHPPolicyFixture(t, "")
	stored := "{DOCROOT}:/usr/share/php"
	off := false
	d := f.domains.domains["d1"]
	d.PHPOpenBasedir, d.PHPAllowURLFopen = &stored, &off
	w := f.patch(t, map[string]any{"php_memory_limit": "512M", "php_open_basedir": stored, "php_allow_url_fopen": false})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if s := f.domains.writes[0]; s.OpenBasedir == nil || *s.OpenBasedir != stored {
		t.Fatalf("write open_basedir = %v, want the stored %q kept", s.OpenBasedir, stored)
	}
}

// GET returns the stored values and the policy of both directives.
func TestPHPAdminValues_GetReturnsValuesAndPolicy(t *testing.T) {
	f := newPHPPolicyFixture(t, `{"open_basedir":"tenant_privileged"}`)
	stored := "{DOCROOT}:{TMP}"
	off := false
	d := f.domains.domains["d1"]
	d.PHPOpenBasedir, d.PHPAllowURLFopen = &stored, &off
	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/d1/php-settings", nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	var body struct {
		OpenBasedir   *string           `json:"php_open_basedir"`
		AllowURLFopen *bool             `json:"php_allow_url_fopen"`
		Policy        map[string]string `json:"policy"`
		Editable      []string          `json:"editable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.OpenBasedir == nil || *body.OpenBasedir != stored || body.AllowURLFopen == nil || *body.AllowURLFopen {
		t.Fatalf("GET values = %v / %v, want %q / false", body.OpenBasedir, body.AllowURLFopen, stored)
	}
	if body.Policy["open_basedir"] != "tenant_privileged" || body.Policy["allow_url_fopen"] != "admin_only" {
		t.Fatalf("policy = %v", body.Policy)
	}
	editable := strings.Join(body.Editable, ",")
	if !strings.Contains(editable, "open_basedir") || strings.Contains(editable, "allow_url_fopen") {
		t.Fatalf("tenant editable = %v, want open_basedir and not allow_url_fopen", body.Editable)
	}
}
