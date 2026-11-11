package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type sendOnlyFlipRepo struct {
	addrMailboxRepo
	setCalls []bool
}

func (r *sendOnlyFlipRepo) SetSendOnly(_ context.Context, _ string, sendOnly bool) error {
	r.setCalls = append(r.setCalls, sendOnly)
	return nil
}

func sendOnlyRouter(repo *sendOnlyFlipRepo, dom *models.Domain) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	RegisterMailboxRoutes(r.Group(""), MailboxHandlerConfig{
		Mailboxes: repo,
		Domains:   &saDomainRepo{byID: map[string]*models.Domain{dom.ID: dom}},
		Addresses: &recAddressReleaser{},
	})
	return r
}

func sendOnlyDomain(emailEnabled bool, provider string) *models.Domain {
	return &models.Domain{ID: "dom1", Name: "x.test", UserID: "u1", EmailEnabled: emailEnabled, MailProvider: provider,
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}}
}

func sendOnlyRequest(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Turning a send-only account into one that receives would make this server
// hold mail for a domain whose mail is hosted elsewhere.
func TestMailboxUpdate_SendOnlyStaysOnWhereMailIsHostedElsewhere(t *testing.T) {
	for name, dom := range map[string]*models.Domain{
		"email off":        sendOnlyDomain(false, models.MailProviderNone),
		"outside provider": sendOnlyDomain(false, models.MailProviderGoogle),
	} {
		t.Run(name, func(t *testing.T) {
			mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "smtp", EmailCached: "smtp@x.test", SendOnly: true}
			repo := &sendOnlyFlipRepo{addrMailboxRepo: addrMailboxRepo{saMailboxRepo: saMailboxRepo{byID: map[string]*models.Mailbox{"mb1": mb}}}}
			w := sendOnlyRequest(t, sendOnlyRouter(repo, dom), http.MethodPatch, "/mailboxes/mb1", `{"send_only":false}`)
			if w.Code != http.StatusConflict {
				t.Fatalf("status %d, want 409: %s", w.Code, w.Body.String())
			}
			var body map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if body["error"] != "mail_hosted_elsewhere" {
				t.Errorf("error = %v, want mail_hosted_elsewhere", body["error"])
			}
			if len(repo.setCalls) != 0 {
				t.Fatalf("send_only was written: %v", repo.setCalls)
			}
		})
	}
}

// The guard is only on that one change. A domain whose mail is hosted here
// can still make a send-only account receive, and a mailbox left from before
// mail moved away can still be saved with its send_only value unchanged, or be
// made send-only.
func TestMailboxUpdate_SendOnlyChangesTheGuardLeavesAlone(t *testing.T) {
	cases := []struct {
		name     string
		dom      *models.Domain
		sendOnly bool
		body     string
		want     []bool
	}{
		{"hosted here, send-only off", sendOnlyDomain(true, models.MailProviderJabali), true, `{"send_only":false}`, []bool{false}},
		{"elsewhere, receiving stays receiving", sendOnlyDomain(false, models.MailProviderNone), false, `{"send_only":false}`, []bool{false}},
		{"elsewhere, receiving made send-only", sendOnlyDomain(false, models.MailProviderNone), false, `{"send_only":true}`, []bool{true}},
		{"elsewhere, send-only stays send-only", sendOnlyDomain(false, models.MailProviderNone), true, `{"send_only":true}`, []bool{true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "smtp", EmailCached: "smtp@x.test", SendOnly: tc.sendOnly}
			repo := &sendOnlyFlipRepo{addrMailboxRepo: addrMailboxRepo{saMailboxRepo: saMailboxRepo{byID: map[string]*models.Mailbox{"mb1": mb}}}}
			w := sendOnlyRequest(t, sendOnlyRouter(repo, tc.dom), http.MethodPatch, "/mailboxes/mb1", tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if len(repo.setCalls) != len(tc.want) || repo.setCalls[0] != tc.want[0] {
				t.Fatalf("SetSendOnly calls %v, want %v", repo.setCalls, tc.want)
			}
		})
	}
}

// POST creates a send-only account on a domain whose email is off, and still
// refuses one that receives.
func TestMailboxCreate_SendOnlyWhereEmailIsOff(t *testing.T) {
	dom := sendOnlyDomain(false, models.MailProviderNone)

	repo := &sendOnlyFlipRepo{}
	w := sendOnlyRequest(t, sendOnlyRouter(repo, dom), http.MethodPost, "/domains/dom1/mailboxes", `{"local_part":"smtp","send_only":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("send-only: status %d, want 201: %s", w.Code, w.Body.String())
	}
	if !repo.created {
		t.Fatal("no row written")
	}

	repo = &sendOnlyFlipRepo{}
	w = sendOnlyRequest(t, sendOnlyRouter(repo, dom), http.MethodPost, "/domains/dom1/mailboxes", `{"local_part":"info"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("receiving: status %d, want 409: %s", w.Code, w.Body.String())
	}
	if repo.created {
		t.Fatal("a receiving mailbox was written")
	}
}
