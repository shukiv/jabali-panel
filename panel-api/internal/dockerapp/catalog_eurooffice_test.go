package dockerapp

import (
	"strings"
	"testing"
)

// GH #2501: EuroOffice must start on a fresh install and its logs must rotate
// safely. The image ships its PostgreSQL cluster ready-made and its entrypoint
// never runs initdb, so a db bind mount over /var/lib/postgresql hides the
// cluster and the container restart-loops. Each supervisord log folder gets
// its own volume: a volume over the parent log dir would hide the subfolders
// the image writes to, and logs one folder below a volume would need a nested
// logrotate glob — those follow symlinked directories, so a container could
// point one at /var/log and have root rotate the host's logs.
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
		if v.ContainerPath == "/var/log/euro-office/documentserver" {
			t.Errorf("eurooffice volume %q mounts the parent log dir — it hides the image's log subfolders and puts logs one folder below the volume, out of rotation's reach (GH #2501)", v.Name)
		}
	}
	logFolders := []string{"adminpanel", "converter", "docservice", "metrics"}
	for _, x := range logFolders {
		path := "/var/log/euro-office/documentserver/" + x
		found := false
		for _, v := range e.Volumes {
			if v.ContainerPath != path {
				continue
			}
			found = true
			if v.Rotate == nil {
				t.Errorf("eurooffice volume %q (%s) has no rotate policy (GH #2501)", v.Name, path)
			}
		}
		if !found {
			t.Errorf("eurooffice has no volume for log folder %s (GH #2501)", path)
		}
		mount := "{{ .DataRoot }}/logs-" + x + ":" + path
		if !strings.Contains(e.ComposeTemplate(), mount) {
			t.Errorf("eurooffice compose template missing mount %q (GH #2501)", mount)
		}
	}
}
