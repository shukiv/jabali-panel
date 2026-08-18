package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempAutomation443Conf redirects the include path to a temp dir for the
// duration of a test (mirrors the catchall test's catchallConfPath override).
func withTempAutomation443Conf(t *testing.T) string {
	t.Helper()
	orig := automation443ConfPath
	p := filepath.Join(t.TempDir(), "jabali-automation-443.conf")
	automation443ConfPath = p
	t.Cleanup(func() { automation443ConfPath = orig })
	return p
}

func boolPtr(b bool) *bool { return &b }

func TestConvergeAutomation443_OnWritesProxyLocation(t *testing.T) {
	p := withTempAutomation443Conf(t)

	changed, err := convergeAutomation443(context.Background(), boolPtr(true))
	if err != nil {
		t.Fatalf("converge on: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on first write")
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(got)
	// The exposed surface must be exactly the HMAC-gated automation prefix —
	// never the whole /api/ tree (that would re-expose the unauthenticated
	// internal endpoints, which have no 404 guard on the :443 hostname vhost).
	if !strings.Contains(body, "location ^~ /api/v1/automation/ {") {
		t.Errorf("on-conf missing the automation location:\n%s", body)
	}
	if strings.Contains(body, "location ^~ /api/ ") || strings.Contains(body, "location / ") {
		t.Errorf("on-conf must NOT broaden beyond /api/v1/automation/:\n%s", body)
	}
	if !strings.Contains(body, "proxy_pass http://unix:/run/jabali-panel/api.sock;") {
		t.Errorf("on-conf must proxy straight to the panel socket:\n%s", body)
	}
	// backups.create is a sync-long automation call — without the long timeout
	// nginx 504/502s it (feedback_long_agent_call_502).
	if !strings.Contains(body, "proxy_read_timeout 3600s;") {
		t.Errorf("on-conf missing the long read timeout:\n%s", body)
	}
}

func TestConvergeAutomation443_OffWritesEmptyInclude(t *testing.T) {
	p := withTempAutomation443Conf(t)

	if _, err := convergeAutomation443(context.Background(), boolPtr(false)); err != nil {
		t.Fatalf("converge off: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(got)
	if strings.Contains(body, "location") || strings.Contains(body, "proxy_pass") {
		t.Errorf("off-conf must contain no directives (empty include), got:\n%s", body)
	}
}

func TestConvergeAutomation443_Idempotent(t *testing.T) {
	withTempAutomation443Conf(t)

	if _, err := convergeAutomation443(context.Background(), boolPtr(true)); err != nil {
		t.Fatalf("first: %v", err)
	}
	changed, err := convergeAutomation443(context.Background(), boolPtr(true))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if changed {
		t.Error("expected changed=false when the desired content already on disk")
	}
}

func TestConvergeAutomation443_NilLeavesFileAlone(t *testing.T) {
	p := withTempAutomation443Conf(t)

	changed, err := convergeAutomation443(context.Background(), nil)
	if err != nil {
		t.Fatalf("nil toggle: %v", err)
	}
	if changed {
		t.Error("nil toggle must be a no-op")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("nil toggle must not create the file, stat err=%v", err)
	}
}

func TestAutomationPublicSet_Registered(t *testing.T) {
	found := false
	for _, cmd := range Default.Commands() {
		if cmd == "nginx.automation_public_set" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("nginx.automation_public_set not registered")
	}
}
