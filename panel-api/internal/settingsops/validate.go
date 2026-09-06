// Package settingsops is the transport-neutral owner of server-settings
// validation and the declarative effect plans the REST PATCH handler and the
// `jabali settings` CLI both execute (ADR-0083, JAB-290). It has no net/http,
// no cobra, and no os.Exit: adapters map their transport in and execute the
// returned plan under their own policy (REST best-effort async, CLI synchronous).
//
// This first vertical slice owns settings validation (moved out of internal/api
// so the CLI no longer imports the HTTP package for it) and the nginx settings
// effect plan (see nginx.go). JAB-294 (optional-module) and JAB-295 (SSH) add
// their own effect plans to this package.
package settingsops

import (
	"fmt"
	"net"
	"net/mail"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Validate does lenient input validation matching the installer. Both the REST
// PATCH and the `jabali settings` CLI call it so they apply identical rules
// (Gitea #539). Moved verbatim from internal/api.ValidateServerSettings (JAB-290).
func Validate(s *models.ServerSettings) error {
	if s.Hostname != "" && !isValidHostname(s.Hostname) {
		return fmt.Errorf("invalid hostname")
	}
	// GH #836: preview base must be a bare hostname (the wildcard label
	// is implied; NormalizePreviewBase already stripped "*." and case).
	if s.PreviewBase != "" && !isValidHostname(s.PreviewBase) {
		return fmt.Errorf("preview_base: invalid hostname")
	}
	// IPv4
	for label, v := range map[string]string{"public_ipv4": s.PublicIPv4, "ns1_ipv4": s.NS1IPv4, "ns2_ipv4": s.NS2IPv4} {
		if v == "" {
			continue
		}
		if net.ParseIP(v) == nil || net.ParseIP(v).To4() == nil {
			return fmt.Errorf("%s: not a valid IPv4", label)
		}
	}
	// IPv6 (optional)
	if s.PublicIPv6 != "" {
		ip := net.ParseIP(s.PublicIPv6)
		if ip == nil || ip.To4() != nil {
			return fmt.Errorf("public_ipv6: not a valid IPv6")
		}
	}
	// NS names
	for label, v := range map[string]string{"ns1_name": s.NS1Name, "ns2_name": s.NS2Name} {
		if v == "" {
			continue
		}
		if !isValidHostname(v) {
			return fmt.Errorf("%s: invalid hostname", label)
		}
	}
	// Admin email
	if s.AdminEmail != "" {
		if _, err := mail.ParseAddress(s.AdminEmail); err != nil {
			return fmt.Errorf("admin_email: invalid")
		}
	}
	// Timezone (optional, empty means "use OS default")
	if s.Timezone != "" && !isValidTimezone(s.Timezone) {
		return fmt.Errorf("timezone: invalid format")
	}
	// SSH port
	if s.SSHPort < 1 || s.SSHPort > 65535 {
		return fmt.Errorf("ssh_port: must be between 1 and 65535")
	}
	if s.DefaultDNSTTL != 0 && (s.DefaultDNSTTL < 60 || s.DefaultDNSTTL > 86400) {
		return fmt.Errorf("default_dns_ttl must be 60–86400 seconds (or 0 to use built-in fallback)")
	}
	// Panel brand text: free-form but capped at 60 chars.
	if len(s.PanelBrandText) > 60 {
		return fmt.Errorf("panel_brand_text: must be <= 60 chars")
	}
	// Upload cap. 0 == "use compile-time default (1 GB)"; otherwise
	// 1 MB minimum and 10 GB ceiling (matches the practical browser-
	// upload limit and the nginx vhost client_max_body_size; admins
	// wanting bigger should use SFTP/SCP).
	if s.UploadMaxSizeMB != 0 && (s.UploadMaxSizeMB < 1 || s.UploadMaxSizeMB > 10240) {
		return fmt.Errorf("upload_max_size_mb: must be 0 or between 1 and 10240")
	}
	// M13 SSH sandbox.
	if s.SSHSandboxMode != "" && s.SSHSandboxMode != "bubblewrap" && s.SSHSandboxMode != "nspawn" {
		return fmt.Errorf("ssh_sandbox_mode: must be 'bubblewrap' or 'nspawn'")
	}
	if s.DefaultNspawnImageVersion != "" && !isImageNamePattern(s.DefaultNspawnImageVersion) {
		return fmt.Errorf("default_nspawn_image_version: must match [a-z0-9-]+")
	}
	// WorkingFolder must be absolute. install.sh ensures /var/lib/
	// jabali exists at first boot; operator who points elsewhere is
	// responsible for pre-creating + ACLing that dir.
	if s.WorkingFolder != "" {
		if !strings.HasPrefix(s.WorkingFolder, "/") {
			return fmt.Errorf("working_folder: must be an absolute path")
		}
		if strings.Contains(s.WorkingFolder, "..") {
			return fmt.Errorf("working_folder: must not contain '..'")
		}
	}
	// M55 nginx tunables. Sizes/timeouts go verbatim into a config file,
	// so reject anything not matching nginx's value grammar.
	if s.NginxClientMaxBodySize != "" && !nginxSizeRE.MatchString(s.NginxClientMaxBodySize) {
		return fmt.Errorf("nginx_client_max_body_size: invalid nginx size (e.g. 50m)")
	}
	for label, v := range map[string]string{
		"nginx_keepalive_timeout":     s.NginxKeepaliveTimeout,
		"nginx_client_body_timeout":   s.NginxClientBodyTimeout,
		"nginx_client_header_timeout": s.NginxClientHeaderTimeout,
		"nginx_send_timeout":          s.NginxSendTimeout,
		"nginx_proxy_connect_timeout": s.NginxProxyConnectTimeout,
		"nginx_proxy_read_timeout":    s.NginxProxyReadTimeout,
		"nginx_proxy_send_timeout":    s.NginxProxySendTimeout,
	} {
		if v != "" && !nginxTimeRE.MatchString(v) {
			return fmt.Errorf("%s: invalid nginx time (e.g. 300s)", label)
		}
	}
	if s.NginxWorkerProcesses != "" && !nginxWorkerProcRE.MatchString(s.NginxWorkerProcesses) {
		return fmt.Errorf("nginx_worker_processes: must be 'auto' or 1-99")
	}
	if s.NginxWorkerConnections > 1048576 {
		return fmt.Errorf("nginx_worker_connections: must be <= 1048576")
	}
	if len(s.NginxCustomHTTP) > 4000 {
		return fmt.Errorf("nginx_custom_http: must be <= 4000 chars")
	}
	return nil
}

var (
	hostnameRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)
	timezoneRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+)*$`)

	// M55 nginx tunable value grammars.
	nginxSizeRE       = regexp.MustCompile(`^[0-9]+[kKmMgG]?$`)
	nginxTimeRE       = regexp.MustCompile(`^[0-9]+(ms|s|m|h)?$`)
	nginxWorkerProcRE = regexp.MustCompile(`^(auto|[1-9][0-9]?)$`)
)

func isValidHostname(s string) bool {
	if len(s) > 253 {
		return false
	}
	return hostnameRE.MatchString(s)
}

func isValidTimezone(s string) bool {
	if len(s) > 64 {
		return false
	}
	if strings.Contains(s, "..") {
		return false
	}
	if strings.HasPrefix(s, "/") {
		return false
	}
	return timezoneRE.MatchString(s)
}

// isImageNamePattern matches the [a-z0-9-]+ shape. A private copy of the
// identical helper in internal/api (packages.go still uses that one); duplicated
// here rather than importing api because api imports this package.
func isImageNamePattern(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}
