package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a bundle from an uploaded file (Deps.Untrusted) can't claim
// admin-level things: server-level docker apps and admin-only custom nginx
// directives are not restored from it. And no restore binds a docker app to a
// data directory another account's app already uses.

type utDocker struct {
	repository.DockerAppRepository
	existing []*models.DockerApp
	created  []*models.DockerApp
}

func (r *utDocker) ListAll(context.Context) ([]*models.DockerApp, error) { return r.existing, nil }
func (r *utDocker) Create(_ context.Context, a *models.DockerApp) error {
	r.created = append(r.created, a)
	return nil
}
func (r *utDocker) CreatePort(context.Context, *models.DockerAppPublishedPort) error { return nil }

func createdApps(r *utDocker) map[string]bool {
	out := map[string]bool{}
	for _, a := range r.created {
		out[a.ID] = true
	}
	return out
}

func TestApply_UploadedBackupRestoresNoServerLevelDockerApp(t *testing.T) {
	dockers := &utDocker{}
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1"},
		DockerApps: []internalbackup.MetadataDockerApp{
			{ID: "srv-1", Slug: "jabali-sounder", ServerLevel: true},
			{ID: "ten-1", Slug: "nextcloud"},
		},
	}

	r := Apply(context.Background(), meta, Deps{Users: existingUsersRepo{}, DockerApps: dockers, Untrusted: true})

	got := createdApps(dockers)
	if got["srv-1"] || !got["ten-1"] {
		t.Fatalf("created %v, want only the account's own app ten-1", got)
	}
	if !hasError(r.Errors, "server-level app can't be restored from an uploaded backup") {
		t.Fatalf("errors %v should say why srv-1 was not restored", r.Errors)
	}
}

func TestApply_UploadedBackupDropsCustomNginxDirectives(t *testing.T) {
	for _, untrusted := range []bool{true, false} {
		pools := &ppPools{}
		doms := &ppDomains{pools: pools}
		meta := ppMeta()
		meta.PHPPools, meta.Domains[0].PHPPoolID, meta.Domains[0].Mailboxes = nil, nil, nil
		raw := "location /x { fastcgi_pass unix:/run/php/bob.sock; }"
		meta.Domains[0].NginxCustomDirectives = &raw
		deps := ppDeps(pools, doms, &dcMailboxes{})
		deps.Untrusted = untrusted

		r := Apply(context.Background(), meta, deps)

		if len(doms.created) != 1 {
			t.Fatalf("untrusted=%v: domains = %+v (errors %v), want alice.org", untrusted, doms.created, r.Errors)
		}
		got := doms.created[0].NginxCustomDirectives
		if untrusted && (got != nil || !hasError(r.Errors, "custom nginx directives not restored from an uploaded backup")) {
			t.Fatalf("uploaded backup: directives = %v, errors %v; want dropped with a note", got, r.Errors)
		}
		if !untrusted && (got == nil || *got != raw) {
			t.Fatalf("own destination: directives = %v, want kept", got)
		}
	}
}

func TestApply_DockerAppOnAnotherAccountsDataDirIsRefused(t *testing.T) {
	bob, me := "u-bob", "u1"
	dockers := &utDocker{existing: []*models.DockerApp{
		{ID: "bob-gitea", UserID: &bob, Slug: "gitea", InstanceSlug: "gitea-1"},
		{ID: "srv-kuma", Slug: "uptime-kuma"}, // server-level, legacy empty instance slug
		{ID: "mine", UserID: &me, Slug: "n8n", InstanceSlug: "n8n-1"},
	}}
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1"},
		DockerApps: []internalbackup.MetadataDockerApp{
			{ID: "a1", Slug: "gitea", InstanceSlug: "gitea-1"},
			{ID: "a2", Slug: "uptime-kuma"},
			{ID: "a3", Slug: "n8n", InstanceSlug: "n8n-2"},
			{ID: "mine", Slug: "n8n", InstanceSlug: "n8n-1"}, // the account's own app, restored over itself
		},
	}

	r := Apply(context.Background(), meta, Deps{Users: existingUsersRepo{}, DockerApps: dockers})

	got := createdApps(dockers)
	if got["a1"] || got["a2"] {
		t.Fatalf("created %v: an app was bound to another account's data dir", got)
	}
	if !got["a3"] {
		t.Fatalf("created %v (errors %v): a new instance slug should restore", got, r.Errors)
	}
	if !hasError(r.Errors, `docker_app a1: not restored: another account's app already uses "gitea-1"`) ||
		!hasError(r.Errors, `docker_app a2: not restored: another account's app already uses "uptime-kuma"`) {
		t.Fatalf("errors %v should name both refusals", r.Errors)
	}
	if hasError(r.Errors, "docker_app mine:") {
		t.Fatalf("errors %v: the account's own app must not be refused", r.Errors)
	}
}
