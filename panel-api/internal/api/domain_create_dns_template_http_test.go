package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"github.com/gin-gonic/gin"
)

// GH #1627: a tenant selects a custom DNS template at domain create by sending
// dns_template_id on POST /domains. #1635 shipped the createDomainOp gating and
// the reconciler seed but never bound the field on createDomainRequest, so the
// whole tenant-select path was dead code: the request value was silently
// dropped and every create stayed on the default Jabali posture.
//
// This exercises the HTTP boundary end to end. Without the request binding it
// reds (the stored row is jabali / MailTemplateID nil); it is the load-bearing
// guard that dns_template_id reaches createDomainInput and flips the persisted
// row to the external 'custom' posture with mail_template_id recorded for the
// reconciler to seed.
func TestDomainCreate_HTTP_BindsDNSTemplateID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}
	const tmplID = "tmpl-01"

	dom := newDCDomains()
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: owner.ID})
		c.Next()
	})
	RegisterDomainRoutes(v1, DomainHandlerConfig{
		Users:   newAbUsers(owner),
		Domains: dom,
		DNSTemplates: fakeDNSTemplates{byID: map[string]*models.DNSTemplate{
			tmplID: {ID: tmplID, Name: "Acme SaaS"},
		}},
	})

	body := bytes.NewBufferString(`{"name":"shop.example.com","dns_template_id":"` + tmplID + `"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201: %s", w.Code, w.Body.String())
	}
	if len(dom.created) != 1 {
		t.Fatalf("created %d domains, want 1", len(dom.created))
	}
	got := dom.created[0]
	if got.MailProvider != models.MailProviderCustom {
		t.Errorf("MailProvider = %q, want custom — dns_template_id was dropped at the HTTP boundary", got.MailProvider)
	}
	if got.MailTemplateID == nil || *got.MailTemplateID != tmplID {
		t.Errorf("MailTemplateID = %v, want %q", got.MailTemplateID, tmplID)
	}

	// The 201 body must also carry the external posture so the SPA reflects it
	// without a re-fetch.
	var resp models.Domain
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.MailProvider != models.MailProviderCustom {
		t.Errorf("response MailProvider = %q, want custom", resp.MailProvider)
	}
}
