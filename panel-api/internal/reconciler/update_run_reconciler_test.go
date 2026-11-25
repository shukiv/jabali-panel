package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeUpdRunRepo is a minimal UpdateHistoryRepository for the run reconciler.
type fakeUpdRunRepo struct {
	running   []models.UpdateHistory
	marked    map[string]string
	summaries map[string]string
}

func (f *fakeUpdRunRepo) List(context.Context, int) ([]models.UpdateHistory, error) {
	return nil, nil
}
func (f *fakeUpdRunRepo) ListRunning(context.Context) ([]models.UpdateHistory, error) {
	return f.running, nil
}
func (f *fakeUpdRunRepo) Insert(context.Context, *models.UpdateHistory) error { return nil }
func (f *fakeUpdRunRepo) MarkFinished(_ context.Context, id, status, summary, _ string) error {
	if f.marked == nil {
		f.marked = map[string]string{}
	}
	if f.summaries == nil {
		f.summaries = map[string]string{}
	}
	f.summaries[id] = summary
	f.marked[id] = status
	return nil
}

func newUpdRunReconciler(repo *fakeUpdRunRepo) *Reconciler {
	return newUpdRunReconcilerWithAgent(repo, &fakeAgent{})
}

func newUpdRunReconcilerWithAgent(repo *fakeUpdRunRepo, ag *fakeAgent) *Reconciler {
	return &Reconciler{
		updateRunHistory: repo,
		agent:            ag,
		log:              slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// A running row with no pollable unit can't be reconciled via systemd; once
// clearly stale (panel restart orphaned it) the reconciler must reap it so the
// tasks indicator stops showing a phantom "running" task.
func TestReconcileUpdateRuns_ReapsStaleUnitlessOrphan(t *testing.T) {
	repo := &fakeUpdRunRepo{running: []models.UpdateHistory{{
		ID: "old", Kind: models.UpdateKindJabali, Unit: "",
		Status: models.UpdateStatusRunning, StartedAt: time.Now().Add(-time.Hour),
	}}}
	newUpdRunReconciler(repo).reconcileUpdateRuns(context.Background())
	if repo.marked["old"] != models.UpdateStatusFailed {
		t.Fatalf("stale unit-less orphan must be reaped failed; got %v", repo.marked)
	}
}

// A recent unit-less row is left alone (don't false-fail a just-started run).
func TestReconcileUpdateRuns_KeepsFreshUnitlessRow(t *testing.T) {
	repo := &fakeUpdRunRepo{running: []models.UpdateHistory{{
		ID: "fresh", Kind: models.UpdateKindJabali, Unit: "",
		Status: models.UpdateStatusRunning, StartedAt: time.Now(),
	}}}
	newUpdRunReconciler(repo).reconcileUpdateRuns(context.Background())
	if _, ok := repo.marked["fresh"]; ok {
		t.Fatalf("fresh unit-less row must NOT be reaped yet")
	}
}

// GH #1486: a pollable-unit row that has spun past the hard cap is reaped even
// when its unit still reports "active" — a hung transient unit must not spin the
// Tasks indicator forever.
func TestReconcileUpdateRuns_HardBackstopReapsRunawayActiveRow(t *testing.T) {
	repo := &fakeUpdRunRepo{running: []models.UpdateHistory{{
		ID: "runaway", Kind: models.UpdateKindJabali, Unit: "jabali-update-oneshot.service",
		Status: models.UpdateStatusRunning, StartedAt: time.Now().Add(-3 * time.Hour),
	}}}
	ag := &fakeAgent{resultByMethod: map[string]json.RawMessage{
		"system.update_status": json.RawMessage(`{"status":"active"}`),
	}}
	newUpdRunReconcilerWithAgent(repo, ag).reconcileUpdateRuns(context.Background())
	if repo.marked["runaway"] != models.UpdateStatusFailed {
		t.Fatalf("a >2h running row must be reaped even if the unit still reports active; got %v", repo.marked)
	}
}

// GH #1486: a pollable-unit row whose agent status call ERRORS (agent down / too
// old to answer) is reaped once clearly stale — this was the hole that let update
// tasks spin "running" forever (only unit-less orphans were reaped before).
func TestReconcileUpdateRuns_ReapsStalePollableWhenAgentErrors(t *testing.T) {
	repo := &fakeUpdRunRepo{running: []models.UpdateHistory{{
		ID: "noagent", Kind: models.UpdateKindJabali, Unit: "jabali-update-oneshot.service",
		Status: models.UpdateStatusRunning, StartedAt: time.Now().Add(-40 * time.Minute),
	}}}
	ag := &fakeAgent{failMethod: "system.update_status"}
	newUpdRunReconcilerWithAgent(repo, ag).reconcileUpdateRuns(context.Background())
	if repo.marked["noagent"] != models.UpdateStatusFailed {
		t.Fatalf("a stale pollable row must be reaped when the agent can't report status; got %v", repo.marked)
	}
}

// A fresh pollable row whose status call errored ONCE is left alone — a transient
// agent blip must not false-fail a run that may still be going.
func TestReconcileUpdateRuns_KeepsFreshPollableWhenAgentErrors(t *testing.T) {
	repo := &fakeUpdRunRepo{running: []models.UpdateHistory{{
		ID: "young", Kind: models.UpdateKindJabali, Unit: "jabali-update-oneshot.service",
		Status: models.UpdateStatusRunning, StartedAt: time.Now().Add(-2 * time.Minute),
	}}}
	ag := &fakeAgent{failMethod: "system.update_status"}
	newUpdRunReconcilerWithAgent(repo, ag).reconcileUpdateRuns(context.Background())
	if _, ok := repo.marked["young"]; ok {
		t.Fatalf("a fresh pollable row must NOT be reaped after a single agent error")
	}
}

// The tail of an apt run that a broken grub-pc stopped (Debian 13, no GRUB
// install device in debconf).
const grubFailedAptLog = `Setting up grub-pc (2.12-9) ...
You must correct your GRUB install devices before proceeding:

  DEBIAN_FRONTEND=dialog dpkg --configure grub-pc
  dpkg --configure -a
dpkg: error processing package grub-pc (--configure):
 installed grub-pc package post-installation script subprocess returned error exit status 1
Processing triggers for man-db (2.13.1-1) ...
Errors were encountered while processing:
 grub-pc
E: Sub-process /usr/bin/dpkg returned an error code (1)`

func TestAptFailedPackages(t *testing.T) {
	if got := aptFailedPackages(grubFailedAptLog); len(got) != 1 || got[0] != "grub-pc" {
		t.Fatalf("got %v, want [grub-pc]", got)
	}
	two := "dpkg: error processing package libc-bin (--configure):\nErrors were encountered while processing:\n libc-bin\n nginx-common\nE: done"
	if got := aptFailedPackages(two); len(got) != 2 || got[0] != "libc-bin" || got[1] != "nginx-common" {
		t.Fatalf("got %v, want [libc-bin nginx-common]", got)
	}
	if got := aptFailedPackages("E: Could not get lock /var/lib/dpkg/lock-frontend"); got != nil {
		t.Fatalf("no package named, got %v", got)
	}
	if got := aptFailedPackages("Errors were encountered while processing:\n $(rm -rf /)\n"); got != nil {
		t.Fatalf("a line that is not a package name is skipped, got %v", got)
	}
}

// A failed system-packages run says which package dpkg could not set up.
func TestReconcileUpdateRuns_AptFailureNamesThePackage(t *testing.T) {
	repo := &fakeUpdRunRepo{running: []models.UpdateHistory{{
		ID: "apt1", Kind: models.UpdateKindApt, Unit: "jabali-apt-oneshot.service",
		Status: models.UpdateStatusRunning, StartedAt: time.Now().Add(-5 * time.Minute),
	}}}
	reply, _ := json.Marshal(map[string]any{"status": "failed", "exit_code": 100, "log_tail": grubFailedAptLog})
	ag := &fakeAgent{resultByMethod: map[string]json.RawMessage{"system.apt_status": reply}}
	newUpdRunReconcilerWithAgent(repo, ag).reconcileUpdateRuns(context.Background())
	if repo.marked["apt1"] != models.UpdateStatusFailed || repo.summaries["apt1"] != "failed: dpkg could not set up grub-pc" {
		t.Fatalf("marked %v summary %q", repo.marked, repo.summaries["apt1"])
	}
}
