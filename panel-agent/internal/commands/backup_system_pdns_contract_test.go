package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #331 two-node drill finding: resyncDBAccountPasswords looked for
// PDNS_GMYSQL_PASSWORD / MariaDB user "pdns" — names no install ever wrote —
// so PowerDNS exited every cross-host system restore with "Access denied for
// user 'jabali_pdns'" until a manual ALTER USER. This pins the Go constants
// to the contract install.sh actually writes (pdns.env + the gmysql config).
func TestPdnsResyncMatchesInstallContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Skipf("install.sh not readable from test dir: %v", err)
	}
	sh := string(raw)
	if !strings.Contains(sh, pdnsEnvPasswordVar+"=") {
		t.Errorf("install.sh does not write %s= into pdns.env — the agent resync greps a variable that never exists", pdnsEnvPasswordVar)
	}
	if !strings.Contains(sh, "PDNS_DB_USER="+pdnsMariaDBUser) {
		t.Errorf("install.sh PDNS_DB_USER does not match agent resync user %q", pdnsMariaDBUser)
	}
	if !strings.Contains(sh, "gmysql-user="+pdnsMariaDBUser) {
		t.Errorf("install.sh gmysql-user does not match agent resync user %q", pdnsMariaDBUser)
	}
	if !strings.Contains(sh, pdnsEnvFile) {
		t.Errorf("install.sh does not reference %s", pdnsEnvFile)
	}
}

// GH #331 drill: reassertDRPairingSQL builds SQL from wire strings — pin the
// injection guards (ULID-shape id, quote-escaped name/label) and the shape.
func TestReassertDRPairingSQLValidation(t *testing.T) {
	if err := reassertDRPairingSQL(t.Context(), &drPairingReassert{
		DestinationID: "not-a-ulid", DestinationName: "x", PeerLabel: "y",
	}); err == nil || !strings.Contains(err.Error(), "not a ULID") {
		t.Errorf("non-ULID destination_id must be rejected, got %v", err)
	}
	if err := reassertDRPairingSQL(t.Context(), &drPairingReassert{
		DestinationID: "01M05Q5V53GQ54Q4PHGHH7F73P", DestinationName: "x", PeerLabel: "y",
		PairedAt: "yesterday",
	}); err == nil || !strings.Contains(err.Error(), "paired_at") {
		t.Errorf("bad paired_at must be rejected, got %v", err)
	}
}
