package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemctlCapture records every systemctl argv the fake receives, so
// tests can assert the exact sequence of enable/reload calls.
type systemctlCapture struct {
	calls [][]string
	// respond replaces the default 200/nil answer. First match wins.
	respond func(args []string) ([]byte, error)
}

// wireSystemctl swaps runSystemctl for a test fake + restores it on cleanup.
// Returns the capture so tests can read calls after.
func wireSystemctl(t *testing.T, cap *systemctlCapture) {
	t.Helper()
	orig := runSystemctl
	runSystemctl = func(_ context.Context, args ...string) ([]byte, error) {
		cap.calls = append(cap.calls, append([]string(nil), args...))
		if cap.respond != nil {
			return cap.respond(args)
		}
		return nil, nil
	}
	t.Cleanup(func() { runSystemctl = orig })
}

// wireDKIMDir points dkimKeyDirFunc at a test temp dir + restores it.
func wireDKIMDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := dkimKeyDirFunc
	dkimKeyDirFunc = func() string { return dir }
	t.Cleanup(func() { dkimKeyDirFunc = orig })
	return dir
}

// --- domain.email_enable --------------------------------------------

// enableJMAPRoutes is the full JMAP fake-server route map for a
// successful domain.email_enable; reg holds the DKIM signatures (pass
// nil for an empty registry).
func enableJMAPRoutes(existingDomainID string, createdDomainID string, reg *fakeDkim2Registry) map[string]jmapHandler {
	queryResult := jmapQueryResult{Total: 0}
	if existingDomainID != "" {
		queryResult.IDs = []string{existingDomainID}
		queryResult.Total = 1
	}
	createResult := jmapSetResult{
		Created: map[string]json.RawMessage{
			"#d1": json.RawMessage(fmt.Sprintf(`{"id":%q}`, createdDomainID)),
		},
	}
	if reg == nil {
		reg = &fakeDkim2Registry{}
	}
	routes := reg.routes()
	routes["x:Domain/query"] = jmapHandlerReturning(queryResult)
	routes["x:Domain/set"] = jmapHandlerReturning(createResult)
	return routes
}

func TestDomainEmailEnable_FreshDomain_CreatesDomainAndDkimSignature(t *testing.T) {
	dir := wireDKIMDir(t)
	var sysctl systemctlCapture
	wireSystemctl(t, &sysctl)
	reg := &fakeDkim2Registry{}
	srv := newJMAPServer(t, enableJMAPRoutes("", "dom-new-42", reg))
	defer srv.Close()
	wireJMAP(t, srv)

	got, err := domainEmailEnableHandler(context.Background(), json.RawMessage(
		`{"domain_id":"01J","domain_name":"example.com"}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp, ok := got.(domainEmailEnableResponse)
	if !ok {
		t.Fatalf("type: got %T, want domainEmailEnableResponse", got)
	}
	if !resp.Ok {
		t.Error("ok: false, want true")
	}
	if resp.DKIMSelector != "jabali" {
		t.Errorf("selector: got %q, want %q", resp.DKIMSelector, "jabali")
	}
	if !strings.HasPrefix(resp.DKIMPublicKey, "v=DKIM1; k=ed25519; p=") {
		t.Errorf("public key wrong shape: %q", resp.DKIMPublicKey)
	}
	if resp.DKIMRSASelector != "jabalirsa" || !strings.HasPrefix(resp.DKIMRSAPublicKey, "v=DKIM1; k=rsa; p=") {
		t.Errorf("RSA selector/key wrong: %q %q", resp.DKIMRSASelector, resp.DKIMRSAPublicKey)
	}
	// Both signatures land on the new domain (GH #2475).
	if got := sigSummary(reg, "dom-new-42"); got != "Dkim1Ed25519Sha256/jabali Dkim1RsaSha256/jabalirsa" {
		t.Errorf("signatures on dom-new-42 = %q", got)
	}

	// Keyfile should exist, mode 0600.
	keyPath := filepath.Join(dir, "example.com.key")
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat keyfile: %v", err)
	}
	if mode := fi.Mode() & os.ModePerm; mode != 0o600 {
		t.Errorf("mode: got %o, want 0600", mode)
	}

	// Three systemctl calls: enable stalwart, enable webmail, reload stalwart.
	if len(sysctl.calls) != 3 {
		t.Fatalf("systemctl calls: got %d, want 3 (%v)", len(sysctl.calls), sysctl.calls)
	}
	wantArgs := [][]string{
		{"enable", "--now", "jabali-stalwart.service"},
		{"enable", "--now", "jabali-webmail.service"},
		{"reload", "jabali-stalwart.service"},
	}
	for i := range wantArgs {
		if strings.Join(sysctl.calls[i], " ") != strings.Join(wantArgs[i], " ") {
			t.Errorf("call %d: got %v, want %v", i, sysctl.calls[i], wantArgs[i])
		}
	}
}

func TestDomainEmailEnable_AlreadyRegistered_SkipsDomainAndSignatureCreates(t *testing.T) {
	// Domain already exists in Stalwart's registry with both signatures
	// (email_enable re-run, or the reconciler re-converging). The handler
	// must create neither the Domain nor a signature: the fake's route map
	// omits x:Domain/set, so a stray Domain create would 400 and fail.
	wireDKIMDir(t)
	var sysctl systemctlCapture
	wireSystemctl(t, &sysctl)
	reg := &fakeDkim2Registry{sigs: map[string]map[string]any{
		"s1": {"id": "s1", "@type": "Dkim1Ed25519Sha256", "domainId": "dom-existing", "selector": "jabali"},
		"s2": {"id": "s2", "@type": "Dkim1RsaSha256", "domainId": "dom-existing", "selector": "jabalirsa"},
	}}
	routes := reg.routes()
	routes["x:Domain/query"] = jmapHandlerReturning(jmapQueryResult{IDs: []string{"dom-existing"}, Total: 1})
	srv := newJMAPServer(t, routes)
	defer srv.Close()
	wireJMAP(t, srv)

	got, err := domainEmailEnableHandler(context.Background(), json.RawMessage(
		`{"domain_id":"01J","domain_name":"example.com"}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	resp := got.(domainEmailEnableResponse)
	if !resp.Ok {
		t.Error("ok: false, want true")
	}
	if reg.nextID != 0 || len(reg.sigs) != 2 {
		t.Errorf("signatures created on a domain that had both: %v", reg.sigs)
	}
}

// GH #2475: a Domain created without signatures (the account registry
// sync creates one) used to be skipped entirely, leaving its mail unsigned.
// email_enable now gives it both.
func TestDomainEmailEnable_RegisteredWithoutSignatures_GetsBoth(t *testing.T) {
	wireDKIMDir(t)
	var sysctl systemctlCapture
	wireSystemctl(t, &sysctl)
	reg := &fakeDkim2Registry{}
	routes := reg.routes()
	routes["x:Domain/query"] = jmapHandlerReturning(jmapQueryResult{IDs: []string{"dom-bare"}, Total: 1})
	srv := newJMAPServer(t, routes)
	defer srv.Close()
	wireJMAP(t, srv)

	got, err := domainEmailEnableHandler(context.Background(), json.RawMessage(
		`{"domain_id":"01J","domain_name":"example.com"}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := sigSummary(reg, "dom-bare"); got != "Dkim1Ed25519Sha256/jabali Dkim1RsaSha256/jabalirsa" {
		t.Fatalf("signatures on dom-bare = %q, want both", got)
	}
	if resp := got.(domainEmailEnableResponse); !strings.HasPrefix(resp.DKIMRSAPublicKey, "v=DKIM1; k=rsa; p=") {
		t.Errorf("RSA public key = %q", resp.DKIMRSAPublicKey)
	}
}

func TestEnsureDKIMKey_ReusesExistingKey(t *testing.T) {
	// Stability test against the pure helper — handler can't reach its
	// response path while task-13 refusal is in force, so we test the
	// DKIM re-derivation guarantee at the helper level.
	dir := wireDKIMDir(t)
	keyPath := filepath.Join(dir, "example.com.key")

	firstTXT, err := ensureDKIMKey(keyPath)
	if err != nil {
		t.Fatalf("first ensureDKIMKey: %v", err)
	}
	if !strings.HasPrefix(firstTXT, "v=DKIM1; k=ed25519; p=") {
		t.Errorf("public key wrong shape: %q", firstTXT)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read keyfile: %v", err)
	}

	secondTXT, err := ensureDKIMKey(keyPath)
	if err != nil {
		t.Fatalf("second ensureDKIMKey: %v", err)
	}
	if firstTXT != secondTXT {
		t.Errorf("TXT drift between first + second derivation\n first: %s\nsecond: %s", firstTXT, secondTXT)
	}

	// Keyfile bytes must be byte-identical (no rewrite on re-derivation).
	keyAfter, _ := os.ReadFile(keyPath)
	if string(keyBefore) != string(keyAfter) {
		t.Error("keyfile rewritten on second call — DKIM key must be stable")
	}
}

func TestDomainEmailEnable_BadDomainNameRejected(t *testing.T) {
	wireDKIMDir(t)
	var sysctl systemctlCapture
	wireSystemctl(t, &sysctl)

	cases := []string{
		`{"domain_id":"01J","domain_name":"exa mple.com"}`, // space
		`{"domain_id":"01J","domain_name":"ex;.com"}`,      // semicolon
		`{"domain_id":"01J","domain_name":"../etc"}`,       // path traversal via slash
		`{"domain_id":"01J","domain_name":".bad"}`,         // leading dot
		`{"domain_id":"01J","domain_name":"-bad.com"}`,     // leading hyphen
		`{"domain_id":"01J","domain_name":""}`,             // empty
	}
	for _, params := range cases {
		_, err := domainEmailEnableHandler(context.Background(), json.RawMessage(params))
		requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
	}

	// No systemctl calls should have fired for any rejected input.
	if len(sysctl.calls) != 0 {
		t.Errorf("systemctl should not have been invoked for rejected inputs, got %d calls", len(sysctl.calls))
	}
}

func TestDomainEmailEnable_SystemctlEnableFailure(t *testing.T) {
	// systemctl enable fails BEFORE any JMAP call — no JMAP fake needed
	// because the handler errors out before reaching the Domain/query.
	wireDKIMDir(t)
	sysctl := &systemctlCapture{
		respond: func(args []string) ([]byte, error) {
			if len(args) >= 3 && args[0] == "enable" && args[2] == "jabali-stalwart.service" {
				return []byte("some systemctl output"), errors.New("unit failed")
			}
			return nil, nil
		},
	}
	wireSystemctl(t, sysctl)

	_, err := domainEmailEnableHandler(context.Background(), json.RawMessage(
		`{"domain_id":"01J","domain_name":"example.com"}`))
	requireAgentErrorCode(t, err, agentwire.CodeInternal)
}

func TestDomainEmailEnable_ReloadNotSupportedIsNotFatal(t *testing.T) {
	// If Stalwart's unit doesn't declare ExecReload, systemctl reload
	// returns a "reload is not applicable" error. The handler swallows
	// it (per isReloadNotSupportedErr) and proceeds to the JMAP
	// Domain + DkimSignature creates. Re-verify that the handler
	// still acks ok when only the reload fails this way.
	wireDKIMDir(t)
	sysctl := &systemctlCapture{
		respond: func(args []string) ([]byte, error) {
			if len(args) >= 1 && args[0] == "reload" {
				return []byte("Failed to reload jabali-stalwart.service: Job type reload is not applicable for unit jabali-stalwart.service."),
					errors.New("exit status 1")
			}
			return nil, nil
		},
	}
	wireSystemctl(t, sysctl)
	srv := newJMAPServer(t, enableJMAPRoutes("", "dom-reload-42", nil))
	defer srv.Close()
	wireJMAP(t, srv)

	_, err := domainEmailEnableHandler(context.Background(), json.RawMessage(
		`{"domain_id":"01J","domain_name":"example.com"}`))
	if err != nil {
		t.Fatalf("reload-not-applicable should not fail: %v", err)
	}
}

// --- domain.email_disable --------------------------------------------
//
// Updated v0.16 flow (post-live-bug-fix): agent does NOT destroy the
// Stalwart registry Domain (Stalwart refuses with objectIsLinked when
// mailboxes exist) and does NOT remove the DKIM private key (ADR-0043
// preserves it across enable/disable cycles). The handler is now just
// a Stalwart reload — the panel DB is authoritative; SqlDirectory
// blocks new auths once email_enabled=0.

func TestDomainEmailDisable_ReloadOnly(t *testing.T) {
	// Seed a keyfile so we can assert it SURVIVES the disable — this
	// is the ADR-0043 contract (DKIM key preserved for re-enable). If
	// disable removed it, re-enable would re-roll and invalidate every
	// cached DKIM signature downstream.
	dir := wireDKIMDir(t)
	keyPath := filepath.Join(dir, "example.com.key")
	if err := os.WriteFile(keyPath, []byte("seed"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var sysctl systemctlCapture
	wireSystemctl(t, &sysctl)

	_, err := domainEmailDisableHandler(context.Background(), json.RawMessage(
		`{"domain_id":"01J","domain_name":"example.com"}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// Key must still be on disk.
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("keyfile should survive disable, stat err: %v", err)
	}

	// Exactly one systemctl call: reload. No JMAP required; no fake
	// server was wired, so any call to Stalwart would blow up — the
	// absence of blow-up is part of the assertion.
	if len(sysctl.calls) != 1 {
		t.Fatalf("systemctl calls: got %d, want 1 (%v)", len(sysctl.calls), sysctl.calls)
	}
	if strings.Join(sysctl.calls[0], " ") != "reload jabali-stalwart.service" {
		t.Errorf("reload argv: got %v, want [reload jabali-stalwart.service]", sysctl.calls[0])
	}
}

func TestDomainEmailDisable_NoRegistryInteraction(t *testing.T) {
	// Regression guard: any JMAP call from disable indicates the
	// destroy path is back. Wire a server that 400s on every method
	// so an unexpected call is loud.
	wireDKIMDir(t)
	var sysctl systemctlCapture
	wireSystemctl(t, &sysctl)
	srv := newJMAPServer(t, map[string]jmapHandler{})
	defer srv.Close()
	wireJMAP(t, srv)

	_, err := domainEmailDisableHandler(context.Background(), json.RawMessage(
		`{"domain_id":"01J","domain_name":"example.com"}`))
	if err != nil {
		t.Fatalf("disable must not touch Stalwart registry: %v", err)
	}
}

func TestDomainEmailDisable_BadParams(t *testing.T) {
	_, err := domainEmailDisableHandler(context.Background(), json.RawMessage(`{}`))
	requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
}
