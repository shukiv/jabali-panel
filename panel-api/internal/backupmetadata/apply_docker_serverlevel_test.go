package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// existingUsersRepo makes applyUser a no-op (user already present) so Apply
// proceeds to the docker block without needing the rest of the user path.
type existingUsersRepo struct {
	repository.UserRepository
}

func (existingUsersRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	return &models.User{ID: id}, nil
}

// captureDockerRepo records the rows Apply tries to Create.
type captureDockerRepo struct {
	repository.DockerAppRepository
	created []*models.DockerApp
}

func (c *captureDockerRepo) Create(_ context.Context, app *models.DockerApp) error {
	c.created = append(c.created, app)
	return nil
}
func (c *captureDockerRepo) CreatePort(context.Context, *models.DockerAppPublishedPort) error {
	return nil
}

// GH #1360: a restored server-level app (ServerLevel=true) must keep UserID
// NULL — re-owning it to the restoring admin would subject it to tenant
// validation + cgroup scoping on the next reconcile. A tenant app is owned by
// the account being restored.
func TestApply_ServerLevelDockerAppKeepsNullOwner(t *testing.T) {
	dockers := &captureDockerRepo{}
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "admin-1"},
		DockerApps: []internalbackup.MetadataDockerApp{
			{ID: "srv-1", Slug: "jabali-sounder", ServerLevel: true},
			{ID: "ten-1", Slug: "nextcloud", ServerLevel: false},
		},
	}
	r := Apply(context.Background(), meta, Deps{Users: existingUsersRepo{}, DockerApps: dockers})
	if len(r.Errors) != 0 {
		t.Fatalf("apply errors: %v", r.Errors)
	}
	owner := map[string]*string{}
	for _, a := range dockers.created {
		owner[a.ID] = a.UserID
	}
	if got, ok := owner["srv-1"]; !ok || got != nil {
		t.Errorf("server-level app must restore with UserID=nil, got ok=%v ptr=%v", ok, got)
	}
	if got, ok := owner["ten-1"]; !ok || got == nil || *got != "admin-1" {
		gotVal := "<nil>"
		if got != nil {
			gotVal = *got
		}
		t.Errorf("tenant app must be owned by the restored account, got ok=%v owner=%s", ok, gotVal)
	}
}
