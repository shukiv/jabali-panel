package backupmetadata

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailbranding"
)

// GH #2502: a backup carries each domain's webmail branding, its pictures
// too, and a restore brings it back through the branding page's checks.

const wbDomainID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"

// wbRepo keeps the branding rows on this server. At each Put it records
// whether the domain's light logo was already on disk.
type wbRepo struct {
	repository.DomainWebmailBrandingRepository
	rows       map[string]models.DomainWebmailBranding
	batchCalls int
	puts       int
	picAtPut   []bool
	dir        string
}

func (r *wbRepo) Get(_ context.Context, id string) (*models.DomainWebmailBranding, error) {
	row, ok := r.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &row, nil
}

func (r *wbRepo) Put(_ context.Context, row *models.DomainWebmailBranding) error {
	r.puts++
	_, err := os.Stat(webmailbranding.Path(r.dir, row.DomainID, webmailbranding.LogoLight))
	r.picAtPut = append(r.picAtPut, err == nil)
	if r.rows == nil {
		r.rows = map[string]models.DomainWebmailBranding{}
	}
	r.rows[row.DomainID] = *row
	return nil
}

func (r *wbRepo) ListByDomainIDs(_ context.Context, ids []string) ([]models.DomainWebmailBranding, error) {
	r.batchCalls++
	var out []models.DomainWebmailBranding
	for _, id := range ids {
		if row, ok := r.rows[id]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func wbPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// wbDir points the restore's pictures at a temporary directory.
func wbDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "webmail-branding")
	orig := webmailBrandingDir
	t.Cleanup(func() { webmailBrandingDir = orig })
	webmailBrandingDir = dir
	return dir
}

// wbErrors are the report's lines about webmail branding.
func wbErrors(errs []string) []string {
	var out []string
	for _, e := range errs {
		if strings.Contains(e, "webmail branding") {
			out = append(out, e)
		}
	}
	return out
}

func TestBuild_CarriesWebmailBranding(t *testing.T) {
	repo := &wbRepo{rows: map[string]models.DomainWebmailBranding{
		"d1": {DomainID: "d1", AppName: "Shop Mail", WebsiteURL: "https://shop.org/", LogoLight: "aaaaaaaaaaaa", SetByAdmin: true},
		"dx": {DomainID: "dx", AppName: "Another account's"},
	}}
	doms := &fDomains{rows: []models.Domain{{ID: "d1", Name: "shop.org"}, {ID: "d2", Name: "bare.org"}}}
	m := Build(context.Background(), &models.User{ID: "u1"}, Deps{Domains: doms, WebmailBranding: repo})
	b := m.Domains[0].WebmailBranding
	if b == nil || b.AppName != "Shop Mail" || b.WebsiteURL != "https://shop.org/" ||
		b.LogoLight != "aaaaaaaaaaaa" || !b.SetByAdmin || b.LogoLightPNG != nil {
		t.Fatalf("shop.org branding %+v, want its row without picture bytes (the agent adds them)", b)
	}
	if m.Domains[1].WebmailBranding != nil {
		t.Fatalf("bare.org has no branding, got %+v", m.Domains[1].WebmailBranding)
	}
	if repo.batchCalls != 1 {
		t.Fatalf("branding reads %d, want one batch", repo.batchCalls)
	}
}

// wbMeta is a new account's backup of good.org, with branding b.
func wbMeta(b *internalbackup.MetadataWebmailBranding) *internalbackup.AccountMetadata {
	m := dcMeta()
	m.AppInstalls = nil
	m.Domains = m.Domains[:1]
	m.Domains[0].ID, m.Domains[0].Mailboxes = wbDomainID, nil
	m.Domains[0].WebmailBranding = b
	return m
}

func wbApply(m *internalbackup.AccountMetadata, repo *wbRepo, untrusted bool) ApplyResult {
	_, _, _, d := dcDeps()
	d.CheckDomain = func(context.Context, *models.Domain, string) ([]string, error) { return nil, nil }
	d.Untrusted = untrusted
	if repo != nil {
		d.WebmailBranding = repo
	}
	return Apply(context.Background(), m, d)
}

func TestApply_RestoresWebmailBranding(t *testing.T) {
	dir := wbDir(t)
	logo := wbPNG(t, 64, 32)
	want, hash, err := webmailbranding.EncodeStored(logo, internalbackup.MaxWebmailBrandingPictureBytes)
	if err != nil {
		t.Fatal(err)
	}
	repo := &wbRepo{dir: dir}
	r := wbApply(wbMeta(&internalbackup.MetadataWebmailBranding{
		AppName: "  Good Mail ", CompanyName: "Good Ltd", ImprintURL: "https://good.org/imprint",
		LogoLight: "000000000000", LogoLightPNG: logo,
		Favicon:    "111111111111", // the backup doesn't carry it
		SetByAdmin: true,
	}), repo, false)

	row, ok := repo.rows[wbDomainID]
	if !ok || r.WebmailBranding != 1 {
		t.Fatalf("no branding restored (count %d), errors %v", r.WebmailBranding, r.Errors)
	}
	if row.AppName != "Good Mail" || row.CompanyName != "Good Ltd" || row.ImprintURL != "https://good.org/imprint" || !row.SetByAdmin {
		t.Fatalf("row %+v", row)
	}
	if row.LogoLight != hash || row.Favicon != "" {
		t.Fatalf("logo %q favicon %q; want the logo under its new hash %q and no favicon", row.LogoLight, row.Favicon, hash)
	}
	if got, err := os.ReadFile(webmailbranding.Path(dir, wbDomainID, webmailbranding.LogoLight)); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("logo on disk: %d bytes, %v", len(got), err)
	}
	if len(repo.picAtPut) != 1 || repo.picAtPut[0] {
		t.Fatalf("pictures on disk at each row write: %v; the row goes in first", repo.picAtPut)
	}
	got := wbErrors(r.Errors)
	if len(got) != 1 || got[0] != "domain "+wbDomainID+" (good.org): webmail branding favicon not restored: the backup doesn't carry it" {
		t.Fatalf("report %v", got)
	}
}

// Branding from an uploaded file is the owner's: whoever made the file chose
// set_by_admin, which would show it whatever the owner's package allows.
func TestApply_UploadedWebmailBrandingIsTheOwners(t *testing.T) {
	wbDir(t)
	repo := &wbRepo{}
	wbApply(wbMeta(&internalbackup.MetadataWebmailBranding{AppName: "Good Mail", SetByAdmin: true}), repo, true)
	if row, ok := repo.rows[wbDomainID]; !ok || row.SetByAdmin {
		t.Fatalf("row %+v (stored %v), want the owner's", row, ok)
	}
}

// A field the branding page would refuse refuses the branding; a picture
// that isn't one is left out.
func TestApply_WebmailBrandingChecks(t *testing.T) {
	dir := wbDir(t)
	repo := &wbRepo{dir: dir}
	r := wbApply(wbMeta(&internalbackup.MetadataWebmailBranding{
		AppName: "Good Mail", WebsiteURL: "javascript:alert(1)",
	}), repo, true)
	if repo.puts != 0 || !hasError(r.Errors, "webmail branding not restored: website_url: must be an http:// or https:// address") {
		t.Fatalf("puts %d errors %v", repo.puts, r.Errors)
	}

	r = wbApply(wbMeta(&internalbackup.MetadataWebmailBranding{
		AppName: "Good Mail\nAPP_NAME=x",
	}), repo, true)
	if repo.puts != 0 || !hasError(r.Errors, "webmail branding not restored: app_name: contains control characters") {
		t.Fatalf("puts %d errors %v", repo.puts, r.Errors)
	}

	r = wbApply(wbMeta(&internalbackup.MetadataWebmailBranding{
		AppName: "Good Mail", LogoDark: "222222222222", LogoDarkPNG: []byte("<svg onload=alert(1)/>"),
	}), repo, true)
	row := repo.rows[wbDomainID]
	if row.AppName != "Good Mail" || row.LogoDark != "" || !hasError(r.Errors, "webmail branding logo-dark not restored: webmailbranding: not a PNG or JPEG picture") {
		t.Fatalf("row %+v errors %v", row, r.Errors)
	}
	if _, err := os.Stat(filepath.Join(dir, wbDomainID)); !os.IsNotExist(err) {
		t.Fatalf("a picture directory was made for no picture: %v", err)
	}

	// Nothing is left once the picture is out: no row.
	repo = &wbRepo{dir: dir}
	r = wbApply(wbMeta(&internalbackup.MetadataWebmailBranding{Favicon: "333333333333"}), repo, true)
	if repo.puts != 0 || !hasError(r.Errors, "webmail branding not restored: nothing of it is left") {
		t.Fatalf("puts %d errors %v", repo.puts, r.Errors)
	}
}

func TestApply_WebmailBrandingNeedsItsStore(t *testing.T) {
	wbDir(t)
	r := wbApply(wbMeta(&internalbackup.MetadataWebmailBranding{AppName: "Good Mail"}), nil, false)
	if !hasError(r.Errors, "domain "+wbDomainID+" (good.org): webmail branding not restored: the branding store is not wired") {
		t.Fatalf("errors %v", r.Errors)
	}
}

// The account's existing domain keeps its own branding, unless the restore
// overwrites existing items: then it takes the backup's, and a picture of its
// own that the backup's branding has none of goes.
func TestApply_WebmailBrandingOfAnExistingDomain(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		dir := wbDir(t)
		if err := webmailbranding.Write(dir, wbDomainID, webmailbranding.Favicon, []byte("own")); err != nil {
			t.Fatal(err)
		}
		own := models.DomainWebmailBranding{DomainID: wbDomainID, AppName: "Own Mail", Favicon: "444444444444", CreatedAt: time.Unix(1, 0)}
		repo := &wbRepo{dir: dir, rows: map[string]models.DomainWebmailBranding{wbDomainID: own}}
		f := odSetup(wbDomainID, overwrite, "")
		f.deps.WebmailBranding = repo
		m := odMeta(wbDomainID)
		m.Domains[0].WebmailBranding = &internalbackup.MetadataWebmailBranding{AppName: "Backup Mail", LogoLight: "555555555555", LogoLightPNG: wbPNG(t, 8, 8)}
		r := Apply(context.Background(), m, f.deps)

		row := repo.rows[wbDomainID]
		_, favErr := os.Stat(webmailbranding.Path(dir, wbDomainID, webmailbranding.Favicon))
		if !overwrite {
			if row != own || repo.puts != 0 || favErr != nil || r.WebmailBranding != 0 {
				t.Fatalf("kept: row %+v puts %d favicon %v", row, repo.puts, favErr)
			}
			continue
		}
		if row.AppName != "Backup Mail" || row.Favicon != "" || row.LogoLight == "" || !row.CreatedAt.Equal(own.CreatedAt) || !os.IsNotExist(favErr) {
			t.Fatalf("overwritten: row %+v favicon %v report %v", row, favErr, wbErrors(r.Errors))
		}
		if _, err := os.Stat(webmailbranding.Path(dir, wbDomainID, webmailbranding.LogoLight)); err != nil {
			t.Fatalf("logo not written: %v", err)
		}
	}
}
