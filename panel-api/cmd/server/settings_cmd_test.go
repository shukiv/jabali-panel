package main

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestSettingsRegistryApply covers Gitea #539 criterion #4: unknown keys and
// invalid values are rejected; valid values apply and flag nginx side effects.
func TestSettingsRegistryApply(t *testing.T) {
	t.Run("unknown key rejected", func(t *testing.T) {
		if _, ok := settableKeys["does_not_exist"]; ok {
			t.Fatal("unexpected key in registry")
		}
	})

	cases := []struct {
		name    string
		key     string
		val     string
		wantErr bool
		check   func(t *testing.T, s *models.ServerSettings)
	}{
		{name: "bool true", key: "webmail_enabled", val: "true", check: func(t *testing.T, s *models.ServerSettings) {
			if !s.WebmailEnabled {
				t.Fatal("want WebmailEnabled true")
			}
		}},
		{name: "bool off alias", key: "postgres_enabled", val: "off", check: func(t *testing.T, s *models.ServerSettings) {
			if s.PostgresEnabled {
				t.Fatal("want PostgresEnabled false")
			}
		}},
		{name: "bool invalid", key: "webmail_enabled", val: "maybe", wantErr: true},
		{name: "nginx str", key: "nginx_client_max_body_size", val: "50m", check: func(t *testing.T, s *models.ServerSettings) {
			if s.NginxClientMaxBodySize != "50m" {
				t.Fatalf("want 50m got %q", s.NginxClientMaxBodySize)
			}
		}},
		{name: "nginx worker_connections valid", key: "nginx_worker_connections", val: "1024", check: func(t *testing.T, s *models.ServerSettings) {
			if s.NginxWorkerConnections != 1024 {
				t.Fatalf("want 1024 got %d", s.NginxWorkerConnections)
			}
		}},
		{name: "nginx worker_connections invalid", key: "nginx_worker_connections", val: "lots", wantErr: true},
		{name: "dns preset valid", key: "dns_user_record_policy_preset", val: "hosting-safe"},
		{name: "dns preset invalid", key: "dns_user_record_policy_preset", val: "nope", wantErr: true},
		{name: "hostname fqdn", key: "hostname", val: "  182-54-236-60-b.jabalihosted.com  ", check: func(t *testing.T, s *models.ServerSettings) {
			if s.Hostname != "182-54-236-60-b.jabalihosted.com" {
				t.Fatalf("want trimmed fqdn, got %q", s.Hostname)
			}
		}},
		{name: "hostname empty rejected", key: "hostname", val: "   ", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nginxDirty = false
			def, ok := settableKeys[tc.key]
			if !ok {
				t.Fatalf("key %q not in registry", tc.key)
			}
			s := &models.ServerSettings{ID: 1, SSHPort: 22}
			err := def.apply(s, tc.val)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %s=%s", tc.key, tc.val)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, s)
			}
		})
	}
}

// TestSettingsHostnameParity covers the JAB-213 CLI gap: `settings set
// hostname=` must (1) reach the same FQDN validation the REST PATCH uses via
// api.ValidateServerSettings, and (2) be detectable as a change so the
// system.set_hostname side effect fires. Without a CLI writer for hostname the
// free-hostname conversion could not be driven headlessly.
func TestSettingsHostnameParity(t *testing.T) {
	def := settableKeys["hostname"]

	t.Run("valid fqdn passes shared validation", func(t *testing.T) {
		s := &models.ServerSettings{ID: 1, SSHPort: 22}
		if err := def.apply(s, "182-54-236-60-b.jabalihosted.com"); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if err := api.ValidateServerSettings(s); err != nil {
			t.Fatalf("ValidateServerSettings rejected a valid fqdn: %v", err)
		}
	})

	t.Run("invalid fqdn rejected by shared validation", func(t *testing.T) {
		s := &models.ServerSettings{ID: 1, SSHPort: 22}
		// Passes the setter's non-empty guard but must fail hostnameRE.
		if err := def.apply(s, "bad host!name"); err != nil {
			t.Fatalf("apply should defer format checks to ValidateServerSettings: %v", err)
		}
		if err := api.ValidateServerSettings(s); err == nil {
			t.Fatal("ValidateServerSettings accepted an invalid hostname")
		}
	})

	t.Run("change is detectable for the side effect", func(t *testing.T) {
		s := &models.ServerSettings{ID: 1, SSHPort: 22, Hostname: "mx.jabali-panel.com"}
		prev := sideEffectSnapshot(s)
		if err := def.apply(s, "182-54-236-60-b.jabalihosted.com"); err != nil {
			t.Fatal(err)
		}
		if s.Hostname == prev.hostname {
			t.Fatal("hostname change not detectable — system.set_hostname would not fire")
		}
	})

	t.Run("hostname key does not flip nginxDirty", func(t *testing.T) {
		nginxDirty = false
		s := &models.ServerSettings{ID: 1, SSHPort: 22}
		if err := def.apply(s, "host.example.com"); err != nil {
			t.Fatal(err)
		}
		if nginxDirty {
			t.Fatal("hostname must not trigger the nginx side effect")
		}
	})
}

// TestSettingsNginxDirtyFlag verifies nginx-key setters flip nginxDirty so the
// nginx.tunables.apply side effect fires, while non-nginx keys do not.
func TestSettingsNginxDirtyFlag(t *testing.T) {
	nginxDirty = false
	s := &models.ServerSettings{ID: 1, SSHPort: 22}
	if err := settableKeys["webmail_enabled"].apply(s, "true"); err != nil {
		t.Fatal(err)
	}
	if nginxDirty {
		t.Fatal("non-nginx key must not set nginxDirty")
	}
	if err := settableKeys["nginx_gzip"].apply(s, "off"); err != nil {
		t.Fatal(err)
	}
	if !nginxDirty {
		t.Fatal("nginx key must set nginxDirty")
	}
	nginxDirty = false
}
