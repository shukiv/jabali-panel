package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// withPDNSRebindFiles points the verb at a temp pdns config (local-address
// line localAddr; "" = no config) and drop-in (absent when !dropIn), and
// records every command it runs.
func withPDNSRebindFiles(t *testing.T, localAddr string, dropIn bool) *[]string {
	t.Helper()
	dir := t.TempDir()
	conf, drop := filepath.Join(dir, "01-jabali-mysql.conf"), filepath.Join(dir, "20-jabali-local-address.conf")
	if localAddr != "" {
		body := "launch=gmysql\nlocal-address=" + localAddr + "\nmax-tcp-connections=200\n"
		if err := os.WriteFile(conf, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if dropIn {
		if err := os.WriteFile(drop, []byte("[Service]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prevC, prevD, prevE := pdnsJabaliConfPath, pdnsLocalAddressDropIn, execCommandContext
	pdnsJabaliConfPath, pdnsLocalAddressDropIn = conf, drop
	var ran []string
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { pdnsJabaliConfPath, pdnsLocalAddressDropIn, execCommandContext = prevC, prevD, prevE })
	return &ran
}

func callPDNSRebind(t *testing.T, addrs ...string) (dnsPDNSRebindResponse, error) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"addresses": addrs})
	raw, err := dnsPDNSRebindHandler(context.Background(), params)
	if err != nil {
		return dnsPDNSRebindResponse{}, err
	}
	return raw.(dnsPDNSRebindResponse), nil
}

// The nameserver address moved to .9, which is on the interface; pdns still
// listens on .121 only, so it is restarted (its ExecStartPre helper then
// binds .9).
func TestPDNSRebind_RestartsForANewAddress(t *testing.T) {
	ran := withPDNSRebindFiles(t, "203.0.113.121:53, 127.0.0.1:5300, [::1]:5300", true)
	withServerAddrs(t, "203.0.113.121", "203.0.113.9")

	resp, err := callPDNSRebind(t, "203.0.113.9", "203.0.113.9")
	if err != nil || !resp.Restarted || strings.Join(resp.Added, ",") != "203.0.113.9" {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	if strings.Join(*ran, ";") != "systemctl try-restart pdns" {
		t.Fatalf("ran %q", *ran)
	}
}

func TestPDNSRebind_NoRestart(t *testing.T) {
	for _, tc := range []struct {
		name, localAddr string
		dropIn          bool
		reason          string
	}{
		{"already listening", "203.0.113.9:53, 127.0.0.1:5300", true, "already listening"},
		{"bare address", "203.0.113.9", true, "already listening"},
		{"helper not installed", "203.0.113.121:53", false, "pdns-local-address helper not installed"},
		{"no jabali pdns", "", true, "PowerDNS is not set up by jabali here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran := withPDNSRebindFiles(t, tc.localAddr, tc.dropIn)
			withServerAddrs(t, "203.0.113.121", "203.0.113.9")
			resp, err := callPDNSRebind(t, "203.0.113.9")
			if err != nil || resp.Restarted || !strings.HasPrefix(resp.Reason, tc.reason) {
				t.Fatalf("resp = %+v, %v", resp, err)
			}
			if len(*ran) != 0 {
				t.Fatalf("ran %q", *ran)
			}
		})
	}
}

// A nameserver hosted elsewhere is not this server's to bind.
func TestPDNSRebind_AddressNotOnThisServer(t *testing.T) {
	ran := withPDNSRebindFiles(t, "203.0.113.121:53", true)
	withServerAddrs(t, "203.0.113.121")
	resp, err := callPDNSRebind(t, "198.51.100.53")
	if err != nil || resp.Restarted || len(*ran) != 0 {
		t.Fatalf("resp = %+v, %v, ran %q", resp, err, *ran)
	}
}

func TestPDNSRebind_RejectsBadInput(t *testing.T) {
	ran := withPDNSRebindFiles(t, "203.0.113.121:53", true)
	withServerAddrs(t, "203.0.113.121", "203.0.113.9")
	for _, in := range [][]string{nil, {"203.0.113.9:53"}, {"127.0.0.1"}, {"not-an-ip"}, {"203.0.113.9", "1.1.1.1", "1.1.1.2", "1.1.1.3", "1.1.1.4"}} {
		var ae *agentwire.AgentError
		if _, err := callPDNSRebind(t, in...); !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%q: err = %v, want invalid_argument", in, err)
		}
	}
	if len(*ran) != 0 {
		t.Fatalf("ran %q", *ran)
	}
}

// The panel's side of the contract decodes into the handler's params.
func TestPDNSRebind_PanelContract(t *testing.T) {
	raw, err := os.ReadFile("../../../panel-api/internal/agent/testdata/dns_pdns_rebind_request.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var p dnsPDNSRebindParams
	if err := dec.Decode(&p); err != nil || len(p.Addresses) != 1 {
		t.Fatalf("decode = %+v, %v", p, err)
	}
}
