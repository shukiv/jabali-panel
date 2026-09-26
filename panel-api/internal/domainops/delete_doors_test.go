package domainops

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// JAB-279: deletion lives in this module, and every door that deletes a domain
// or retries a teardown goes through it. The doors cannot be unit-tested end to
// end here (they need Gin, a live DB, or the CLI's global init), so these pins
// read their source: each must call the module, and no second copy of the
// delete or teardown may come back in userops.

// deleteDoorSource returns the Go source at path with // line comments removed,
// so a commented-out call cannot satisfy a pin.
func deleteDoorSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "//"); idx >= 0 && !strings.Contains(line[:idx], `"`) {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

func TestDeleteDoors_RouteThroughTheModule(t *testing.T) {
	doors := []struct {
		path    string
		want    string
		banned  []string
		purpose string
	}{
		{"../api/domains.go", "domainops.Delete(", []string{"userops.DeleteDomain("}, "REST domain delete"},
		{"../../cmd/server/cli_ops.go", "domainops.Delete(", []string{"userops.DeleteDomain("}, "jabali domain delete"},
		{"../userops/userops_lifecycle.go", "domainops.Delete(", []string{"DeleteDomain("}, "user-delete cascade"},
		{"../reconciler/domain_teardowns.go", "domainops.ExecuteTeardown(", []string{"userops.ExecuteDomainTeardown("}, "tombstone retry sweep"},
	}
	for _, d := range doors {
		src := deleteDoorSource(t, d.path)
		if !strings.Contains(src, d.want) {
			t.Errorf("%s (%s) must call %s", d.purpose, d.path, d.want)
		}
		for _, b := range d.banned {
			if strings.Contains(src, b) {
				t.Errorf("%s (%s) still calls %s — the delete has one implementation, in domainops", d.purpose, d.path, b)
			}
		}
	}
}

func TestDeleteDoors_NoSecondImplementationInUserops(t *testing.T) {
	files, err := filepath.Glob("../userops/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob userops sources: %v (%d files)", err, len(files))
	}
	defs := regexp.MustCompile(`(?m)^func (DeleteDomain|ExecuteDomainTeardown|PurgeDomainMail)\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		for _, m := range defs.FindAllString(deleteDoorSource(t, f), -1) {
			t.Errorf("%s defines %q — domain deletion belongs to domainops", f, strings.TrimSpace(m))
		}
	}
}
