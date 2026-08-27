package reconciler

// GH #1237 — after a WordPress core update / auto-update the Applications UI kept
// showing the old version, because the reconciler probe only stat'd version.php
// for existence and never re-read the version. These tests cover the refresh:
// the probe now reports the parsed version and the reconciler writes it on drift.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeWPProbeAgent answers wordpress.version_probe with a canned payload and
// records what it was called with.
type fakeWPProbeAgent struct {
	resp      []byte
	err       error
	gotMethod string
	gotDir    string
}

func (f *fakeWPProbeAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	f.gotMethod = method
	if m, ok := params.(map[string]any); ok {
		if d, ok := m["dir"].(string); ok {
			f.gotDir = d
		}
	}
	return f.resp, f.err
}

// fakeWPDomains satisfies DomainRepository via embedding; only FindByID is used.
type fakeWPDomains struct {
	repository.DomainRepository
	docRoot string
}

func (f *fakeWPDomains) FindByID(_ context.Context, _ string) (*models.Domain, error) {
	return &models.Domain{DocRoot: f.docRoot}, nil
}

func probeReconciler(t *testing.T, ready []models.WordPressInstall, agent *fakeWPProbeAgent, docRoot string) (*Reconciler, *mockWordPressInstallRepo) {
	t.Helper()
	mock := &mockWordPressInstallRepo{ready: ready}
	r := &Reconciler{
		wordPressInstalls: mock,
		domains:           &fakeWPDomains{docRoot: docRoot},
		agent:             agent,
		cfg: &config.Config{
			WordPress: config.WordPressConfig{
				InstallTimeout: 10 * time.Minute,
				CloneTimeout:   30 * time.Minute,
				DeleteTimeout:  5 * time.Minute,
				ProbeBatch:     100,
			},
		},
		log: newTestLogger(t),
	}
	return r, mock
}

func wpProbeResp(t *testing.T, exists bool, version string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"exists": exists, "version": version})
	if err != nil {
		t.Fatalf("marshal probe resp: %v", err)
	}
	return b
}

func TestProbe_RefreshesVersionOnDrift(t *testing.T) {
	ready := []models.WordPressInstall{
		{ID: "w1", Status: "ready", AppType: "wordpress", DomainID: "d1", Version: stringPtr("6.4.10"), UpdatedAt: time.Now()},
	}
	agent := &fakeWPProbeAgent{resp: wpProbeResp(t, true, "6.7.1")}
	r, mock := probeReconciler(t, ready, agent, "/home/u/public_html")

	r.reconcileWordPressInstalls(context.Background())

	if agent.gotMethod != "wordpress.version_probe" {
		t.Fatalf("expected wordpress.version_probe, got %q", agent.gotMethod)
	}
	if agent.gotDir != "/home/u/public_html" {
		t.Fatalf("probe dir = %q, want the docroot", agent.gotDir)
	}
	if len(mock.updateCalls) != 1 {
		t.Fatalf("expected 1 update (version refresh), got %d", len(mock.updateCalls))
	}
	u := mock.updateCalls[0]
	if u.id != "w1" || u.status != "ready" || u.version == nil || *u.version != "6.7.1" {
		t.Fatalf("bad refresh update: %+v (version=%v)", u, u.version)
	}
}

func TestProbe_NoUpdateWhenVersionUnchanged(t *testing.T) {
	ready := []models.WordPressInstall{
		{ID: "w1", Status: "ready", AppType: "wordpress", DomainID: "d1", Version: stringPtr("6.7.1"), UpdatedAt: time.Now()},
	}
	agent := &fakeWPProbeAgent{resp: wpProbeResp(t, true, "6.7.1")}
	r, mock := probeReconciler(t, ready, agent, "/home/u/public_html")

	r.reconcileWordPressInstalls(context.Background())

	if len(mock.updateCalls) != 0 {
		t.Fatalf("no drift → expected 0 updates, got %d", len(mock.updateCalls))
	}
}

func TestProbe_BackfillsNullVersion(t *testing.T) {
	ready := []models.WordPressInstall{
		{ID: "w1", Status: "ready", AppType: "wordpress", DomainID: "d1", Version: nil, UpdatedAt: time.Now()},
	}
	agent := &fakeWPProbeAgent{resp: wpProbeResp(t, true, "6.7.1")}
	r, mock := probeReconciler(t, ready, agent, "/home/u/public_html")

	r.reconcileWordPressInstalls(context.Background())

	if len(mock.updateCalls) != 1 || mock.updateCalls[0].version == nil || *mock.updateCalls[0].version != "6.7.1" {
		t.Fatalf("expected NULL version to be backfilled to 6.7.1, got %+v", mock.updateCalls)
	}
}

func TestProbe_MissingVersionPhpMarksFailed(t *testing.T) {
	ready := []models.WordPressInstall{
		{ID: "w1", Status: "ready", AppType: "wordpress", DomainID: "d1", Version: stringPtr("6.7.1"), UpdatedAt: time.Now()},
	}
	agent := &fakeWPProbeAgent{resp: wpProbeResp(t, false, "")}
	r, mock := probeReconciler(t, ready, agent, "/home/u/public_html")

	r.reconcileWordPressInstalls(context.Background())

	if len(mock.updateCalls) != 1 || mock.updateCalls[0].status != "failed" {
		t.Fatalf("missing version.php must flip to failed, got %+v", mock.updateCalls)
	}
}
