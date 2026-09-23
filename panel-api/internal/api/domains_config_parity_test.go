package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-318 AC5 — HTTP door half of the domain-config validation parity matrix.
//
// The unified-Apply goal is that the HTTP PATCH door and the `domain set` CLI
// door accept and reject exactly the same per-field inputs. Slice 1 already
// unified the ssl-mode protected invariant behind models.SSLModeProtectedRefusal
// after the CLI door was found bypassing it; this matrix is the regression guard
// that keeps every config field in lockstep.
//
// Parity is asserted BY CONSTRUCTION, not by a hand-copied expectation column:
// each row's expected verdict is derived from the SAME api.* validator the door
// is supposed to call (ValidateNginxDirectivesAdmin / ValidateRedirectURL /
// IsValidRedirectType / IsValidIndexPriority). The test then drives the REAL
// handler and asserts its behaviour matches that anchor. If a door ever stops
// calling its validator (the slice-1 bug shape), the behavioural result diverges
// from the anchor and the row reddens. If a validator legitimately evolves (say
// it starts accepting redirect type 303), both the door and the anchor move
// together and the row stays green — which is correct.
//
// The CLI half lives in cmd/server/domain_config_parity_test.go over the SAME
// case values and the SAME api.* anchors, so the two doors are pinned to one
// contract from both sides.
//
// Two deliberate asymmetries are documented rather than "fixed":
//
//   - redirect_all_type friendly aliases: the CLI accepts "permanent"/"temporary"
//     and normalises them to "301"/"302"; the HTTP door accepts only the numeric
//     codes. This is a CLI-input superset, not a drift — both doors PERSIST the
//     identical numeric value. Here those aliases are ordinary reject rows; the
//     CLI file proves the normalisation target is a value this door accepts.
//
//   - cache_enabled has a dedicated CLI flag (`domain set --cache`) but NO
//     general PATCH key — it is only reachable over HTTP through the app-cache
//     flow, not this handler. There is therefore no cache_enabled row: the field
//     is out of the shared matrix by construction, and this asymmetry is called
//     out in the PR body so nobody mistakes its absence for an oversight.

// acceptNginxAdmin etc. mirror each door's ACCEPT predicate by calling the exact
// api.* validator the door calls (empty-string "clear" semantics included where
// the door has them). They are the parity anchor for both the HTTP and CLI files.
func acceptNginxAdmin(v string) bool { return ValidateNginxDirectivesAdmin(v) == "" }

func acceptRedirectTo(v string) bool {
	t := strings.TrimSpace(v)
	return t == "" || ValidateRedirectURL(t) == nil
}

func acceptRedirectType(v string) bool {
	t := strings.TrimSpace(v)
	return t == "" || IsValidRedirectType(t)
}

func acceptIndexPriority(v string) bool { return IsValidIndexPriority(strings.TrimSpace(v)) }

func mustPatchJSON(t *testing.T, field, value string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{field: value})
	if err != nil {
		t.Fatalf("marshal %s body: %v", field, err)
	}
	return string(b)
}

// TestDomainConfig_HTTPParityMatrix drives the PATCH /domains/:id door over the
// shared config matrix and asserts each verdict matches the api.* anchor. A
// rejected config field surfaces as 400 (the validation runs before the apply
// transaction opens); an accepted one as 200.
func TestDomainConfig_HTTPParityMatrix(t *testing.T) {
	fields := []struct {
		name   string
		accept func(string) bool
		values []string
	}{
		{
			name:   "nginx_custom_directives",
			accept: acceptNginxAdmin,
			values: []string{
				`add_header X-Frame-Options "DENY";`, // allowed admin directive
				``,                                   // clears the field
				`root /tmp;`,                         // denylisted directive
				`access_log off;`,                    // value-blocked footgun
			},
		},
		{
			name:   "redirect_all_to",
			accept: acceptRedirectTo,
			values: []string{
				`https://example.com/`, // valid absolute URL
				``,                     // clears the redirect
				`javascript:alert(1)`,  // forbidden scheme
				`https://`,             // no host
				`/relative`,            // no scheme/host
			},
		},
		{
			name:   "redirect_all_type",
			accept: acceptRedirectType,
			values: []string{
				`301`,       // canonical
				`308`,       // canonical
				``,          // clears the type
				`303`,       // not an accepted code
				`bogus`,     // garbage
				`permanent`, // CLI-only alias -> HTTP reject (see file header)
				`temporary`, // CLI-only alias -> HTTP reject
			},
		},
		{
			name:   "index_priority",
			accept: acceptIndexPriority,
			values: []string{
				`php_first`, // valid
				`full`,      // valid
				``,          // empty is NOT a clear here — both doors reject it
				`bogus`,     // invalid
			},
		},
	}

	for _, f := range fields {
		f := f
		t.Run(f.name, func(t *testing.T) {
			// One ordinary domain (not panel-primary, no mail) is enough: every
			// config field is independent of domain state.
			dom := &models.Domain{ID: "d1", UserID: "u1", Name: "example.com", MailProvider: models.MailProviderJabali}
			r, _ := applyTxHarness(t, dom)

			var accepts, rejects int
			for _, v := range f.values {
				want := f.accept(v)
				w := patchDomainBody(r, "d1", mustPatchJSON(t, f.name, v))
				got := w.Code == http.StatusOK
				if got != want {
					t.Errorf("%s=%q: door accept=%v (code %d), anchor accept=%v — HTTP door diverged from its api.* validator",
						f.name, v, got, w.Code, want)
				}
				if !want && w.Code != http.StatusBadRequest {
					t.Errorf("%s=%q: rejected config field must be 400 (pre-transaction), got %d: %s",
						f.name, v, w.Code, w.Body.String())
				}
				if want {
					accepts++
				} else {
					rejects++
				}
			}
			// Vacuity guard: a field whose every row lands on one verdict proves
			// nothing about the door's discrimination.
			if accepts == 0 || rejects == 0 {
				t.Fatalf("%s matrix is vacuous: %d accepts / %d rejects — need at least one of each", f.name, accepts, rejects)
			}
		})
	}
}

// TestDomainConfig_HTTPSSLModeMatrix drives the PATCH door over the full ssl_mode
// input space and closes the behavioural gap slice 1 left: the PATCH door's
// ssl-mode=none refusal was swapped to the shared models.SSLModeProtectedRefusal
// leaf, but only the `ssl disable` DELETE door had behavioural coverage
// (ssl_mode_parity_test.go). The protected refusal lands INSIDE the apply
// transaction, so it surfaces as 422 with the shared code; `custom` is rejected
// pre-leaf as upload-only; `shared` and the other operator modes are accepted.
//
// The CLI door enforces the same ssl_mode contract — models.ValidSSLMode, the
// custom-is-upload-only reject, and the shared models.SSLModeProtectedRefusal
// leaf — but ssl_mode is not part of validateDomainSetInput (cmd/server has no
// injection seam), so CLI ssl_mode parity is source-pinned in
// domain_advanced_cmd_ssl_mode_test.go rather than re-driven here. Both doors
// accept {le, self, none, shared} and reject {custom, invalid} identically.
func TestDomainConfig_HTTPSSLModeMatrix(t *testing.T) {
	cases := []struct {
		name     string
		dom      *models.Domain
		mode     string
		wantCode int
		wantBody string
	}{
		{
			name:     "none on panel primary refused",
			dom:      &models.Domain{ID: "d1", UserID: "u1", Name: "panel.example.com", IsPanelPrimary: true},
			mode:     models.SSLModeNone,
			wantCode: http.StatusUnprocessableEntity,
			wantBody: "ssl_none_panel_primary",
		},
		{
			name:     "none on mail-enabled refused",
			dom:      &models.Domain{ID: "d1", UserID: "u1", Name: "mail.example.com", EmailEnabled: true},
			mode:     models.SSLModeNone,
			wantCode: http.StatusUnprocessableEntity,
			wantBody: "ssl_none_with_email",
		},
		{
			name:     "none on ordinary domain allowed",
			dom:      &models.Domain{ID: "d1", UserID: "u1", Name: "site.example.com", MailProvider: models.MailProviderJabali},
			mode:     models.SSLModeNone,
			wantCode: http.StatusOK,
		},
		{
			name:     "shared on ordinary domain allowed",
			dom:      &models.Domain{ID: "d1", UserID: "u1", Name: "site.example.com", MailProvider: models.MailProviderJabali},
			mode:     models.SSLModeShared,
			wantCode: http.StatusOK,
		},
		{
			name:     "custom rejected as upload-only",
			dom:      &models.Domain{ID: "d1", UserID: "u1", Name: "site.example.com", MailProvider: models.MailProviderJabali},
			mode:     models.SSLModeCustom,
			wantCode: http.StatusBadRequest,
			wantBody: "ssl_mode_custom_via_upload",
		},
		{
			name:     "unknown mode rejected",
			dom:      &models.Domain{ID: "d1", UserID: "u1", Name: "site.example.com", MailProvider: models.MailProviderJabali},
			mode:     "bogus",
			wantCode: http.StatusBadRequest,
			wantBody: "invalid_ssl_mode",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r, _ := applyTxHarness(t, tc.dom)
			w := patchDomainBody(r, "d1", mustPatchJSON(t, "ssl_mode", tc.mode))
			if w.Code != tc.wantCode {
				t.Fatalf("ssl_mode=%q: code = %d, want %d: %s", tc.mode, w.Code, tc.wantCode, w.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(w.Body.String(), tc.wantBody) {
				t.Fatalf("ssl_mode=%q: body must carry %q, got %s", tc.mode, tc.wantBody, w.Body.String())
			}
		})
	}
}
