package reconciler

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ssl_acme_webroot_test.go — a docker-app proxy domain has no document root
// (DocRoot ""), but its vhost answers /.well-known/acme-challenge/ from the
// shared ACME root (panel-agent docker_app_vhost.go). The reconciler handed
// ssl.issue the empty DocRoot instead, the agent refused it, and such a domain
// never got a Let's Encrypt certificate unless a CDN sent it to DNS-01.

func TestACMEWebroot(t *testing.T) {
	cases := []struct {
		name string
		d    models.Domain
		want string
	}{
		{"tenant domain answers from its docroot",
			models.Domain{DocRoot: "/home/u1/domains/a.example.com/public_html"}, "/home/u1/domains/a.example.com/public_html"},
		{"docker-app domain answers from the shared ACME root",
			models.Domain{ManagedBy: models.DomainManagedByDockerApp}, "/var/www/jabali-acme"},
		{"panel hostname has none",
			models.Domain{IsPanelPrimary: true}, ""},
	}
	for _, tc := range cases {
		if got := acmeWebroot(&tc.d); got != tc.want {
			t.Errorf("%s: acmeWebroot = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The upgrade from the self-signed placeholder reaches ssl.issue with the
// shared ACME root.
func TestReconcileSSL_DockerApp_IssuesFromSharedACMERoot(t *testing.T) {
	r, ag, sc, _ := sslWebrootFixture(t, nil)
	dom := &models.Domain{
		OwnershipState: verifiedOwnership,
		ID:             "d3", Name: "app.example.com", UserID: "admin",
		DocRoot: "", SSLMode: models.SSLModeLE, IsEnabled: true,
		ManagedBy: models.DomainManagedByDockerApp,
	}
	r.domains.(*fakeDomainRepo).domains[dom.ID] = dom
	cp, kp := "/etc/ssl/jabali-selfsigned/app.example.com/fullchain.pem", "/etc/ssl/jabali-selfsigned/app.example.com/privkey.pem"
	exp := time.Now().UTC().Add(300 * 24 * time.Hour)
	sc.byDomain[dom.ID] = &models.SSLCertificate{ID: "c3", DomainID: dom.ID, Status: models.SSLStatusSelfSigned, CertPath: &cp, KeyPath: &kp, ExpiresAt: &exp}

	r.reconcileSSLForDomain(context.Background(), dom)

	calls := agentCalledWith(ag, "ssl.issue")
	if len(calls) != 1 {
		t.Fatalf("want one ssl.issue for the docker-app domain, got %d", len(calls))
	}
	if calls[0]["webroot"] != "/var/www/jabali-acme" {
		t.Fatalf("ssl.issue webroot = %v, want /var/www/jabali-acme", calls[0]["webroot"])
	}
}

// The SAN top-up (expandCertSANsForDrift) sends ssl.issue too. It resolves
// names against public DNS, so pin its webroot in the source.
func TestSANDrift_UsesACMEWebroot(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "ssl_san_drift.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "expandCertSANsForDrift" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("expandCertSANsForDrift not found")
	}
	// The variable that holds acmeWebroot(dom), and what "webroot" is sent as.
	fromHelper := map[string]bool{}
	var sent ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Lhs) == 1 && len(x.Rhs) == 1 {
				if c, ok := x.Rhs[0].(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "acmeWebroot" {
						if l, ok := x.Lhs[0].(*ast.Ident); ok {
							fromHelper[l.Name] = true
						}
					}
				}
			}
		case *ast.KeyValueExpr:
			if k, ok := x.Key.(*ast.BasicLit); ok && k.Value == `"webroot"` {
				sent = x.Value
			}
		}
		return true
	})
	ok := false
	switch v := sent.(type) {
	case *ast.Ident:
		ok = fromHelper[v.Name]
	case *ast.CallExpr:
		id, isID := v.Fun.(*ast.Ident)
		ok = isID && id.Name == "acmeWebroot"
	}
	if !ok {
		t.Fatal(`expandCertSANsForDrift must send "webroot" from acmeWebroot(dom), not the bare DocRoot`)
	}
}
