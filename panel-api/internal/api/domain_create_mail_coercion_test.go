package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestCreateDomainOp_MailModuleCoercion pins that createDomainOp (the REST/GUI/
// automation entrypoint) routes the resolved mail provider through the shared
// domainops.MailProviderForServer leaf (JAB-279 / GH #1409): a Jabali provider is
// coerced to none — email disabled — when the server mail module is off, and kept
// when it is on. This is the api-side proof that REST still routes through the
// leaf after the coercion moved out of this package; the CLI runs the identical
// leaf. Falsify: pass a literal true at the call site → the module-off case
// reddens (mail stays jabali, email enabled).
func TestCreateDomainOp_MailModuleCoercion(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}

	run := func(mailModuleEnabled bool) *models.Domain {
		dom := newDCDomains()
		h := &domainHandler{cfg: DomainHandlerConfig{
			Users:          newAbUsers(owner),
			Domains:        dom,
			ServerSettings: &fakeStatusSettingsRepo{s: &models.ServerSettings{MailEnabled: mailModuleEnabled}},
		}}
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:       owner.ID,
			Name:          "shop.example.com",
			MailProvider:  models.MailProviderJabali,
			SSLMode:       models.SSLModeLE,
			SkipInlineSSL: true,
		})
		if oerr != nil {
			t.Fatalf("unexpected error (mailModuleEnabled=%v): %v", mailModuleEnabled, oerr)
		}
		return d
	}

	t.Run("module off coerces jabali to none, email disabled", func(t *testing.T) {
		d := run(false)
		if d.MailProvider != models.MailProviderNone {
			t.Errorf("mail-less server must coerce jabali to none, got %q", d.MailProvider)
		}
		if d.EmailEnabled {
			t.Error("a coerced domain must not have email enabled")
		}
	})

	t.Run("module on keeps jabali, email enabled", func(t *testing.T) {
		d := run(true)
		if d.MailProvider != models.MailProviderJabali {
			t.Errorf("mail-enabled server must keep jabali, got %q", d.MailProvider)
		}
		if !d.EmailEnabled {
			t.Error("an uncoerced jabali domain must have email enabled")
		}
	})
}
