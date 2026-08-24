package main

// JAB-364: the CLI docker-app delete must DELETE the app's managed domains but
// DETACH — never delete — a tenant's own domain attached to a reverse-proxy app.

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type cleanupFakeDomains struct {
	repository.DomainRepository
	list     []models.Domain
	deleted  map[string]bool
	detached map[string]bool
}

func (f *cleanupFakeDomains) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	return f.list, int64(len(f.list)), nil
}
func (f *cleanupFakeDomains) Delete(_ context.Context, id string) error {
	if f.deleted == nil {
		f.deleted = map[string]bool{}
	}
	f.deleted[id] = true
	return nil
}
func (f *cleanupFakeDomains) DetachDockerApp(_ context.Context, id string, _ bool) error {
	if f.detached == nil {
		f.detached = map[string]bool{}
	}
	f.detached[id] = true
	return nil
}

func dptr(s string) *string { return &s }

type cleanupFakeAgent struct {
	agent.AgentInterface
	vhostRemoved []string
}

func (f *cleanupFakeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd == "docker_app.vhost_remove" {
		if m, ok := params.(map[string]string); ok {
			f.vhostRemoved = append(f.vhostRemoved, m["domain_name"])
		}
	}
	return nil, nil
}

func TestCleanupDockerAppDomains_DeletesManagedDetachesUserOwned(t *testing.T) {
	appID := "app1"
	dom := &cleanupFakeDomains{list: []models.Domain{
		// User-owned, attached to the reverse-proxy app → DETACH, keep the row.
		{ID: "d-user", DockerAppID: dptr(appID), ManagedBy: "", Name: "mine.example.com"},
		// Auto-created managed domain → DELETE + vhost_remove.
		{ID: "d-mgd", DockerAppID: dptr(appID), ManagedBy: models.DomainManagedByDockerApp, Name: "auto.example.com"},
		// Unrelated domain (different app) → untouched.
		{ID: "d-other", DockerAppID: dptr("app2"), ManagedBy: models.DomainManagedByDockerApp, Name: "other.example.com"},
	}}
	ag := &cleanupFakeAgent{}

	cleanupDockerAppDomains(context.Background(), dom, ag, appID)

	if dom.deleted["d-user"] {
		t.Fatal("user-owned domain d-user was DELETED — data loss; it must only be detached")
	}
	if !dom.detached["d-user"] {
		t.Fatal("user-owned domain d-user was not detached")
	}
	if !dom.deleted["d-mgd"] {
		t.Fatal("managed domain d-mgd was not deleted")
	}
	if dom.detached["d-mgd"] {
		t.Fatal("managed domain d-mgd should be deleted, not detached")
	}
	if dom.deleted["d-other"] || dom.detached["d-other"] {
		t.Fatal("unrelated domain d-other must be untouched")
	}
	if len(ag.vhostRemoved) != 1 || ag.vhostRemoved[0] != "auto.example.com" {
		t.Fatalf("expected vhost_remove for the managed domain only; got %v", ag.vhostRemoved)
	}
}
