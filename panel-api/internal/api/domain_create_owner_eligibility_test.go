package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestCreateDomainOp_OwnerEligibility characterizes the owner-eligibility gate
// the REST handler now runs through domainops (JAB-279). It pins the two
// caller-visible refusals — an admin owner and a suspended owner — to the exact
// status codes and error codes the handler returned before the leaf existed, and
// asserts no domain row is persisted on either. The suspended case is also the
// falsify vehicle for the CLI parity fix: before this slice the CLI skipped it.
func TestCreateDomainOp_OwnerEligibility(t *testing.T) {
	uname := "alice"

	run := func(owner *models.User) (*createDomainError, *dcDomains) {
		users := newAbUsers(owner)
		dom := newDCDomains()
		h := &domainHandler{cfg: DomainHandlerConfig{Users: users, Domains: dom}}
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:       owner.ID,
			Name:          "shop.example.com",
			MailProvider:  models.MailProviderNone,
			SSLMode:       models.SSLModeNone,
			SkipInlineSSL: true,
		})
		return oerr, dom
	}

	t.Run("admin owner cannot host", func(t *testing.T) {
		oerr, dom := run(&models.User{ID: "u-admin", Username: &uname, IsAdmin: true})
		if oerr == nil || oerr.Status != 400 || oerr.Code != "admin_cannot_host" {
			t.Fatalf("want 400 admin_cannot_host, got %+v", oerr)
		}
		if len(dom.created) != 0 {
			t.Fatalf("no domain must be persisted on refusal, got %d", len(dom.created))
		}
	})

	t.Run("suspended owner is refused", func(t *testing.T) {
		oerr, dom := run(&models.User{ID: "u-susp", Username: &uname, Suspended: true})
		if oerr == nil || oerr.Status != 409 || oerr.Code != "user_suspended" {
			t.Fatalf("want 409 user_suspended, got %+v", oerr)
		}
		if len(dom.created) != 0 {
			t.Fatalf("no domain must be persisted on refusal, got %d", len(dom.created))
		}
	})
}
