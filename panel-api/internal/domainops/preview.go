package domainops

import (
	"context"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Preview-URL slug collision (JAB-279). A domain's preview host is
// <slug>.preview.<base>, where the slug flattens dots to dashes. The flattening
// is not injective ("my-site.com" and "my.site.com" both give "my-site-com"),
// so the create door and the preview-enable door refuse the second domain of a
// colliding pair — nginx would otherwise serve whichever vhost it loaded first.
//
// The check reads only the preview-enabled domains, and all of them: it used to
// page through every full domain row with a 10000-row cap, which both loaded
// every heavy column and missed a collision past the cap.

// PreviewDomainLister lists the preview-enabled domains. repository's
// DomainRepository satisfies it.
type PreviewDomainLister interface {
	ListPreviewEnabled(ctx context.Context) ([]models.Domain, error)
}

// PreviewSlugConflict returns the name of another preview-enabled domain whose
// preview slug equals name's, or "" when there is none. selfID is the domain
// being enabled ("" at create) and never collides with itself. A store error
// is returned as is; the doors treat it as no conflict (nginx first-wins is the
// backstop), so a transient read failure never blocks a create or an enable.
func PreviewSlugConflict(ctx context.Context, domains PreviewDomainLister, name, selfID string) (string, error) {
	rows, err := domains.ListPreviewEnabled(ctx)
	if err != nil {
		return "", err
	}
	slug := models.PreviewSlug(name)
	for i := range rows {
		d := &rows[i]
		if d.ID == selfID || !d.TempURLEnabled {
			continue
		}
		if models.PreviewSlug(d.Name) == slug {
			return d.Name, nil
		}
	}
	return "", nil
}
