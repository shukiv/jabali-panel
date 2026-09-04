package settingsops

import (
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// base is a minimally-valid settings struct (SSHPort in range); each case
// mutates one field to exercise a single validation branch.
func base() *models.ServerSettings { return &models.ServerSettings{SSHPort: 22} }

func TestValidate_Valid(t *testing.T) {
	s := base()
	s.Hostname = "mx.example.com"
	s.PublicIPv4 = "192.0.2.10"
	s.PublicIPv6 = "2001:db8::1"
	s.AdminEmail = "ops@example.com"
	s.Timezone = "Europe/Amsterdam"
	s.NginxClientMaxBodySize = "50m"
	s.NginxKeepaliveTimeout = "65s"
	s.NginxWorkerProcesses = "auto"
	s.NginxWorkerConnections = 1024
	require.NoError(t, Validate(s))
}

func TestValidate_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*models.ServerSettings)
		msg    string
	}{
		{"bad hostname", func(s *models.ServerSettings) { s.Hostname = "bad host!name" }, "invalid hostname"},
		{"bad preview_base", func(s *models.ServerSettings) { s.PreviewBase = "no spaces!" }, "preview_base"},
		{"bad ipv4", func(s *models.ServerSettings) { s.PublicIPv4 = "999.1.1.1" }, "public_ipv4"},
		{"ipv6 in ipv4 field", func(s *models.ServerSettings) { s.NS1IPv4 = "2001:db8::1" }, "ns1_ipv4"},
		{"bad ipv6", func(s *models.ServerSettings) { s.PublicIPv6 = "192.0.2.1" }, "public_ipv6"},
		{"bad ns name", func(s *models.ServerSettings) { s.NS2Name = "bad ns!" }, "ns2_name"},
		{"bad email", func(s *models.ServerSettings) { s.AdminEmail = "not-an-email" }, "admin_email"},
		{"bad timezone", func(s *models.ServerSettings) { s.Timezone = "../etc" }, "timezone"},
		{"ssh port low", func(s *models.ServerSettings) { s.SSHPort = 0 }, "ssh_port"},
		{"dns ttl range", func(s *models.ServerSettings) { s.DefaultDNSTTL = 10 }, "default_dns_ttl"},
		{"brand too long", func(s *models.ServerSettings) { s.PanelBrandText = string(make([]byte, 61)) }, "panel_brand_text"},
		{"upload cap range", func(s *models.ServerSettings) { s.UploadMaxSizeMB = 20000 }, "upload_max_size_mb"},
		{"sandbox mode", func(s *models.ServerSettings) { s.SSHSandboxMode = "docker" }, "ssh_sandbox_mode"},
		{"nspawn image", func(s *models.ServerSettings) { s.DefaultNspawnImageVersion = "Bad_Image" }, "default_nspawn_image_version"},
		{"working folder relative", func(s *models.ServerSettings) { s.WorkingFolder = "relative/path" }, "working_folder"},
		{"working folder dotdot", func(s *models.ServerSettings) { s.WorkingFolder = "/var/../etc" }, "working_folder"},
		{"nginx size", func(s *models.ServerSettings) { s.NginxClientMaxBodySize = "50megs" }, "nginx_client_max_body_size"},
		{"nginx time", func(s *models.ServerSettings) { s.NginxSendTimeout = "1minute" }, "nginx_send_timeout"},
		{"nginx worker_processes", func(s *models.ServerSettings) { s.NginxWorkerProcesses = "many" }, "nginx_worker_processes"},
		{"nginx worker_connections", func(s *models.ServerSettings) { s.NginxWorkerConnections = 2000000 }, "nginx_worker_connections"},
		{"nginx custom_http too long", func(s *models.ServerSettings) { s.NginxCustomHTTP = string(make([]byte, 4001)) }, "nginx_custom_http"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.mutate(s)
			err := Validate(s)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.msg)
		})
	}
}
