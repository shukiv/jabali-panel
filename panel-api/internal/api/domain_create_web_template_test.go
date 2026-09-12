package api

import (
	"context"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeWebTemplates is a minimal WebTemplateRepository for the createDomainOp
// gating tests — only FindByID is exercised.
type fakeWebTemplates struct {
	repository.WebTemplateRepository
	byID map[string]*models.WebTemplate
}

func (f fakeWebTemplates) FindByID(_ context.Context, id string) (*models.WebTemplate, error) {
	if t, ok := f.byID[id]; ok {
		return t, nil
	}
	return nil, repository.ErrNotFound
}

// GH #1624 / ADR-0169 Phase 3: an ADMIN may create a domain from a web (nginx)
// template — its directives are snapshot-copied onto the new domain's
// NginxCustomDirectives. ADMIN-ONLY (the admin denylist does not block
// proxy_pass, so a tenant-pickable template would be an SSRF vector), validated
// again at apply, and fail-closed on every misuse.
func TestCreateDomainOp_WebTemplate(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}
	const tmplID = "wtmpl-01"
	const directives = `add_header X-From-Template "1" always;`

	newH := func(withTemplates bool, tmpl *models.WebTemplate) *domainHandler {
		cfg := DomainHandlerConfig{Users: newAbUsers(owner), Domains: newDCDomains()}
		if withTemplates {
			m := map[string]*models.WebTemplate{}
			if tmpl != nil {
				m[tmpl.ID] = tmpl
			}
			cfg.WebTemplates = fakeWebTemplates{byID: m}
		}
		return &domainHandler{cfg: cfg}
	}
	goodTmpl := &models.WebTemplate{ID: tmplID, Name: "WordPress", NginxDirectives: directives}

	t.Run("admin: template directives snapshot-copied + web_template_id set", func(t *testing.T) {
		d, oerr := createDomainOp(context.Background(), newH(true, goodTmpl), createDomainInput{
			OwnerID:       owner.ID,
			Name:          "shop.example.com",
			ActorIsAdmin:  true,
			WebTemplateID: tmplID,
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v (%s)", oerr.Code, oerr.Detail)
		}
		if d.NginxCustomDirectives == nil || *d.NginxCustomDirectives != directives {
			t.Errorf("NginxCustomDirectives = %v, want %q", d.NginxCustomDirectives, directives)
		}
		if d.WebTemplateID == nil || *d.WebTemplateID != tmplID {
			t.Errorf("WebTemplateID = %v, want %q", d.WebTemplateID, tmplID)
		}
	})

	t.Run("non-admin: template rejected (web_template_admin_only)", func(t *testing.T) {
		_, oerr := createDomainOp(context.Background(), newH(true, goodTmpl), createDomainInput{
			OwnerID:       owner.ID,
			Name:          "b.example.com",
			ActorIsAdmin:  false,
			WebTemplateID: tmplID,
		})
		if oerr == nil || oerr.Code != "web_template_admin_only" {
			t.Fatalf("want web_template_admin_only, got %v", oerr)
		}
		if oerr.Status != http.StatusForbidden {
			t.Errorf("status = %d, want 403", oerr.Status)
		}
	})

	t.Run("unknown template id → 400 unknown_web_template", func(t *testing.T) {
		_, oerr := createDomainOp(context.Background(), newH(true, goodTmpl), createDomainInput{
			OwnerID:       owner.ID,
			Name:          "c.example.com",
			ActorIsAdmin:  true,
			WebTemplateID: "does-not-exist",
		})
		if oerr == nil || oerr.Code != "unknown_web_template" {
			t.Fatalf("want unknown_web_template, got %v", oerr)
		}
	})

	t.Run("repo unwired → 503 fail-closed", func(t *testing.T) {
		_, oerr := createDomainOp(context.Background(), newH(false, nil), createDomainInput{
			OwnerID:       owner.ID,
			Name:          "d.example.com",
			ActorIsAdmin:  true,
			WebTemplateID: tmplID,
		})
		if oerr == nil || oerr.Code != "web_templates_unavailable" {
			t.Fatalf("want web_templates_unavailable, got %v", oerr)
		}
		if oerr.Status != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", oerr.Status)
		}
	})

	t.Run("apply-time validation: a denylisted directive in a stored template → 400", func(t *testing.T) {
		// A template whose directives bypassed save validation (denylist tightened
		// after the template was saved, or a row inserted out-of-band). Apply-time
		// ValidateNginxDirectivesAdmin must STILL reject it — fail-closed, not dead
		// code.
		badTmpl := &models.WebTemplate{ID: "wtmpl-bad", Name: "Bad", NginxDirectives: "root /etc/jabali-panel/;"}
		_, oerr := createDomainOp(context.Background(), newH(true, badTmpl), createDomainInput{
			OwnerID:       owner.ID,
			Name:          "e.example.com",
			ActorIsAdmin:  true,
			WebTemplateID: "wtmpl-bad",
		})
		if oerr == nil || oerr.Code != "web_template_invalid" {
			t.Fatalf("want web_template_invalid, got %v", oerr)
		}
	})

	t.Run("no template → no directives, no template id (unchanged create)", func(t *testing.T) {
		d, oerr := createDomainOp(context.Background(), newH(true, goodTmpl), createDomainInput{
			OwnerID:      owner.ID,
			Name:         "f.example.com",
			ActorIsAdmin: true,
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v (%s)", oerr.Code, oerr.Detail)
		}
		if d.NginxCustomDirectives != nil {
			t.Errorf("NginxCustomDirectives = %v, want nil", d.NginxCustomDirectives)
		}
		if d.WebTemplateID != nil {
			t.Errorf("WebTemplateID = %v, want nil", d.WebTemplateID)
		}
	})
}
