package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The mail tabs show the API's detail in their error toast (extractApiError).
// Share and forwarder rejections used to send only a code, so the tenant saw
// "already_shared" or a bare HTTP status. Every rejection now carries detail.

func errorDetail(t *testing.T, body []byte) (code, detail string) {
	t.Helper()
	var resp struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("body %s is not JSON: %v", body, err)
	}
	return resp.Error, resp.Detail
}

func TestShareRejections_CarryADetail(t *testing.T) {
	cases := []struct {
		name, method, mbid, shareID, body string
		seed                              bool
		status                            int
		code                              string
	}{
		{"invalid body", http.MethodPost, "alice", "", `{}`, false, http.StatusBadRequest, "invalid_body"},
		{"target in another account", http.MethodPost, "alice", "", `{"shared_with_mailbox_id":"mallory","rights":{"mayRead":true}}`, false, http.StatusBadRequest, "target_not_found"},
		{"self", http.MethodPost, "alice", "", `{"shared_with_mailbox_id":"alice","rights":{"mayRead":true}}`, false, http.StatusBadRequest, "cannot_share_with_self"},
		{"no rights", http.MethodPost, "alice", "", `{"shared_with_mailbox_id":"bob","rights":{}}`, false, http.StatusBadRequest, "rights_required"},
		{"duplicate", http.MethodPost, "alice", "", `{"shared_with_mailbox_id":"bob","rights":{"mayRead":true}}`, true, http.StatusConflict, "already_shared"},
		{"unknown mailbox", http.MethodPost, "nobody", "", `{"shared_with_mailbox_id":"bob","rights":{"mayRead":true}}`, false, http.StatusNotFound, "not_found"},
		{"another tenant's mailbox", http.MethodPost, "mallory", "", `{"shared_with_mailbox_id":"bob","rights":{"mayRead":true}}`, false, http.StatusForbidden, "forbidden"},
		{"delete unknown share", http.MethodDelete, "alice", "nope", "", false, http.StatusNotFound, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newShStore()
			if tc.seed {
				s.shares["s0"] = models.MailboxShare{ID: "s0", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: models.Rights{MayRead: true}}
			}
			w := shareRequest(newShareHandlerFake(s, &shAgent{}), tc.method, tc.mbid, tc.shareID, tc.body, tenantU1)
			code, detail := errorDetail(t, w.Body.Bytes())
			if w.Code != tc.status || code != tc.code {
				t.Fatalf("status %d body %s, want %d %s", w.Code, w.Body.String(), tc.status, tc.code)
			}
			if detail == "" {
				t.Errorf("%s has no detail: %s", code, w.Body.String())
			}
		})
	}
}

func TestForwarderRejections_CarryADetail(t *testing.T) {
	cases := []struct {
		name, body string
		code       string
	}{
		{"invalid body", `{"type":`, "invalid_body"},
		{"invalid type", `{"type":"relay","target":"out@elsewhere.com"}`, "invalid_type"},
		{"alias without address", `{"type":"alias"}`, "alias_requires_local_part"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postForwarderAs(newForwarderHandlerFake(nil), tc.body, tenantU1)
			code, detail := errorDetail(t, w.Body.Bytes())
			if w.Code != http.StatusBadRequest || code != tc.code {
				t.Fatalf("status %d body %s, want 400 %s", w.Code, w.Body.String(), tc.code)
			}
			if detail == "" {
				t.Errorf("%s has no detail: %s", code, w.Body.String())
			}
		})
	}
}

// fwDupForwarders fails Create the way MariaDB does on uq_external_forward.
type fwDupForwarders struct{ fwFakeForwarders }

func (f *fwDupForwarders) Create(context.Context, *models.EmailForwarder) error {
	return errors.New("Error 1062 (23000): Duplicate entry 'mb1-external-out@elsewhere.com' for key 'uq_external_forward'")
}

// A second forwarder to the same target hit the unique key and came back as
// 500 internal, so the tenant saw "Request failed with status code 500".
func TestForwarderCreate_DuplicateIsAConflict(t *testing.T) {
	h := newForwarderHandlerFake(nil)
	h.cfg.Forwarders = &fwDupForwarders{}
	w := postForwarderAs(h, `{"type":"external","target":"out@elsewhere.com"}`, tenantU1)
	code, detail := errorDetail(t, w.Body.Bytes())
	if w.Code != http.StatusConflict || code != "already_exists" || detail == "" {
		t.Fatalf("status %d body %s, want 409 already_exists with a detail", w.Code, w.Body.String())
	}
}
