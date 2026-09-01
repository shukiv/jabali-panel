package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1413: the domain-create op must confine the document root by the
// CALLER's privilege — a tenant (non-admin actor) is held to the domain's
// own tree, an admin may use anywhere under the owner's home. This closes
// the create/edit asymmetry (create used to run only the loose validator,
// so a tenant API caller could point a vhost at ~/.ssh or another domain).
func TestCreateDomainOp_DocRootActorConfinement(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}

	run := func(actorAdmin bool, docRoot string) (*models.Domain, *createDomainError, *dcDomains) {
		users := newAbUsers(owner)
		dom := newDCDomains()
		h := &domainHandler{cfg: DomainHandlerConfig{Users: users, Domains: dom}}
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:       owner.ID,
			Name:          "shop.example.com",
			DocRoot:       docRoot,
			ActorIsAdmin:  actorAdmin,
			MailProvider:  models.MailProviderNone,
			SSLMode:       models.SSLModeNone,
			SkipInlineSSL: true,
		})
		return d, oerr, dom
	}

	t.Run("tenant out-of-tree docroot is rejected", func(t *testing.T) {
		d, oerr, dom := run(false, "/home/alice/.ssh")
		if oerr == nil || oerr.Code != "invalid_document_root" {
			t.Fatalf("want invalid_document_root, got d=%v oerr=%v", d, oerr)
		}
		if len(dom.created) != 0 {
			t.Fatalf("no domain must be created on rejection, got %d", len(dom.created))
		}
	})

	t.Run("tenant another-domain tree is rejected", func(t *testing.T) {
		_, oerr, _ := run(false, "/home/alice/domains/other.com/public_html")
		if oerr == nil || oerr.Code != "invalid_document_root" {
			t.Fatalf("want invalid_document_root for a cross-domain tree, got %v", oerr)
		}
	})

	t.Run("tenant in-tree docroot is accepted and persisted", func(t *testing.T) {
		want := "/home/alice/domains/shop.example.com/public"
		d, oerr, dom := run(false, "  "+want+"  ") // leading/trailing space is trimmed
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if d.DocRoot != want {
			t.Fatalf("DocRoot = %q, want %q", d.DocRoot, want)
		}
		if len(dom.created) != 1 || dom.created[0].DocRoot != want {
			t.Fatalf("persisted DocRoot mismatch: %+v", dom.created)
		}
	})

	t.Run("empty docroot derives the default", func(t *testing.T) {
		d, oerr, _ := run(false, "")
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if d.DocRoot != "/home/alice/domains/shop.example.com/public_html" {
			t.Fatalf("default DocRoot = %q", d.DocRoot)
		}
	})

	t.Run("admin may point anywhere under the owner's home", func(t *testing.T) {
		want := "/home/alice/othersite"
		d, oerr, _ := run(true, want)
		if oerr != nil {
			t.Fatalf("admin out-of-domain-tree should be allowed, got %v", oerr)
		}
		if d.DocRoot != want {
			t.Fatalf("DocRoot = %q, want %q", d.DocRoot, want)
		}
	})

	t.Run("admin still cannot escape the home with traversal", func(t *testing.T) {
		_, oerr, _ := run(true, "/home/alice/../bob/secret")
		if oerr == nil || oerr.Code != "invalid_document_root" {
			t.Fatalf("want invalid_document_root for traversal, got %v", oerr)
		}
	})
}
