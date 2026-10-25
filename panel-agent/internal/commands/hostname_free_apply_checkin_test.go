package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Activation must send the first heartbeat at once: it marks the label in use
// at the service, so a later claim from the same server and email can never
// reissue it away from this box. It must not run the DNS-01 wildcard script,
// which wrote the panel cert's own lineage and spent a second certificate
// from the shared jabalihosted.com quota.
func TestHostnameFreeApply_ChecksInAndSkipsWildcardCert(t *testing.T) {
	t.Setenv("JABALI_HOSTNAME_SKIP_SYSTEMD", "")
	dir := t.TempDir()
	origPath, origChown := hostnameEnvPathVar, hostnameChown
	origCtx, origCmd := execCommandContext, execCommand
	var calls []string
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return exec.CommandContext(ctx, "true")
	}
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return exec.Command("true")
	}
	hostnameEnvPathVar = filepath.Join(dir, "hostname.env")
	hostnameChown = func(string, int, int) error { return nil }
	t.Cleanup(func() {
		hostnameEnvPathVar, hostnameChown = origPath, origChown
		execCommandContext, execCommand = origCtx, origCmd
	})

	raw, _ := json.Marshal(map[string]any{
		"fqdn": "45-79-1-9.jabalihosted.com", "label": "45-79-1-9",
		"email": "op@example.com", "token": "abc123", "api": "https://api.jabalihosted.com",
	})
	if _, err := hostnameFreeApplyHandler(context.Background(), raw); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []string{
		"systemctl enable --now jabali-hostname-heartbeat.timer",
		"systemctl start --no-block jabali-hostname-heartbeat.service",
	}
	got := strings.Join(calls, "\n")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q; calls:\n%s", w, got)
		}
	}
	if strings.Contains(got, "jabali-hostname-cert") {
		t.Errorf("activation ran the wildcard cert script; calls:\n%s", got)
	}
}
