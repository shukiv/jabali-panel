package dockerapp

import (
	"strings"
	"testing"
)

// GH #2501: EuroOffice must start on a fresh install. The image ships its
// PostgreSQL cluster ready-made and its entrypoint never runs initdb, so a
// db bind mount over /var/lib/postgresql hides the cluster and the container
// restart-loops. The logs volume arrives empty and hides the image's log
// subdirectories, which supervisord requires, so the compose entrypoint must
// create them before exec'ing the image entrypoint.
func TestCatalogEuroOfficeStartsOnFreshInstall(t *testing.T) {
	cat, errs := LoadDir(repoCatalogDir(t))
	for _, e := range errs {
		t.Fatalf("catalog load error: %v", e)
	}
	e, ok := cat.Get("eurooffice")
	if !ok {
		t.Fatal("catalog missing eurooffice")
	}
	for _, v := range e.Volumes {
		if v.ContainerPath == "/var/lib/postgresql" {
			t.Errorf("eurooffice volume %q mounts /var/lib/postgresql — a bind mount hides the image's ready-made cluster (GH #2501)", v.Name)
		}
	}
	tmpl := e.ComposeTemplate()
	for _, want := range []string{
		"mkdir -p ",
		"/var/log/euro-office/documentserver/adminpanel",
		"/var/log/euro-office/documentserver/converter",
		"/var/log/euro-office/documentserver/docservice",
		"/var/log/euro-office/documentserver/metrics",
		"exec /entrypoint.sh",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("eurooffice compose template missing %q (GH #2501)", want)
		}
	}
}
