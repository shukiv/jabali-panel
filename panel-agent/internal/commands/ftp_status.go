package commands

import (
	"context"
	"encoding/json"
	"os"
)

// ftp.status — READ-ONLY full effective FTP posture for the observed-vs-desired
// UI (JAB-259/260 phase C). Combines the live vsftpd.conf TLS state with the
// systemd/ufw runtime state so the panel can render desired-vs-effective and
// never report a false "off"/"secure" while the host disagrees.
//
// Deliberately separate from ftp.config_status: the reconciler calls that one
// every tick and it must stay lean (a single file read). This one adds the
// exec-backed active/masked/ports fields and is called on demand only.
type ftpStatusResponse struct {
	ConfExists  bool `json:"conf_exists"`
	SSLEnforced bool `json:"ssl_enforced"`
	Active      bool `json:"active"`
	Masked      bool `json:"masked"`
	PortsOpen   bool `json:"ports_open"`
}

func ftpStatusHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	resp := ftpStatusResponse{
		Active:    ftpUnitActive(ctx, ftpDisableUnit),
		Masked:    ftpUnitMasked(ctx, ftpDisableUnit),
		PortsOpen: ftpControlPortOpen(ctx),
	}
	if data, err := os.ReadFile(getVsftpdConfPath()); err == nil {
		resp.ConfExists = true
		resp.SSLEnforced = vsftpdSSLEnforced(parseVsftpdConf(string(data)))
	}
	return resp, nil
}

func init() {
	Default.Register("ftp.status", ftpStatusHandler)
}
