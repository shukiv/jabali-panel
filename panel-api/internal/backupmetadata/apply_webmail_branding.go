package backupmetadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailbranding"
)

// webmailBrandingDir is where a restore stores the domains' pictures; tests
// swap it.
var webmailBrandingDir = webmailbranding.Dir

// restoreWebmailBranding stores the backup's webmail branding (GH #2502) of
// dm on dom, the account's domain that stands for it here. A domain that
// already has branding keeps it, unless OverwriteRows.
//
// The archive may be an uploaded file, so every field goes through the
// branding page's checks, and one that fails refuses the branding. Each
// picture is decoded and encoded again, and stored under its new hash; one
// the backup doesn't carry, or that fails, is left out and the server-wide
// one shows. Branding from an uploaded file is the owner's, never an
// administrator's: it shows only while the owner's package has webmail
// branding.
func restoreWebmailBranding(ctx context.Context, d Deps, r *ApplyResult, dm internalbackup.MetadataDomain, dom *models.Domain, now time.Time) {
	b := dm.WebmailBranding
	report := func(format string, args ...any) {
		r.Errors = append(r.Errors, fmt.Sprintf("domain %s (%s): webmail branding ", dm.ID, dm.Name)+fmt.Sprintf(format, args...))
	}
	if d.WebmailBranding == nil {
		report("not restored: the branding store is not wired")
		return
	}
	existing, err := d.WebmailBranding.Get(ctx, dom.ID)
	switch {
	case err == nil && !d.OverwriteRows:
		r.Skipped++
		return
	case err != nil && !errors.Is(err, repository.ErrNotFound):
		report("not restored: %v", err)
		return
	}
	var local models.DomainWebmailBranding
	if existing != nil {
		local = *existing
	}

	row := &models.DomainWebmailBranding{DomainID: dom.ID, SetByAdmin: b.SetByAdmin && !d.Untrusted, CreatedAt: now, UpdatedAt: now}
	if existing != nil {
		row.CreatedAt = existing.CreatedAt
	}
	var bad []string
	text := func(field, v string, max int) string {
		s, err := webmailbranding.Text(field, v, max)
		if err != nil {
			bad = append(bad, err.Error())
		}
		return s
	}
	link := func(field, v string) string {
		s, err := webmailbranding.Link(field, v)
		if err != nil {
			bad = append(bad, err.Error())
		}
		return s
	}
	row.AppName = text("app_name", b.AppName, webmailbranding.MaxAppName)
	row.CompanyName = text("company_name", b.CompanyName, webmailbranding.MaxCompanyName)
	row.WebsiteURL = link("website_url", b.WebsiteURL)
	row.PrivacyURL = link("privacy_url", b.PrivacyURL)
	row.ImprintURL = link("imprint_url", b.ImprintURL)
	if len(bad) > 0 {
		report("not restored: %s", strings.Join(bad, "; "))
		return
	}

	pics := []struct {
		asset   webmailbranding.Asset
		backup  string // the picture's hash in the backup, "" for none
		data    []byte
		here    string // this server's picture's hash, "" for none
		hash    *string
		encoded []byte
	}{
		{webmailbranding.LogoLight, b.LogoLight, b.LogoLightPNG, local.LogoLight, &row.LogoLight, nil},
		{webmailbranding.LogoDark, b.LogoDark, b.LogoDarkPNG, local.LogoDark, &row.LogoDark, nil},
		{webmailbranding.Favicon, b.Favicon, b.FaviconPNG, local.Favicon, &row.Favicon, nil},
	}
	for i := range pics {
		p := &pics[i]
		if p.backup == "" {
			continue
		}
		if len(p.data) == 0 {
			report("%s not restored: the backup doesn't carry it", p.asset)
			continue
		}
		out, hash, err := webmailbranding.EncodeStored(p.data, internalbackup.MaxWebmailBrandingPictureBytes)
		if err != nil {
			report("%s not restored: %v", p.asset, err)
			continue
		}
		p.encoded, *p.hash = out, hash
	}
	if row.Empty() {
		report("not restored: nothing of it is left")
		return
	}

	// The row goes in before the pictures: the reconciler deletes the
	// pictures of a domain without a row.
	if err := d.WebmailBranding.Put(ctx, row); err != nil {
		report("not restored: %v", err)
		return
	}
	r.WebmailBranding++
	wrote, failed := false, false
	for _, p := range pics {
		switch {
		case p.encoded != nil:
			if err := webmailbranding.Write(webmailBrandingDir, dom.ID, p.asset, p.encoded); err != nil {
				report("%s not restored: %v", p.asset, err)
				*p.hash, failed = "", true
				continue
			}
			wrote = true
		case p.here != "":
			// This server's picture, which the backup's branding has none of.
			if err := webmailbranding.Remove(webmailBrandingDir, dom.ID, p.asset); err != nil {
				report("%s: remove this server's: %v", p.asset, err)
			}
		}
	}
	if failed {
		// The row must not name a picture that isn't there.
		if err := d.WebmailBranding.Put(ctx, row); err != nil {
			report("names a picture that was not restored: %v", err)
		}
	}
	if wrote {
		if err := webmailbranding.OwnForPanel(webmailBrandingDir, dom.ID); err != nil {
			report("pictures not given to the panel's user: %v", err)
		}
	}
}
