package commands

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// setupPanelMailSelfSign points the mail cert paths at a temp dir and stubs
// the reload seam, counting the reloads. Restores the globals on cleanup.
func setupPanelMailSelfSign(t *testing.T) (certPath, keyPath string, reloads *int) {
	t.Helper()
	dir := t.TempDir()
	certPath = filepath.Join(dir, "panel-mail.crt")
	keyPath = filepath.Join(dir, "panel-mail.key")
	oc, ok, or := panelMailSelfSignCertPath, panelMailSelfSignKeyPath, panelMailSelfSignReloadFn
	n := 0
	panelMailSelfSignCertPath, panelMailSelfSignKeyPath = certPath, keyPath
	panelMailSelfSignReloadFn = func(context.Context) { n++ }
	t.Cleanup(func() {
		panelMailSelfSignCertPath, panelMailSelfSignKeyPath, panelMailSelfSignReloadFn = oc, ok, or
	})
	return certPath, keyPath, &n
}

func callPanelMailSelfSign(t *testing.T, names ...string) (sslPanelMailSelfSignResponse, error) {
	t.Helper()
	params, _ := json.Marshal(sslPanelMailSelfSignParams{Names: names})
	raw, err := sslPanelMailSelfSignHandler(context.Background(), params)
	if err != nil {
		return sslPanelMailSelfSignResponse{}, err
	}
	b, _ := json.Marshal(raw)
	var resp sslPanelMailSelfSignResponse
	if e := json.Unmarshal(b, &resp); e != nil {
		t.Fatalf("unmarshal resp: %v", e)
	}
	return resp, nil
}

// GH #2166: with Let's Encrypt off and no mail cert, the mail names get a
// self-signed cert with its own key, and nginx + Stalwart are reloaded.
func TestPanelMailSelfSign_WritesCertForTheMailNames(t *testing.T) {
	certPath, keyPath, reloads := setupPanelMailSelfSign(t)

	resp, err := callPanelMailSelfSign(t, "mx.example.net", "mail.panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Regenerated || resp.ExpiresAt == "" || resp.CertPath != certPath {
		t.Fatalf("resp %+v", resp)
	}
	cert, cn := readCertCN_SANs(t, certPath)
	if cn != "mx.example.net" || strings.Join(cert.DNSNames, ",") != "mx.example.net,mail.panel.example.com" {
		t.Errorf("CN %q SANs %v", cn, cert.DNSNames)
	}
	if len(cert.Issuer.Organization) != 1 || cert.Issuer.Organization[0] != "Jabali Panel" || len(cert.IPAddresses) != 0 {
		t.Errorf("issuer %v IPs %v, want the panel's self-signed mark and no IPs", cert.Issuer.Organization, cert.IPAddresses)
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Errorf("cert and key don't match: %v", err)
	}
	for _, p := range []string{certPath, keyPath} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o640 {
			t.Errorf("%s mode %v (%v), want 0640 as the deploy hook installs it", filepath.Base(p), fi.Mode().Perm(), err)
		}
	}
	if *reloads != 1 {
		t.Errorf("reloads %d, want 1", *reloads)
	}
}

// The hostname cert and its key are never touched or shared.
func TestPanelMailSelfSign_LeavesTheHostnameCertAlone(t *testing.T) {
	hostCert, hostKey, _ := setupPanelSelfSign(t)
	writeTestCert(t, hostCert, hostKey, "Jabali Panel", "panel.example.com",
		[]string{"panel.example.com", "mail.panel.example.com"}, time.Now().AddDate(10, 0, 0))
	before, _ := os.ReadFile(hostKey)
	_, mailKey, _ := setupPanelMailSelfSign(t)

	if _, err := callPanelMailSelfSign(t, "mail.panel.example.com"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(hostKey)
	mail, _ := os.ReadFile(mailKey)
	if string(before) != string(after) {
		t.Fatal("the hostname key changed")
	}
	if string(mail) == string(after) {
		t.Fatal("the mail cert reuses the hostname key")
	}
}

// A live Let's Encrypt mail cert is kept until it expires: no write, no
// reload, no Stalwart restart.
func TestPanelMailSelfSign_PreservesLiveLECert(t *testing.T) {
	certPath, keyPath, reloads := setupPanelMailSelfSign(t)
	exp := time.Now().AddDate(0, 2, 0)
	writeTestCert(t, certPath, keyPath, "Let's Encrypt", "mail.panel.example.com", []string{"mail.panel.example.com"}, exp)
	before, _ := os.ReadFile(certPath)

	resp, err := callPanelMailSelfSign(t, "mail.panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(certPath)
	if resp.Regenerated || string(before) != string(after) || *reloads != 0 {
		t.Fatalf("live LE mail cert not preserved: %+v, reloads %d", resp, *reloads)
	}
	if resp.Reason != "preserved: cert issued by Let's Encrypt" || resp.ExpiresAt != exp.UTC().Truncate(time.Second).Format(time.RFC3339) {
		t.Errorf("resp %+v", resp)
	}
}

func TestPanelMailSelfSign_ReplacesExpiredLECert(t *testing.T) {
	certPath, keyPath, reloads := setupPanelMailSelfSign(t)
	writeTestCert(t, certPath, keyPath, "Let's Encrypt", "mail.panel.example.com", []string{"mail.panel.example.com"}, time.Now().Add(-time.Hour))

	resp, err := callPanelMailSelfSign(t, "mail.panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := readCertCN_SANs(t, certPath)
	if !resp.Regenerated || cert.Issuer.Organization[0] != "Jabali Panel" || *reloads != 1 {
		t.Fatalf("expired LE mail cert kept: %+v, issuer %v", resp, cert.Issuer.Organization)
	}
}

// No churn: a current self-signed cert for the names is left alone, so a
// reconciler tick never restarts Stalwart for nothing.
func TestPanelMailSelfSign_NoChurnWhenCovered(t *testing.T) {
	certPath, _, reloads := setupPanelMailSelfSign(t)
	if _, err := callPanelMailSelfSign(t, "mail.panel.example.com"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(certPath)
	resp, err := callPanelMailSelfSign(t, "mail.panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(certPath)
	if resp.Regenerated || string(before) != string(after) || *reloads != 1 {
		t.Fatalf("second run churned: %+v, reloads %d", resp, *reloads)
	}
	// A new name (a custom mail hostname applied) regenerates.
	if resp, _ := callPanelMailSelfSign(t, "mx.example.net", "mail.panel.example.com"); !resp.Regenerated {
		t.Fatalf("new names kept the old cert: %+v", resp)
	}
}

func TestPanelMailSelfSign_RejectsBadNames(t *testing.T) {
	certPath, _, reloads := setupPanelMailSelfSign(t)
	for name, names := range map[string][]string{
		"none":      nil,
		"too many":  {"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com"},
		"invalid":   {"mail.example.com; rm -rf /"},
		"wildcard":  {"*.example.com"},
		"duplicate": {"mail.example.com", "MAIL.example.com"},
	} {
		_, err := callPanelMailSelfSign(t, names...)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%s: err %v, want invalid argument", name, err)
		}
	}
	if _, err := os.Stat(certPath); !os.IsNotExist(err) || *reloads != 0 {
		t.Fatalf("a rejected call wrote or reloaded (%v, %d)", err, *reloads)
	}
}

// The reconciler and the admin's action can dispatch at once; the second
// run waits, then finds the cert up to date.
func TestPanelMailSelfSign_RunsOneAtATime(t *testing.T) {
	certPath, keyPath, _ := setupPanelMailSelfSign(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	var mu sync.Mutex
	runs := 0
	panelMailSelfSignReloadFn = func(context.Context) {
		mu.Lock()
		runs++
		mu.Unlock()
		entered <- struct{}{}
		<-release
	}
	var wg sync.WaitGroup
	results := make([]sslPanelMailSelfSignResponse, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = callPanelMailSelfSign(t, "mail.panel.example.com")
		}(i)
	}
	<-entered
	select {
	case <-entered:
		t.Fatal("two runs regenerated at once")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if runs != 1 || results[0].Regenerated == results[1].Regenerated {
		t.Fatalf("runs %d, results %+v", runs, results)
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("cert and key don't match: %v", err)
	}
}

// recordPanelMailReload stubs the commands the reload runs and points the
// push-cert helper and the Stalwart binary at temp files, with the Stalwart
// user set to the test's own user, so the box looks like it has the mail
// server. It returns the push-cert path and the commands run so far.
func recordPanelMailReload(t *testing.T) (push string, ran func() []string) {
	t.Helper()
	var got []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, strings.TrimSpace(name+" "+strings.Join(args, " ")))
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	dir := t.TempDir()
	push = filepath.Join(dir, "push-cert")
	bin := filepath.Join(dir, "stalwart")
	for _, p := range []string{push, bin} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	oldPush, oldUser, oldBin := panelMailPushCertPath, panelMailStalwartUser, panelMailStalwartBinary
	panelMailPushCertPath, panelMailStalwartUser, panelMailStalwartBinary = push, me.Username, bin
	t.Cleanup(func() {
		panelMailPushCertPath, panelMailStalwartUser, panelMailStalwartBinary = oldPush, oldUser, oldBin
	})
	return push, func() []string { return got }
}

// Stalwart loads certificates only at startup: the push comes before the
// restart, as in the deploy hook. nginx reloads for webmail.
func TestPanelMailSelfSign_ReloadOrder(t *testing.T) {
	push, ran := recordPanelMailReload(t)

	defaultPanelMailSelfSignReload(context.Background())
	want := []string{
		"systemctl reload nginx",
		"systemctl start jabali-stalwart",
		push,
		"systemctl restart jabali-stalwart",
	}
	if got := ran(); strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("ran %q, want %q", got, want)
	}
}

// GH #2166: a box without the mail server still has jabali-stalwart.service
// (`jabali update` installs it everywhere), but not the user it runs as or
// its binary. Starting it there fails at step USER and restarts in a loop,
// each failure a "Stalwart is down" alert. The reload leaves Stalwart alone
// unless both are there; nginx still reloads.
func TestPanelMailSelfSign_NoMailServerLeavesStalwartAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unset func()
	}{
		{"no stalwart user", func() { panelMailStalwartUser = "jabali-no-such-user-2166" }},
		{"no stalwart binary", func() { panelMailStalwartBinary = filepath.Join(t.TempDir(), "absent") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ran := recordPanelMailReload(t)
			tc.unset()

			defaultPanelMailSelfSignReload(context.Background())
			want := []string{"systemctl reload nginx"}
			if got := ran(); strings.Join(got, " | ") != strings.Join(want, " | ") {
				t.Fatalf("ran %q, want %q", got, want)
			}
		})
	}
}
