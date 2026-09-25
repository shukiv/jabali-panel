package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domain_preview_slug_test.go — JAB-279. The preview-URL slug flattens dots to
// dashes, which is not injective, so the create and enable doors refuse a
// domain whose slug collides with another preview-enabled domain. The check
// must see every preview-enabled domain, not a capped page of all domains.

// previewDomains is a domain store whose generic List honours Limit (as the
// real repository does) and which records how the preview check reads it.
type previewDomains struct {
	*dcDomains
	rows          []models.Domain
	listCalls     int
	previewCalls  int
	previewReturn error
}

func (r *previewDomains) List(_ context.Context, opts repository.ListOptions) ([]models.Domain, int64, error) {
	r.listCalls++
	out := r.rows
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, int64(len(r.rows)), nil
}

func (r *previewDomains) ListPreviewEnabled(context.Context) ([]models.Domain, error) {
	r.previewCalls++
	if r.previewReturn != nil {
		return nil, r.previewReturn
	}
	var out []models.Domain
	for _, d := range r.rows {
		if d.TempURLEnabled {
			out = append(out, models.Domain{ID: d.ID, Name: d.Name, TempURLEnabled: true})
		}
	}
	return out, nil
}

// manyPreviewDomains returns n preview-enabled domains plus, last, one whose
// slug collides with "my.site.com" ("my-site-com").
func manyPreviewDomains(n int) []models.Domain {
	rows := make([]models.Domain, 0, n+1)
	for i := 0; i < n; i++ {
		rows = append(rows, models.Domain{ID: fmt.Sprintf("d%05d", i), Name: fmt.Sprintf("site%05d.example.com", i), TempURLEnabled: true})
	}
	return append(rows, models.Domain{ID: "d-last", Name: "my-site.com", TempURLEnabled: true})
}

func TestCreateDomainOp_PreviewSlugConflict(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}
	newH := func(rows []models.Domain) (*domainHandler, *previewDomains) {
		dom := &previewDomains{dcDomains: newDCDomains(), rows: rows}
		return &domainHandler{cfg: DomainHandlerConfig{Users: newAbUsers(owner), Domains: dom}}, dom
	}
	create := func(h *domainHandler) (*models.Domain, *createDomainError) {
		return createDomainOp(context.Background(), h, createDomainInput{
			OwnerID: owner.ID, Name: "my.site.com", TempURLEnabled: true, SkipInlineSSL: true,
		})
	}

	t.Run("a collision past the first 10000 domains is still refused", func(t *testing.T) {
		h, dom := newH(manyPreviewDomains(10000))
		_, oerr := create(h)
		if oerr == nil || oerr.Status != http.StatusConflict || oerr.Code != "temp_url_slug_conflict" ||
			oerr.Detail != "preview URL would collide with my-site.com" {
			t.Fatalf("want 409 temp_url_slug_conflict naming my-site.com, got %+v", oerr)
		}
		if len(dom.created) != 0 {
			t.Fatal("a refused create must persist nothing")
		}
	})

	t.Run("the check reads only preview-enabled domains, not every full row", func(t *testing.T) {
		h, dom := newH(manyPreviewDomains(3))
		_, _ = create(h)
		if dom.listCalls != 0 {
			t.Fatalf("the preview check must not page through every domain (List called %d times)", dom.listCalls)
		}
		if dom.previewCalls != 1 {
			t.Fatalf("the preview check must read the preview-enabled domains once, got %d", dom.previewCalls)
		}
	})

	t.Run("a domain whose preview is off does not collide", func(t *testing.T) {
		h, dom := newH([]models.Domain{{ID: "d1", Name: "my-site.com", TempURLEnabled: false}})
		if _, oerr := create(h); oerr != nil {
			t.Fatalf("want created, got %+v", oerr)
		}
		if len(dom.created) != 1 {
			t.Fatal("want one persisted row")
		}
	})

	t.Run("no preview requested skips the check", func(t *testing.T) {
		h, dom := newH(manyPreviewDomains(3))
		if _, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID: owner.ID, Name: "my.site.com", SkipInlineSSL: true,
		}); oerr != nil {
			t.Fatalf("want created, got %+v", oerr)
		}
		if dom.previewCalls+dom.listCalls != 0 {
			t.Fatal("a create without a preview URL must not run the slug check")
		}
	})

	t.Run("a store error does not block the create (nginx first-wins is the backstop)", func(t *testing.T) {
		h, dom := newH(manyPreviewDomains(3))
		dom.previewReturn = fmt.Errorf("domains: connection refused")
		if _, oerr := create(h); oerr != nil {
			t.Fatalf("want created (fail-open, as before), got %+v", oerr)
		}
	})
}

// TestPreviewSlugConflict_ExcludesSelf pins the enable (update) door's use:
// the domain being toggled never collides with itself.
func TestPreviewSlugConflict_ExcludesSelf(t *testing.T) {
	dom := &previewDomains{dcDomains: newDCDomains(), rows: []models.Domain{
		{ID: "self", Name: "My.Site.com.", TempURLEnabled: true},
	}}
	h := &domainHandler{cfg: DomainHandlerConfig{Domains: dom}}
	if got := h.previewSlugConflict(context.Background(), "my.site.com", "self"); got != "" {
		t.Fatalf("a domain must not collide with itself, got %q", got)
	}
	if got := h.previewSlugConflict(context.Background(), "my.site.com", "other"); got != "My.Site.com." {
		t.Fatalf("a case / trailing-dot variant must collide, got %q", got)
	}
}
