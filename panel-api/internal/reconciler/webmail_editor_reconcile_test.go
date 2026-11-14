package reconciler

import (
	"context"
	"testing"
)

// The agent gets the editor URL from server settings; an unchanged URL is
// not sent again (the ledger gates it).
func TestReconcileWebmailEditor_SendsSettingsValue(t *testing.T) {
	r, ag, _, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	s, _ := r.serverSettings.Get(context.Background())
	s.WebmailOfficeEditorURL = "https://office.example.com"

	run := func() {
		ctx, rr := ensureRun(context.Background(), RunNormal)
		r.reconcileWebmailEditor(ctx)
		rr.finish()
	}
	run()
	call, ok := findAgentCall(ag, "webmail.editor.apply")
	if !ok {
		t.Fatal("webmail.editor.apply was not called")
	}
	p := call.params.(map[string]any)
	if p["wopi_client_url"] != "https://office.example.com" {
		t.Fatalf("wopi_client_url = %v", p["wopi_client_url"])
	}

	run()
	if n := countAgentCalls(ag, "webmail.editor.apply"); n != 1 {
		t.Fatalf("an unchanged editor URL was sent again (%d calls)", n)
	}
}

// Without an agent the phase is skipped.
func TestReconcileWebmailEditor_SkippedWithoutAgent(t *testing.T) {
	r, ag, _, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	s, _ := r.serverSettings.Get(context.Background())
	s.WebmailOfficeEditorURL = "https://office.example.com"
	r.agent = nil

	ctx, rr := ensureRun(context.Background(), RunNormal)
	r.reconcileWebmailEditor(ctx)
	rr.finish()

	if n := countAgentCalls(ag, "webmail.editor.apply"); n != 0 {
		t.Fatalf("webmail.editor.apply was called without an agent (%d)", n)
	}
}
