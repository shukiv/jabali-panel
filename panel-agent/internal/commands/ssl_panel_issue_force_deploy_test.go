package commands

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/certbot"
)

// JAB-390: the mail-hostname switchover re-issues into a lineage that may
// already hold a valid certificate — switching back to mail.<hostname>, or
// retrying a switchover whose deploy was interrupted. certbot then keeps the
// existing cert (Skipped) and ssl.panel.issue skipped the deploy hook, so
// panel-mail.crt, the lineage record and Stalwart stayed on the previous
// name. force_deploy runs the hook anyway; it is refused for kind=hostname,
// whose hook restarts jabali-panel (the caller) and would deadlock the
// panel-cert reconciler.

type fakePanelIssuer struct {
	result *certbot.Result
	calls  int
}

func (f *fakePanelIssuer) Issue(domain, webroot, email string, staging bool, extra []string) (*certbot.Result, error) {
	f.calls++
	return f.result, nil
}

func wirePanelIssue(t *testing.T, skipped bool) (*fakePanelIssuer, *[]string) {
	t.Helper()
	issuer := &fakePanelIssuer{result: &certbot.Result{
		Skipped:   skipped,
		IssuedAt:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
	}}
	var hooks []string
	origRunner, origHook, origRoot := newPanelIssueRunner, runDeployHookFn, panelACMEWebroot
	newPanelIssueRunner = func() panelCertIssuer { return issuer }
	runDeployHookFn = func(_ context.Context, hostname, kind string) error {
		hooks = append(hooks, hostname+"/"+kind)
		return nil
	}
	panelACMEWebroot = t.TempDir()
	t.Cleanup(func() { newPanelIssueRunner, runDeployHookFn, panelACMEWebroot = origRunner, origHook, origRoot })
	return issuer, &hooks
}

func panelIssue(t *testing.T, p sslPanelIssueParams) error {
	t.Helper()
	raw, _ := json.Marshal(p)
	_, err := sslPanelIssueHandler(context.Background(), raw)
	return err
}

func TestSSLPanelIssue_SkippedCertIsNotRedeployed(t *testing.T) {
	_, hooks := wirePanelIssue(t, true)
	if err := panelIssue(t, sslPanelIssueParams{Hostname: "mail.panel.example.com", Email: "admin@example.com", Kind: "mail"}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(*hooks) != 0 {
		t.Errorf("a kept certificate must not re-run the deploy hook without force_deploy, ran %v", *hooks)
	}
}

func TestSSLPanelIssue_ForceDeployRedeploysKeptMailCert(t *testing.T) {
	_, hooks := wirePanelIssue(t, true)
	if err := panelIssue(t, sslPanelIssueParams{Hostname: "mail.panel.example.com", Email: "admin@example.com", Kind: "mail", ForceDeploy: true}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(*hooks) != 1 || (*hooks)[0] != "mail.panel.example.com/mail" {
		t.Errorf("force_deploy must run the mail deploy hook once for the lineage, ran %v", *hooks)
	}
}

func TestSSLPanelIssue_ForceDeployRefusedForHostnameKind(t *testing.T) {
	for _, kind := range []string{"", "hostname"} {
		issuer, hooks := wirePanelIssue(t, true)
		err := panelIssue(t, sslPanelIssueParams{Hostname: "panel.example.com", Email: "admin@example.com", Kind: kind, ForceDeploy: true})
		assertInvalidArgument(t, err, "force_deploy with kind "+kind)
		if issuer.calls != 0 || len(*hooks) != 0 {
			t.Errorf("kind %q: a refused request must not run certbot or the hook (certbot=%d hooks=%v)", kind, issuer.calls, *hooks)
		}
	}
}
