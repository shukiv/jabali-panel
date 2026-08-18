package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeConf(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "vsftpd.conf")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JABALI_VSFTPD_CONF_PATH", p)
}

func configStatus(t *testing.T) ftpConfigStatusResponse {
	t.Helper()
	res, err := ftpConfigStatusHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("config_status: %v", err)
	}
	return res.(ftpConfigStatusResponse)
}

func TestFtpConfigStatus_TLSEnforced(t *testing.T) {
	writeConf(t, `# managed
listen=YES
ssl_enable=YES
force_local_logins_ssl=YES
force_local_data_ssl=YES
`)
	st := configStatus(t)
	if !st.Exists || !st.SSLEnforced {
		t.Fatalf("want exists+enforced, got %+v", st)
	}
}

func TestFtpConfigStatus_PlaintextSslDisabled(t *testing.T) {
	// The legitimate plaintext render: ssl_enable=NO, no force_local_* block.
	writeConf(t, `listen=YES
ssl_enable=NO
`)
	if configStatus(t).SSLEnforced {
		t.Fatal("ssl_enable=NO must report SSLEnforced=false, not drift")
	}
}

func TestFtpConfigStatus_TLSOnButNotForced(t *testing.T) {
	// TLS available but not required on both channels → NOT enforced (a stale
	// pre-tighten config an on-path attacker can still downgrade).
	writeConf(t, `ssl_enable=YES
force_local_logins_ssl=NO
force_local_data_ssl=NO
`)
	if configStatus(t).SSLEnforced {
		t.Fatal("force_local_*_ssl=NO must report not-enforced")
	}
}

func TestFtpConfigStatus_MissingFile(t *testing.T) {
	t.Setenv("JABALI_VSFTPD_CONF_PATH", filepath.Join(t.TempDir(), "does-not-exist.conf"))
	st := configStatus(t)
	if st.Exists {
		t.Fatal("missing conf must report exists=false")
	}
}
