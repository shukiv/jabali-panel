package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/gitdeploy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type gitQueueWebhooks struct {
	repository.DomainGitWebhookRepository
	rows map[string]*models.DomainGitWebhook
}

func (f *gitQueueWebhooks) Queue(_ context.Context, domainID string, at time.Time) error {
	if r, ok := f.rows[domainID]; ok && r.QueuedAt == nil {
		r.QueuedAt = &at
	}
	return nil
}

func (f *gitQueueWebhooks) ListQueued(_ context.Context, _ int) ([]models.DomainGitWebhook, error) {
	var out []models.DomainGitWebhook
	for _, r := range f.rows {
		if r.QueuedAt != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *gitQueueWebhooks) Unqueue(_ context.Context, domainID string) (bool, error) {
	r, ok := f.rows[domainID]
	if !ok || r.QueuedAt == nil {
		return false, nil
	}
	r.QueuedAt = nil
	return true, nil
}

func (f *gitQueueWebhooks) RecordDelivery(_ context.Context, domainID string, at time.Time, result string) error {
	if r, ok := f.rows[domainID]; ok {
		if !at.IsZero() {
			r.LastDeliveryAt = &at
		}
		r.LastResult = result
	}
	return nil
}

type gitQueueDeployments struct {
	repository.DomainGitDeploymentRepository
	runs map[string]*models.DomainGitDeployment
}

func (f *gitQueueDeployments) CreateUnlessRunning(_ context.Context, run *models.DomainGitDeployment) error {
	for _, r := range f.runs {
		if r.DomainID == run.DomainID && r.State == models.GitDeployRunning {
			return repository.ErrGitDeploymentActive
		}
	}
	cp := *run
	f.runs[run.ID] = &cp
	return nil
}

func (f *gitQueueDeployments) Save(_ context.Context, run *models.DomainGitDeployment) error {
	cp := *run
	f.runs[run.ID] = &cp
	return nil
}

func (f *gitQueueDeployments) ListRunning(_ context.Context, _ int) ([]models.DomainGitDeployment, error) {
	var out []models.DomainGitDeployment
	for _, r := range f.runs {
		if r.State == models.GitDeployRunning {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *gitQueueDeployments) SetDocRoot(context.Context, string, string, string) (bool, error) {
	return false, nil
}

type gitQueueRepos struct {
	repository.DomainGitRepoRepository
	row *models.DomainGitRepo
}

func (f *gitQueueRepos) Get(_ context.Context, domainID string) (*models.DomainGitRepo, error) {
	if f.row == nil || f.row.DomainID != domainID {
		return nil, repository.ErrNotFound
	}
	cp := *f.row
	return &cp, nil
}

type gitQueuePackages struct {
	repository.PackageRepository
	pkg *models.HostingPackage
}

func (f *gitQueuePackages) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return f.pkg, nil
}

type gitQueueEnv struct {
	r       *Reconciler
	agent   *fakeAgent
	hooks   *gitQueueWebhooks
	deploys *gitQueueDeployments
	pkg     *models.HostingPackage
	pushed  time.Time
}

// newGitQueueEnv: alice's example.com deploys main, and a push to it waits.
func newGitQueueEnv(t *testing.T) *gitQueueEnv {
	t.Helper()
	name, pkgID := "alice", "pkg"
	pushed := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	e := &gitQueueEnv{
		agent: &fakeAgent{resultByMethod: map[string]json.RawMessage{
			"git.deploy_start":  json.RawMessage(`{"unit":"u","php_version":"8.3"}`),
			"git.deploy_status": json.RawMessage(`{"state":"running"}`),
			"git.deploy_stop":   json.RawMessage(`{}`),
		}},
		hooks: &gitQueueWebhooks{rows: map[string]*models.DomainGitWebhook{
			"d1": {DomainID: "d1", HookID: "0123456789abcdef0123456789abcdef", QueuedAt: &pushed, LastDeliveryAt: &pushed, LastResult: models.GitWebhookQueued},
		}},
		deploys: &gitQueueDeployments{runs: map[string]*models.DomainGitDeployment{}},
		pkg:     &models.HostingPackage{ID: pkgID, GitDeployEnabled: true},
		pushed:  pushed,
	}
	domains := &fakeDomainRepo{domains: map[string]*models.Domain{"d1": {ID: "d1", UserID: "u1", Name: "example.com"}}}
	users := &fakeUserRepo{users: map[string]*models.User{"u1": {ID: "u1", Username: &name, PackageID: &pkgID}}}
	e.r = New(domains, users, e.agent, slog.New(slog.NewTextHandler(os.Stderr, nil)), Config{})
	e.r.WithGitDeployments(e.deploys)
	e.r.WithGitWebhooks(e.hooks, &gitdeploy.Deployer{
		Agent:       e.agent,
		Packages:    &gitQueuePackages{pkg: e.pkg},
		Repos:       &gitQueueRepos{row: &models.DomainGitRepo{DomainID: "d1", UserID: "u1", RepoURL: "https://gitlab.com/acme/shop.git", Branch: "main"}},
		Deployments: e.deploys,
		CheckHost:   func(context.Context, string) error { return nil },
	})
	return e
}

func (e *gitQueueEnv) started() []*models.DomainGitDeployment {
	var out []*models.DomainGitDeployment
	for _, r := range e.deploys.runs {
		if r.Source == models.GitDeploySourceWebhook {
			out = append(out, r)
		}
	}
	return out
}

// Once the domain has no run in progress, the push that waited deploys the
// branch, as the domain's owner.
func TestGitQueue_AWaitingPushDeploysWhenTheRunHasEnded(t *testing.T) {
	e := newGitQueueEnv(t)
	e.r.reconcileGitDeployments(context.Background())
	runs := e.started()
	if len(runs) != 1 || runs[0].Branch != "main" || runs[0].UserID != "u1" || runs[0].OSUser != "alice" {
		t.Fatalf("runs = %+v", runs)
	}
	h := e.hooks.rows["d1"]
	if h.QueuedAt != nil || h.LastResult != models.GitWebhookStarted || !h.LastDeliveryAt.Equal(e.pushed) {
		t.Fatalf("webhook = %+v", h)
	}
	// Taken once: the next pass starts nothing more.
	e.r.reconcileGitDeployments(context.Background())
	if n := len(e.started()); n != 1 {
		t.Fatalf("runs after a second pass = %d", n)
	}
}

func TestGitQueue_APushKeepsWaitingWhileTheRunGoesOn(t *testing.T) {
	e := newGitQueueEnv(t)
	now := time.Now().UTC()
	e.deploys.runs["r0"] = &models.DomainGitDeployment{
		ID: "r0", UserID: "u1", DomainID: "d1", OSUser: "alice", Kind: models.GitDeployKindDeploy,
		Source: models.GitDeploySourceManual, ReleaseID: "r0", State: models.GitDeployRunning,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	e.r.reconcileGitDeployments(context.Background())
	if n := len(e.started()); n != 0 {
		t.Fatalf("runs = %d", n)
	}
	if h := e.hooks.rows["d1"]; h.QueuedAt == nil || h.LastResult != models.GitWebhookQueued {
		t.Fatalf("webhook = %+v", h)
	}

	e.agent.resultByMethod["git.deploy_status"] = json.RawMessage(`{"state":"succeeded","result":"success"}`)
	e.r.reconcileGitDeployments(context.Background())
	if n := len(e.started()); n != 1 {
		t.Fatalf("runs once the run ended = %d", n)
	}
}

// A package that no longer allows Git deploy ends the wait, and says so.
func TestGitQueue_ARefusedPushStopsWaiting(t *testing.T) {
	e := newGitQueueEnv(t)
	e.pkg.GitDeployEnabled = false
	e.r.reconcileGitDeployments(context.Background())
	if n := len(e.started()); n != 0 {
		t.Fatalf("runs = %d", n)
	}
	if h := e.hooks.rows["d1"]; h.QueuedAt != nil || h.LastResult != models.GitWebhookRefused+"git_deploy_not_in_package" {
		t.Fatalf("webhook = %+v", h)
	}
}
