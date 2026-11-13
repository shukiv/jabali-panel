package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailbranding"
)

const wbDomainID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"

type wbDomains struct {
	repository.DomainRepository
	dom *models.Domain
}

func (f *wbDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if f.dom == nil || id != f.dom.ID {
		return nil, repository.ErrNotFound
	}
	return f.dom, nil
}

type wbUsers struct {
	repository.UserRepository
	u *models.User
}

func (f *wbUsers) FindByID(context.Context, string) (*models.User, error) { return f.u, nil }

type wbPackages struct {
	repository.PackageRepository
	pkg *models.HostingPackage
}

func (f *wbPackages) FindByID(context.Context, string) (*models.HostingPackage, error) {
	if f.pkg == nil {
		return nil, repository.ErrNotFound
	}
	return f.pkg, nil
}

type wbBranding struct {
	rows map[string]models.DomainWebmailBranding
}

func (f *wbBranding) Get(_ context.Context, id string) (*models.DomainWebmailBranding, error) {
	row, ok := f.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &row, nil
}

func (f *wbBranding) Put(_ context.Context, row *models.DomainWebmailBranding) error {
	f.rows[row.DomainID] = *row
	return nil
}

func (f *wbBranding) Delete(_ context.Context, id string) error {
	delete(f.rows, id)
	return nil
}

func (f *wbBranding) ListAll(context.Context) ([]repository.WebmailBrandingRow, error) {
	return nil, nil
}

func (f *wbBranding) ListByDomainIDs(context.Context, []string) ([]models.DomainWebmailBranding, error) {
	return nil, nil
}

type wbFixture struct {
	router   *gin.Engine
	branding *wbBranding
	dir      string
}

// wbSetup serves the routes to claims, for a domain of user "owner" whose
// package has webmail branding when allows; noPackage leaves them none.
func wbSetup(t *testing.T, claims *auth.AccessClaims, allows, noPackage bool) *wbFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	pkgID := "pkg"
	user := &models.User{ID: "owner", PackageID: &pkgID}
	if noPackage {
		user.PackageID = nil
	}
	f := &wbFixture{branding: &wbBranding{rows: map[string]models.DomainWebmailBranding{}}, dir: t.TempDir()}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, claims)
		c.Next()
	})
	RegisterDomainWebmailBrandingRoutes(r.Group("/api/v1"), DomainWebmailBrandingHandlerConfig{
		Domains:  &wbDomains{dom: &models.Domain{ID: wbDomainID, UserID: "owner", Name: "customer-a.com"}},
		Users:    &wbUsers{u: user},
		Packages: &wbPackages{pkg: &models.HostingPackage{ID: pkgID, WebmailBrandingEnabled: allows}},
		Branding: f.branding,
		Dir:      f.dir,
	})
	f.router = r
	return f
}

var (
	wbOwner = &auth.AccessClaims{UserID: "owner"}
	wbAdmin = &auth.AccessClaims{UserID: "adm", IsAdmin: true}
)

func (f *wbFixture) do(t *testing.T, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/v1/domains/"+wbDomainID+"/webmail-branding"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func (f *wbFixture) upload(t *testing.T, asset string, data []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "logo.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/domains/"+wbDomainID+"/webmail-branding/"+asset, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func wbPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	img.Set(1, 1, color.RGBA{B: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var wbFields = map[string]string{"app_name": "Customer A Mail", "company_name": "Customer A Ltd", "website_url": "https://customer-a.com"}

// An owner whose package doesn't have webmail branding sees the branding but
// can't change it; with no package at all neither (GH #282).
func TestWebmailBranding_OwnerNeedsThePackageSwitch(t *testing.T) {
	for name, noPackage := range map[string]bool{"switch off": false, "no package": true} {
		t.Run(name, func(t *testing.T) {
			f := wbSetup(t, wbOwner, false, noPackage)
			w, body := f.do(t, http.MethodGet, "", nil)
			if w.Code != http.StatusOK || body["can_edit"] != false || body["host"] != "mail.customer-a.com" {
				t.Fatalf("GET = %d %v", w.Code, body)
			}
			for _, req := range []struct{ method, path string }{{http.MethodPut, ""}, {http.MethodDelete, ""}, {http.MethodDelete, "/logo-light"}} {
				w, body := f.do(t, req.method, req.path, wbFields)
				if w.Code != http.StatusForbidden || body["error"] != "webmail_branding_not_in_package" {
					t.Fatalf("%s %q = %d %v, want 403 webmail_branding_not_in_package", req.method, req.path, w.Code, body)
				}
			}
			if w, _ := f.upload(t, "logo-light", wbPNG(t)); w.Code != http.StatusForbidden {
				t.Fatalf("upload = %d, want 403", w.Code)
			}
			if len(f.branding.rows) != 0 {
				t.Fatal("a refused change was stored")
			}
		})
	}
}

// The owner's own branding applies while the package has the switch.
func TestWebmailBranding_OwnerSavesTheirs(t *testing.T) {
	f := wbSetup(t, wbOwner, true, false)
	w, body := f.do(t, http.MethodPut, "", wbFields)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %v", w.Code, body)
	}
	if body["applies"] != true || body["set_by_admin"] != false || body["app_name"] != "Customer A Mail" {
		t.Fatalf("PUT body = %v", body)
	}
	if row := f.branding.rows[wbDomainID]; row.SetByAdmin || row.CompanyName != "Customer A Ltd" || row.CreatedAt.IsZero() {
		t.Fatalf("stored = %+v", row)
	}
}

// An administrator brands any domain, whatever the owner's package allows,
// and that branding applies.
func TestWebmailBranding_AdminBrandsAnyDomain(t *testing.T) {
	f := wbSetup(t, wbAdmin, false, true)
	w, body := f.do(t, http.MethodPut, "", wbFields)
	if w.Code != http.StatusOK || body["applies"] != true || body["set_by_admin"] != true || body["can_edit"] != true {
		t.Fatalf("PUT = %d %v", w.Code, body)
	}
	if !f.branding.rows[wbDomainID].SetByAdmin {
		t.Fatal("an administrator's branding was not stored as theirs")
	}
}

func TestWebmailBranding_OtherUsersAreRefused(t *testing.T) {
	f := wbSetup(t, &auth.AccessClaims{UserID: "someone-else"}, true, false)
	if w, _ := f.do(t, http.MethodGet, "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("GET = %d, want 403", w.Code)
	}
	if w, _ := f.do(t, http.MethodPut, "", wbFields); w.Code != http.StatusForbidden {
		t.Fatalf("PUT = %d, want 403", w.Code)
	}
}

func TestWebmailBranding_RefusesBadFields(t *testing.T) {
	f := wbSetup(t, wbOwner, true, false)
	for field, value := range map[string]string{
		"website_url":  "javascript:alert(1)",
		"privacy_url":  "data:text/html,x",
		"imprint_url":  "https://user:pw@example.com",
		"app_name":     "Mail\nDOMAIN_BRANDING=[]",
		"company_name": "a\u0007b",
	} {
		w, body := f.do(t, http.MethodPut, "", map[string]string{field: value})
		if w.Code != http.StatusUnprocessableEntity || body["field"] != field {
			t.Errorf("%s=%q: %d %v, want 422 naming the field", field, value, w.Code, body)
		}
	}
	if len(f.branding.rows) != 0 {
		t.Fatal("a refused value was stored")
	}
}

// A picture is stored as the PNG panel-api encoded, readable by nginx, and
// its hash is what webmail's URL carries.
func TestWebmailBranding_UploadStoresAnEncodedPNG(t *testing.T) {
	f := wbSetup(t, wbOwner, true, false)
	upload := append(wbPNG(t), []byte("<script>alert(1)</script>")...)
	w, body := f.upload(t, "logo-light", upload)
	if w.Code != http.StatusOK {
		t.Fatalf("upload = %d %v", w.Code, body)
	}
	hash, _ := body["logo_light"].(string)
	if len(hash) != 12 || f.branding.rows[wbDomainID].LogoLight != hash {
		t.Fatalf("logo_light = %q, stored %q", hash, f.branding.rows[wbDomainID].LogoLight)
	}
	stored, err := os.ReadFile(webmailbranding.Path(f.dir, wbDomainID, webmailbranding.LogoLight))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("<script>")) {
		t.Fatal("the stored picture still carries what followed the PNG")
	}
	if _, err := png.Decode(bytes.NewReader(stored)); err != nil {
		t.Fatalf("stored picture is not a PNG: %v", err)
	}
	// The panel's preview serves it as a PNG, never sniffed.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/"+wbDomainID+"/webmail-branding/logo-light", nil)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("preview = %d %v", rec.Code, rec.Header())
	}
}

func TestWebmailBranding_UploadRefusals(t *testing.T) {
	f := wbSetup(t, wbOwner, true, false)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`)
	if w, body := f.upload(t, "favicon", svg); w.Code != http.StatusUnprocessableEntity || body["error"] != "invalid_picture" {
		t.Fatalf("svg = %d %v, want 422 invalid_picture", w.Code, body)
	}
	if w, _ := f.upload(t, "favicon", bytes.Repeat([]byte{1}, webmailbranding.MaxUploadBytes+1)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d, want 413", w.Code)
	}
	if w, _ := f.upload(t, "background", wbPNG(t)); w.Code != http.StatusNotFound {
		t.Fatalf("unknown picture = %d, want 404", w.Code)
	}
	if _, err := os.Stat(filepath.Join(f.dir, wbDomainID)); !os.IsNotExist(err) {
		t.Fatal("a refused upload left a file")
	}
}

// Clearing the last field or picture removes the branding: the domain is
// back on the server-wide branding, with no row and no files left.
func TestWebmailBranding_EmptyBrandingIsRemoved(t *testing.T) {
	f := wbSetup(t, wbOwner, true, false)
	if w, _ := f.upload(t, "logo-dark", wbPNG(t)); w.Code != http.StatusOK {
		t.Fatalf("upload = %d", w.Code)
	}
	if w, body := f.do(t, http.MethodDelete, "/logo-dark", nil); w.Code != http.StatusOK || body["applies"] != false {
		t.Fatalf("remove picture = %d %v", w.Code, body)
	}
	if _, ok := f.branding.rows[wbDomainID]; ok {
		t.Fatal("an empty branding row stayed")
	}
	if _, err := os.Stat(filepath.Join(f.dir, wbDomainID)); !os.IsNotExist(err) {
		t.Fatal("the domain's picture dir stayed")
	}

	if w, _ := f.do(t, http.MethodPut, "", wbFields); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d", w.Code)
	}
	if w, _ := f.upload(t, "favicon", wbPNG(t)); w.Code != http.StatusOK {
		t.Fatalf("upload = %d", w.Code)
	}
	if w, body := f.do(t, http.MethodDelete, "", nil); w.Code != http.StatusOK || body["app_name"] != "" {
		t.Fatalf("reset = %d %v", w.Code, body)
	}
	if _, ok := f.branding.rows[wbDomainID]; ok {
		t.Fatal("reset left the row")
	}
	if _, err := os.Stat(filepath.Join(f.dir, wbDomainID)); !os.IsNotExist(err) {
		t.Fatal("reset left the pictures")
	}
}
