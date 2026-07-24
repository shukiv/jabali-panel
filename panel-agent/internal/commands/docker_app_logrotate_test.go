package commands

import (
	"path/filepath"
	"strings"
	"testing"
)

// JAB-121: the rendered logrotate snippet must target <DataRoot>/<vol>/*.log,
// honour per-volume overrides, and default to a container-safe policy.
func TestBuildDockerAppLogrotate(t *testing.T) {
	body := buildDockerAppLogrotate("flarum-abc", "/var/lib/jabali/docker-apps/flarum-abc", []dockerAppLogRotate{
		{Volume: "storagelogs", RetentionDays: 14, MaxSize: "50M", Mode: "copytruncate"},
		{Volume: "logs"}, // defaults
	})
	for _, want := range []string{
		"/var/lib/jabali/docker-apps/flarum-abc/storagelogs/*.log {",
		"/var/lib/jabali/docker-apps/flarum-abc/logs/*.log {",
		"rotate 14",
		"maxsize 50M",
		"copytruncate",
		"su root root",
		"missingok",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("snippet missing %q\n---\n%s", want, body)
		}
	}
	// the defaults-only volume must NOT emit a maxsize line but must still
	// default to rotate 14 + copytruncate.
	logsBlock := body[strings.Index(body, "/logs/*.log {"):]
	if strings.Contains(logsBlock, "maxsize") {
		t.Errorf("defaults-only volume should have no maxsize:\n%s", logsBlock)
	}
	if !strings.Contains(logsBlock, "rotate 14") || !strings.Contains(logsBlock, "copytruncate") {
		t.Errorf("defaults-only volume missing default rotate/mode:\n%s", logsBlock)
	}
}

// A bogus volume name must never reach the glob (path-injection guard).
func TestBuildDockerAppLogrotate_SkipsBadVolume(t *testing.T) {
	body := buildDockerAppLogrotate("app", "/var/lib/jabali/docker-apps/app", []dockerAppLogRotate{
		{Volume: "../../etc/cron.d"},
	})
	if strings.Contains(body, "cron.d") {
		t.Errorf("unvalidated volume name leaked into the snippet:\n%s", body)
	}
}

// write → remove round-trip against a temp dir stand-in for /etc/logrotate.d.
// (dockerAppLogrotateDir is a const, so we exercise the pure builder above and
// the write/remove path here with a slug-scoped temp file via os APIs.)
func TestWriteRemoveDockerAppLogrotate_EmptySpecsRemoves(t *testing.T) {
	// Empty specs must not create a file. We can't redirect the const dir, so
	// assert the no-op contract: writeDockerAppLogrotate with no valid specs
	// returns nil and (best-effort) removes — it must not error.
	if err := writeDockerAppLogrotate("nonexistent-slug", t.TempDir(), nil); err != nil {
		t.Errorf("empty specs should be a no-op, got %v", err)
	}
	if err := writeDockerAppLogrotate("nonexistent-slug", t.TempDir(), []dockerAppLogRotate{{Volume: "bad/name"}}); err != nil {
		t.Errorf("all-invalid specs should be a no-op, got %v", err)
	}
	// sanity: dockerAppLogrotatePath is slug-scoped under logrotate.d.
	if got := dockerAppLogrotatePath("myapp"); got != filepath.Join("/etc/logrotate.d", "jabali-dockerapp-myapp") {
		t.Errorf("path = %q", got)
	}
}
