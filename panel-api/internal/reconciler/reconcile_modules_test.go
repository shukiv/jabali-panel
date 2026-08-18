package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

var errStub = errors.New("stub agent error")

// moduleStubAgent returns a canned system.module.status per key and records
// install calls. status[key] = {"installed":bool,"active":bool}.
type moduleStubAgent struct {
	status       map[string]map[string]any
	installCalls int
	configStatus map[string]any // canned ftp.config_status response
	configErr    error          // if set, ftp.config_status returns this error

	mu      sync.Mutex
	methods []string // every method seen (guarded; detached dispatch races the test)
}

func (m *moduleStubAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	m.mu.Lock()
	m.methods = append(m.methods, method)
	m.mu.Unlock()
	key := ""
	if mp, ok := params.(map[string]any); ok {
		key, _ = mp["key"].(string)
	}
	switch method {
	case "system.module.status":
		st := m.status[key]
		if st == nil {
			st = map[string]any{"installed": false, "active": false}
		}
		return json.Marshal(st)
	case "system.module.install":
		m.installCalls++
		return json.Marshal(m.status[key])
	case "ftp.config_status":
		if m.configErr != nil {
			return nil, m.configErr
		}
		return json.Marshal(m.configStatus)
	default:
		return nil, nil
	}
}

func (m *moduleStubAgent) methodCount(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, x := range m.methods {
		if x == name {
			n++
		}
	}
	return n
}

var _ agent.AgentInterface = (*moduleStubAgent)(nil)

func discardReconciler(ag agent.AgentInterface) *Reconciler {
	return &Reconciler{agent: ag, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func up() map[string]any   { return map[string]any{"installed": true, "active": true} }
func down() map[string]any { return map[string]any{"installed": false, "active": false} }
func inactive() map[string]any {
	return map[string]any{"installed": true, "active": false}
}

// convergeModule (no dependency) must dispatch for the not-fully-up states and
// short-circuit when installed+active. Asserted on the synchronous backoff
// record (set iff an install was decided), avoiding a race with the detached
// install goroutine.
func TestConvergeModuleDecision(t *testing.T) {
	cases := []struct {
		name         string
		status       map[string]any
		wantDispatch bool
	}{
		{"installed+active converged", up(), false},
		{"missing", down(), true},
		{"installed but inactive (the bug)", inactive(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := discardReconciler(&moduleStubAgent{status: map[string]map[string]any{"dns": tc.status}})
			r.convergeModule(context.Background(), "dns", "")

			r.moduleInstallMu.Lock()
			_, dispatched := r.moduleInstallAttempt["dns"]
			r.moduleInstallMu.Unlock()
			if dispatched != tc.wantDispatch {
				t.Errorf("dispatch = %v, want %v", dispatched, tc.wantDispatch)
			}
		})
	}
}

// A module with an unmet dependency must NOT dispatch and must NOT burn its
// backoff (so it installs promptly once the dependency converges). Once the
// dependency is up, the module dispatches normally.
func TestConvergeModuleDependency(t *testing.T) {
	// mail needs dns; dns is down → mail must not dispatch, no backoff recorded.
	r := discardReconciler(&moduleStubAgent{status: map[string]map[string]any{
		"dns":  down(),
		"mail": down(),
	}})
	r.convergeModule(context.Background(), "mail", "dns")
	r.moduleInstallMu.Lock()
	_, recorded := r.moduleInstallAttempt["mail"]
	r.moduleInstallMu.Unlock()
	if recorded {
		t.Error("mail with unmet dns dependency must not record a backoff attempt")
	}

	// dns up, mail down → mail dispatches.
	r2 := discardReconciler(&moduleStubAgent{status: map[string]map[string]any{
		"dns":  up(),
		"mail": down(),
	}})
	r2.convergeModule(context.Background(), "mail", "dns")
	r2.moduleInstallMu.Lock()
	_, dispatched := r2.moduleInstallAttempt["mail"]
	r2.moduleInstallMu.Unlock()
	if !dispatched {
		t.Error("mail with satisfied dns dependency (mail down) must dispatch")
	}
}

// JAB-259: a DISABLED ftp module must be converged fail-closed — convergeFtpDisabled
// dispatches ftp.disable and records its own "ftp-disable" backoff so a stubborn
// failure retries without hot-looping every tick.
func TestConvergeFtpDisabled_DispatchesFtpDisableWithBackoff(t *testing.T) {
	ag := &moduleStubAgent{status: map[string]map[string]any{}}
	r := discardReconciler(ag)

	r.convergeFtpDisabled(context.Background())

	// Synchronous decision record (house pattern — avoids the detached goroutine).
	r.moduleInstallMu.Lock()
	_, recorded := r.moduleInstallAttempt["ftp-disable"]
	r.moduleInstallMu.Unlock()
	if !recorded {
		t.Fatal("convergeFtpDisabled must record an ftp-disable backoff attempt")
	}
	// A second immediate pass is gated by that backoff.
	if r.moduleInstallDue("ftp-disable") {
		t.Fatal("second immediate convergeFtpDisabled must be gated by backoff")
	}

	// The dispatched verb is ftp.disable (the fail-closed shutoff), not the
	// generic system.module.disable. Poll — the dispatch is detached.
	deadline := time.Now().Add(2 * time.Second)
	for ag.methodCount("ftp.disable") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ag.methodCount("ftp.disable") != 1 {
		t.Fatalf("want exactly one ftp.disable dispatch, got %d", ag.methodCount("ftp.disable"))
	}
	if ag.methodCount("system.module.disable") != 0 {
		t.Fatal("must NOT use the generic fail-soft system.module.disable for ftp")
	}
}

// JAB-260: convergeFtpConfig re-renders (system.module.install) exactly when the
// LIVE TLS enforcement disagrees with the desired ftp_allow_plaintext — an
// equality check catching BOTH the tighten-drift (want TLS, config plaintext)
// and the loosen-drift (want plaintext, config still TLS). Converged states and
// a verb error (old agent) never re-render.
func TestConvergeFtpConfig_ReAppliesOnlyOnDrift(t *testing.T) {
	cases := []struct {
		name           string
		allowPlaintext bool // desired
		enforced       bool // live config
		active         bool // vsftpd active? (an inactive/masked daemon serves nothing)
		configErr      error
		wantReApply    bool
	}{
		{"want-tls,enforced,active → converged", false, true, true, nil, false},
		{"want-tls,plaintext,active → tighten drift", false, false, true, nil, true},
		{"want-plaintext,enforced,active → loosen drift", true, true, true, nil, true},
		{"want-plaintext,plaintext,active → converged", true, false, true, nil, false},
		{"want-tls,plaintext,INACTIVE → skip (masked=fail-closed already)", false, false, false, nil, false},
		{"verb error (old agent) → skip", false, false, true, errStub, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ftpStatus := down()
			if tc.active {
				ftpStatus = up()
			}
			ag := &moduleStubAgent{
				status:       map[string]map[string]any{"ftp": ftpStatus},
				configStatus: map[string]any{"exists": true, "ssl_enforced": tc.enforced},
				configErr:    tc.configErr,
			}
			r := discardReconciler(ag)
			r.convergeFtpConfig(context.Background(), &models.ServerSettings{FTPAllowPlaintext: tc.allowPlaintext})

			// Detached goroutine — poll for the re-apply decision, then settle.
			deadline := time.Now().Add(1 * time.Second)
			for tc.wantReApply && ag.methodCount("system.module.install") == 0 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if !tc.wantReApply {
				time.Sleep(150 * time.Millisecond) // give a wrong dispatch time to appear
			}
			got := ag.methodCount("system.module.install")
			if tc.wantReApply && got != 1 {
				t.Fatalf("want re-apply, got %d install calls", got)
			}
			if !tc.wantReApply && got != 0 {
				t.Fatalf("want NO re-apply, got %d install calls", got)
			}
		})
	}
}

// JAB-260: an active daemon with no config file is odd but must be treated as
// converged (convergeModule owns the install), NOT re-applied here.
func TestConvergeFtpConfig_MissingConfIsConverged(t *testing.T) {
	ag := &moduleStubAgent{
		status:       map[string]map[string]any{"ftp": up()}, // active, so we reach the config check
		configStatus: map[string]any{"exists": false, "ssl_enforced": false},
	}
	r := discardReconciler(ag)
	r.convergeFtpConfig(context.Background(), &models.ServerSettings{FTPAllowPlaintext: false})
	time.Sleep(150 * time.Millisecond)
	if ag.methodCount("system.module.install") != 0 {
		t.Fatal("missing conf must be treated as converged, not re-applied")
	}
}

// JAB-259: a stale disable must stand down if FTP was re-enabled after the pass
// was scheduled. convergeFtpDisabled re-reads ftp_enabled inside the goroutine
// and must NOT dispatch ftp.disable when it now reads enabled — masking a
// just-re-enabled vsftpd would ping-pong against install-convergence.
func TestConvergeFtpDisabled_SkipsWhenReEnabled(t *testing.T) {
	ag := &moduleStubAgent{status: map[string]map[string]any{}}
	r := discardReconciler(ag)
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{FTPEnabled: true}}

	r.convergeFtpDisabled(context.Background())

	// Let the detached goroutine run its re-read + decision; it must not dispatch.
	time.Sleep(200 * time.Millisecond)
	if ag.methodCount("ftp.disable") != 0 {
		t.Fatal("ftp.disable dispatched despite FTP being re-enabled (stale-disable ping-pong)")
	}
}

// The backoff gate must reject a second install within the retry window and
// allow it again afterward.
func TestModuleInstallBackoff(t *testing.T) {
	r := &Reconciler{}
	if !r.moduleInstallDue("dns") {
		t.Fatal("first attempt should be due")
	}
	if r.moduleInstallDue("dns") {
		t.Fatal("second immediate attempt should be gated by backoff")
	}
	if !r.moduleInstallDue("quota") {
		t.Fatal("a different module must not be gated by dns's attempt")
	}
	r.moduleInstallMu.Lock()
	r.moduleInstallAttempt["dns"] = time.Now().Add(-moduleInstallRetryInterval - time.Minute)
	r.moduleInstallMu.Unlock()
	if !r.moduleInstallDue("dns") {
		t.Fatal("attempt after the retry window should be due again")
	}
}

// Locks the convergence registry wiring: every optional module install.sh
// supports is present, mail depends on dns, and the rest have no dependency.
func TestConvergedModulesWiring(t *testing.T) {
	deps := map[string]string{}
	for _, m := range convergedModules {
		deps[m.key] = m.dependsOn
	}
	for _, want := range []string{"dns", "mail", "quota", "security"} {
		if _, ok := deps[want]; !ok {
			t.Errorf("convergedModules missing %q", want)
		}
	}
	if deps["mail"] != "dns" {
		t.Errorf("mail dependsOn = %q, want dns", deps["mail"])
	}
	for _, indep := range []string{"dns", "quota", "security"} {
		if deps[indep] != "" {
			t.Errorf("%s dependsOn = %q, want none", indep, deps[indep])
		}
	}
}
