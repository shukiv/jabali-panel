package commands

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ftp.config_status — report the SECURITY-relevant applied FTP config by parsing
// the LIVE /etc/vsftpd.conf (JAB-260). Reading the running config observes what
// is ACTUALLY applied, not what install.sh claims it applied — a crash between
// the config write and any "applied" marker can't produce a false-converged
// read here. The reconciler compares SSLEnforced against the desired
// ftp_allow_plaintext (equality, both drift directions) and re-renders on drift.
const vsftpdConfDefault = "/etc/vsftpd.conf"

func getVsftpdConfPath() string {
	if p := os.Getenv("JABALI_VSFTPD_CONF_PATH"); p != "" {
		return p
	}
	return vsftpdConfDefault
}

type ftpConfigStatusResponse struct {
	// Exists is false when no vsftpd.conf is present (never installed).
	Exists bool `json:"exists"`
	// SSLEnforced is true only when TLS is on AND both control + data channels
	// require it — the render install.sh produces for ftp_allow_plaintext=0.
	SSLEnforced bool `json:"ssl_enforced"`
}

func ftpConfigStatusHandler(_ context.Context, _ json.RawMessage) (any, error) {
	data, err := os.ReadFile(getVsftpdConfPath())
	if err != nil {
		if os.IsNotExist(err) {
			return ftpConfigStatusResponse{Exists: false}, nil
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "read vsftpd.conf: " + err.Error()}
	}
	kv := parseVsftpdConf(string(data))
	// Absent directives read as "" — the legitimate ssl_enable=NO render (no TLS
	// block at all) correctly reports SSLEnforced=false, which equals the
	// plaintext-desired state, so it is NOT flagged as drift.
	return ftpConfigStatusResponse{
		Exists: true,
		SSLEnforced: kv["ssl_enable"] == "YES" &&
			kv["force_local_logins_ssl"] == "YES" &&
			kv["force_local_data_ssl"] == "YES",
	}, nil
}

// parseVsftpdConf maps the `key=value` directives, skipping comments/blanks.
// Last value wins (vsftpd's own semantics).
func parseVsftpdConf(s string) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return kv
}

func init() {
	Default.Register("ftp.config_status", ftpConfigStatusHandler)
}
