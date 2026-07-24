package dockerapp

import (
	"strings"
	"testing"
)

// looksLikeLogVolume reports whether a catalog volume is a persistent log
// directory — by name (contains "log") or container mount path (a "/log"
// segment or a "logs" leaf). JAB-121: such volumes bypass Docker's journald
// driver, so they must declare a retention policy (or the lint below fails).
func looksLikeLogVolume(v Volume) bool {
	n := strings.ToLower(v.Name)
	p := strings.ToLower(v.ContainerPath)
	if strings.Contains(n, "log") {
		return true
	}
	// "/var/log/...", ".../logs", ".../storage/logs"
	return strings.Contains(p, "/log/") || strings.HasSuffix(p, "/logs") || strings.HasSuffix(p, "/log")
}

// JAB-121 catalog lint: every persistent log volume must declare a `rotate:`
// policy so the agent installs a host logrotate snippet for it. A new catalog
// entry that persists logs without retention fails here, which is the whole
// point — it stops the unbounded-disk regression from re-entering the catalog.
func TestDockerAppLogVolumesDeclareRotation(t *testing.T) {
	cat, errs := LoadDir(repoCatalogDir(t))
	for _, e := range errs {
		t.Fatalf("catalog load error: %v", e)
	}
	for _, e := range cat.All() {
		for _, v := range e.Volumes {
			if looksLikeLogVolume(v) && v.Rotate == nil {
				t.Errorf("%s: persistent log volume %q (%s) has no `rotate:` policy — "+
					"declare retention_days/max_size/mode so the agent installs a logrotate "+
					"snippet, or rename it if it is not actually a log dir (JAB-121)",
					e.Slug, v.Name, v.ContainerPath)
			}
		}
	}
}

// The four apps flagged in JAB-121 must carry a concrete rotate policy — a
// regression guard tighter than the generic lint above.
func TestDockerAppKnownLogAppsHaveRotation(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	for slug, vol := range map[string]string{
		"flarum":     "storagelogs",
		"erpnext":    "logs",
		"onlyoffice": "logs",
		"plausible":  "clickhouse-logs",
	} {
		e, ok := cat.Get(slug)
		if !ok {
			t.Errorf("catalog missing %q", slug)
			continue
		}
		found := false
		for _, v := range e.Volumes {
			if v.Name == vol {
				found = true
				if v.Rotate == nil {
					t.Errorf("%s: log volume %q must declare rotate (JAB-121)", slug, vol)
				} else if v.Rotate.RetentionDays <= 0 {
					t.Errorf("%s: log volume %q rotate.retention_days must be > 0", slug, vol)
				}
			}
		}
		if !found {
			t.Errorf("%s: expected log volume %q not found", slug, vol)
		}
	}
}
