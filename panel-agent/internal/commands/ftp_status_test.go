package commands

import (
	"context"
	"testing"
)

// ftp.status combines the live conf TLS state with the runtime unit state.
// Under the default exec stub, systemctl/ufw emit nothing (inactive/unmasked/
// ports-closed); the conf is read from the JABALI_VSFTPD_CONF_PATH file.
func TestFtpStatus_ReadsConfAndRuntime(t *testing.T) {
	writeConf(t, `ssl_enable=YES
force_local_logins_ssl=YES
force_local_data_ssl=YES
`)
	res, err := ftpStatusHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	st := res.(ftpStatusResponse)
	if !st.ConfExists || !st.SSLEnforced {
		t.Fatalf("want conf_exists + ssl_enforced from the TLS render, got %+v", st)
	}
	// Default stub: systemctl is-active/is-enabled emit "" → inactive, unmasked.
	if st.Active {
		t.Fatal("stubbed systemctl must report inactive")
	}
}

func TestFtpStatus_NoConf(t *testing.T) {
	t.Setenv("JABALI_VSFTPD_CONF_PATH", t.TempDir()+"/absent.conf")
	res, err := ftpStatusHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if res.(ftpStatusResponse).ConfExists {
		t.Fatal("absent conf must report conf_exists=false")
	}
}
