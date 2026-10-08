package dockerapp

import (
	"strings"
	"testing"
)

// Batch-2 catalog entries (Grafana, Odoo, ONLYOFFICE) — render each end-to-end
// so the compose templates can't silently regress to "<no value>" or fail to
// parse, and lock the behaviours each depends on behind the nginx vhost.

func TestRender_Grafana(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	g, ok := cat.Get("grafana")
	if !ok {
		t.Fatal("grafana missing from catalog")
	}
	out, err := Render(g, RenderParams{
		Slug: "grafana", Name: "metrics", Domain: "stats.example.com",
		ImageChannel: "grafana/grafana-oss:13.0.2",
		DataRoot:     "/var/lib/jabali/docker-apps/grafana",
		CPULimit:     "1.0", MemoryLimit: "512m",
		Ports: map[string]RuntimePort{"http": {HostPort: 10050, ContainerPort: 3000, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		Env:   map[string]string{"ADMIN_PASSWORD": "grafanapw"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertNoUnresolved(t, out)
	for _, n := range []string{
		"image: grafana/grafana-oss:13.0.2",
		`GF_SERVER_ROOT_URL: "https://stats.example.com"`,
		`GF_SECURITY_ADMIN_PASSWORD: "grafanapw"`,
		`GF_USERS_ALLOW_SIGN_UP: "false"`,
		`"127.0.0.1:10050:3000/tcp"`,
		"/var/lib/jabali/docker-apps/grafana/data:/var/lib/grafana",
	} {
		mustContain(t, out, n)
	}
}

func TestRender_Odoo(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	o, ok := cat.Get("odoo")
	if !ok {
		t.Fatal("odoo missing from catalog")
	}
	out, err := Render(o, RenderParams{
		Slug: "odoo", Name: "erp", Domain: "erp.example.com",
		ImageChannel: "odoo:18.0",
		DataRoot:     "/var/lib/jabali/docker-apps/odoo",
		CPULimit:     "2.0", MemoryLimit: "1g",
		Ports: map[string]RuntimePort{"http": {HostPort: 10060, ContainerPort: 8069, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		Env:   map[string]string{"POSTGRES_PASSWORD": "odoopw"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertNoUnresolved(t, out)
	for _, n := range []string{
		"image: odoo:18.0",
		// Proxy trust is load-bearing behind the vhost; Odoo 20 listens on
		// loopback inside the container unless told otherwise (GH #1956).
		"command: odoo --proxy-mode --http-interface=0.0.0.0",
		`PASSWORD: "odoopw"`,
		"HOST: postgres",
		`"127.0.0.1:10060:8069/tcp"`,
		"/var/lib/jabali/docker-apps/odoo/odoo-data:/var/lib/odoo",
		"/var/lib/jabali/docker-apps/odoo/postgres:/var/lib/postgresql/data",
		"image: postgres:17-alpine",
		"pg_isready -U odoo",
	} {
		mustContain(t, out, n)
	}
}

func TestRender_OnlyOffice(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	oo, ok := cat.Get("onlyoffice")
	if !ok {
		t.Fatal("onlyoffice missing from catalog")
	}
	out, err := Render(oo, RenderParams{
		Slug: "onlyoffice", Name: "docs", Domain: "docs.example.com",
		ImageChannel: "onlyoffice/documentserver:9.4.0",
		DataRoot:     "/var/lib/jabali/docker-apps/onlyoffice",
		CPULimit:     "2.0", MemoryLimit: "2g",
		Ports: map[string]RuntimePort{"http": {HostPort: 10070, ContainerPort: 80, BindInterface: "127.0.0.1", Protocol: "tcp"}},
		Env:   map[string]string{"JWT_SECRET": "jwtsecretvalue"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertNoUnresolved(t, out)
	for _, n := range []string{
		"image: onlyoffice/documentserver:9.4.0",
		`JWT_ENABLED: "true"`,
		`JWT_SECRET: "jwtsecretvalue"`,
		`"127.0.0.1:10070:80/tcp"`,
		"/var/lib/jabali/docker-apps/onlyoffice/data:/var/www/onlyoffice/Data",
		"/var/lib/jabali/docker-apps/onlyoffice/db:/var/lib/postgresql",
	} {
		mustContain(t, out, n)
	}
}

func assertNoUnresolved(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "<no value>") {
		t.Fatalf("template left an unresolved variable:\n%s", out)
	}
}

func mustContain(t *testing.T, out, needle string) {
	t.Helper()
	if !strings.Contains(out, needle) {
		t.Errorf("rendered compose missing %q.\nfull output:\n%s", needle, out)
	}
}
