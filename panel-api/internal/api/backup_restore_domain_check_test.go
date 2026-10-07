package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1898: a domain rebuilt from a backup archive passes the checks its
// create and update doors run, because the archive may be untrusted.

type rdcDomains map[string]*models.Domain

func (f rdcDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if d, ok := f[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}
func (f rdcDomains) FindStrictSubdomains(_ context.Context, name string) ([]models.Domain, error) {
	var out []models.Domain
	for n, d := range f {
		if strings.HasSuffix(n, "."+name) {
			out = append(out, *d)
		}
	}
	return out, nil
}

type rdcAliases map[string]*models.WebDomainAlias

func (f rdcAliases) FindByHostname(_ context.Context, host string) (*models.WebDomainAlias, error) {
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, repository.ErrNotFound
}

type rdcSettings struct{}

func (rdcSettings) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{Hostname: "panel.example.net"}, nil
}

func rdcCheck() func(context.Context, *models.Domain, string) ([]string, error) {
	return RestoreDomainCheck(
		rdcDomains{"other.com": {ID: "d-o", Name: "other.com", UserID: "u-other"}},
		rdcAliases{"aliased.com": {ID: "a1", DomainID: "d-x", Hostname: "aliased.com"}},
		rdcSettings{},
	)
}

func rdcRow(name string) *models.Domain {
	return &models.Domain{ID: "d1", UserID: "u1", Name: name, DocRoot: "/home/alice/domains/" + name + "/public_html"}
}

func strp(s string) *string { return &s }

func TestRestoreDomainCheck_RefusesWhatCreateRefuses(t *testing.T) {
	check := rdcCheck()
	for _, tc := range []struct {
		name    string
		row     *models.Domain
		user    string
		wantErr error
	}{
		{"invalid name", rdcRow("not a domain"), "alice", nil},
		{"nested under another owner", rdcRow("shop.other.com"), "alice", domainops.ErrDomainConflictsTenant},
		{"held by another domain's alias", rdcRow("aliased.com"), "alice", domainops.ErrDomainConflictsAlias},
		{"the panel's own hostname", rdcRow("panel.example.net"), "alice", domainops.ErrDomainConflictsMailHostname},
		{"document root outside the owner's home", func() *models.Domain {
			r := rdcRow("site.org")
			r.DocRoot = "/etc/nginx"
			return r
		}(), "alice", domainops.ErrDocRootOutsideHome},
		// With no username the home prefix degenerates to "/home//", which
		// "/home//bob/..." would match — another tenant's home.
		{"no owner username", func() *models.Domain {
			r := rdcRow("site.org")
			r.DocRoot = "/home//bob/public_html"
			return r
		}(), "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := check(context.Background(), tc.row, tc.user)
			if err == nil {
				t.Fatal("restored domain was not refused")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// Missing stores refuse rather than silently skip a guard.
func TestRestoreDomainCheck_UnwiredRefuses(t *testing.T) {
	check := RestoreDomainCheck(rdcDomains{}, nil, rdcSettings{})
	if _, err := check(context.Background(), rdcRow("site.org"), "alice"); !errors.Is(err, errRestoreChecksUnwired) {
		t.Fatalf("want errRestoreChecksUnwired, got %v", err)
	}
}

func TestRestoreDomainCheck_CleanRowPassesUnchanged(t *testing.T) {
	row := rdcRow("site.org")
	row.NginxCustomDirectives = strp("client_max_body_size 64m;")
	row.RedirectAllTo = strp("https://new.example.org")
	row.RedirectAllType = strp("301")
	w, err := rdcCheck()(context.Background(), row, "alice")
	if err != nil || len(w) != 0 {
		t.Fatalf("clean row: warnings %v err %v", w, err)
	}
	if row.NginxCustomDirectives == nil || row.RedirectAllTo == nil || row.RedirectAllType == nil {
		t.Fatalf("clean fields were cleared: %+v", row)
	}
}

func intp(i int) *int { return &i }

func TestRestoreDomainCheck_KeepsValidPHPLimits(t *testing.T) {
	row := rdcRow("site.org")
	row.PHPMemoryLimit, row.PHPUploadMaxFilesize, row.PHPPostMaxSize = strp("512M"), strp("64M"), strp("1G")
	row.PHPMaxInputVars, row.PHPMaxExecutionTime, row.PHPMaxInputTime = intp(3000), intp(300), intp(60)
	w, err := rdcCheck()(context.Background(), row, "alice")
	if err != nil || len(w) != 0 {
		t.Fatalf("valid limits: warnings %v err %v", w, err)
	}
	if *row.PHPMemoryLimit != "512M" || *row.PHPUploadMaxFilesize != "64M" || *row.PHPPostMaxSize != "1G" ||
		*row.PHPMaxInputVars != 3000 || *row.PHPMaxExecutionTime != 300 || *row.PHPMaxInputTime != 60 {
		t.Fatalf("valid limits changed: %+v", row)
	}
}

// A PHP limit the PHP settings page would refuse is dropped with a warning:
// the agent renders the sizes into the site's web server config.
func TestRestoreDomainCheck_DropsPHPLimitsTheSettingsPageRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   func(*models.Domain)
		unset func(*models.Domain) bool
	}{
		{"memory_limit", func(d *models.Domain) { d.PHPMemoryLimit = strp(`1";x`) }, func(d *models.Domain) bool { return d.PHPMemoryLimit == nil }},
		{"upload_max_filesize", func(d *models.Domain) { d.PHPUploadMaxFilesize = strp("2M\nx=1") }, func(d *models.Domain) bool { return d.PHPUploadMaxFilesize == nil }},
		{"post_max_size", func(d *models.Domain) { d.PHPPostMaxSize = strp("8M;") }, func(d *models.Domain) bool { return d.PHPPostMaxSize == nil }},
		{"max_input_vars", func(d *models.Domain) { d.PHPMaxInputVars = intp(0) }, func(d *models.Domain) bool { return d.PHPMaxInputVars == nil }},
		{"max_execution_time", func(d *models.Domain) { d.PHPMaxExecutionTime = intp(-5) }, func(d *models.Domain) bool { return d.PHPMaxExecutionTime == nil }},
		{"max_input_time", func(d *models.Domain) { d.PHPMaxInputTime = intp(86401) }, func(d *models.Domain) bool { return d.PHPMaxInputTime == nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rdcRow("site.org")
			row.PHPMemoryLimit = strp("256M")
			tc.set(row)
			w, err := rdcCheck()(context.Background(), row, "alice")
			if err != nil || !tc.unset(row) || len(w) != 1 || !strings.Contains(w[0], "PHP "+tc.name+" dropped") {
				t.Fatalf("got warnings %v err %v row %+v", w, err, row)
			}
			if tc.name != "memory_limit" && (row.PHPMemoryLimit == nil || *row.PHPMemoryLimit != "256M") {
				t.Fatalf("a valid limit beside it was dropped: %v", row.PHPMemoryLimit)
			}
		})
	}
}

// Unsafe optional vhost fields are dropped with a warning; the domain stays.
func TestRestoreDomainCheck_DropsUnsafeVhostFields(t *testing.T) {
	t.Run("directive the admin validator refuses", func(t *testing.T) {
		row := rdcRow("site.org")
		row.NginxCustomDirectives = strp("include /etc/shadow;")
		w, err := rdcCheck()(context.Background(), row, "alice")
		if err != nil || row.NginxCustomDirectives != nil || len(w) != 1 || !strings.Contains(w[0], "directives dropped") {
			t.Fatalf("got warnings %v err %v directives %v", w, err, row.NginxCustomDirectives)
		}
	})
	t.Run("redirect target", func(t *testing.T) {
		row := rdcRow("site.org")
		row.RedirectAllTo = strp("javascript:alert(1)")
		row.RedirectAllType = strp("301")
		w, err := rdcCheck()(context.Background(), row, "alice")
		if err != nil || row.RedirectAllTo != nil || row.RedirectAllType != nil || len(w) != 1 {
			t.Fatalf("got warnings %v err %v to %v type %v", w, err, row.RedirectAllTo, row.RedirectAllType)
		}
	})
	t.Run("redirect type", func(t *testing.T) {
		row := rdcRow("site.org")
		row.RedirectAllTo = strp("https://new.example.org")
		row.RedirectAllType = strp("200; return 403")
		w, err := rdcCheck()(context.Background(), row, "alice")
		if err != nil || row.RedirectAllTo != nil || row.RedirectAllType != nil || len(w) != 1 {
			t.Fatalf("got warnings %v err %v to %v type %v", w, err, row.RedirectAllTo, row.RedirectAllType)
		}
	})
}
