package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostnameFreeApply_WritesCredential(t *testing.T) {
	t.Setenv("JABALI_HOSTNAME_SKIP_SYSTEMD", "1")
	dir := t.TempDir()
	orig := hostnameEnvPathVar
	hostnameEnvPathVar = filepath.Join(dir, "hostname.env")
	origChown := hostnameChown
	hostnameChown = func(string, int, int) error { return nil }
	t.Cleanup(func() { hostnameEnvPathVar = orig; hostnameChown = origChown })

	raw, _ := json.Marshal(map[string]any{
		"fqdn":  "45-79-1-9.jabalihosted.com",
		"label": "45-79-1-9",
		"email": "op@example.com",
		"token": "tok-abc-123",
		"api":   "https://api.jabalihosted.com",
	})
	if _, err := hostnameFreeApplyHandler(context.Background(), raw); err != nil {
		t.Fatalf("apply: %v", err)
	}
	data, err := os.ReadFile(hostnameEnvPathVar)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"FQDN=45-79-1-9.jabalihosted.com", "TOKEN=tok-abc-123",
		"EMAIL=op@example.com", "API=https://api.jabalihosted.com",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("cred missing %q:\n%s", want, s)
		}
	}
	fi, _ := os.Stat(hostnameEnvPathVar)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("cred mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestHostnameFreeApply_Validation(t *testing.T) {
	t.Setenv("JABALI_HOSTNAME_SKIP_SYSTEMD", "1")
	dir := t.TempDir()
	orig := hostnameEnvPathVar
	hostnameEnvPathVar = filepath.Join(dir, "hostname.env")
	origChown := hostnameChown
	hostnameChown = func(string, int, int) error { return nil }
	t.Cleanup(func() { hostnameEnvPathVar = orig; hostnameChown = origChown })

	base := map[string]any{
		"fqdn": "45-79-1-9.jabalihosted.com", "label": "45-79-1-9",
		"email": "op@example.com", "token": "t", "api": "https://api.jabalihosted.com",
	}
	bad := []struct {
		name  string
		patch map[string]any
	}{
		{"foreign fqdn", map[string]any{"fqdn": "evil.example.com"}},
		{"fqdn not free base", map[string]any{"fqdn": "x.jabali-panel.com"}},
		{"http api", map[string]any{"api": "http://api.jabalihosted.com"}},
		{"newline token", map[string]any{"token": "a\nb"}},
		{"bad email", map[string]any{"email": "nope"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range base {
				m[k] = v
			}
			for k, v := range tc.patch {
				m[k] = v
			}
			raw, _ := json.Marshal(m)
			if _, err := hostnameFreeApplyHandler(context.Background(), raw); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
