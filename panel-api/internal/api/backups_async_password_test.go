package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// recBackupAgent records the agent method names a dispatch issues.
type recBackupAgent struct {
	mu    sync.Mutex
	calls []string
}

func (a *recBackupAgent) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	a.mu.Lock()
	a.calls = append(a.calls, method)
	a.mu.Unlock()
	if method == "backup.repo.password.write_temp" {
		return json.RawMessage(`{"path":"/run/jabali/restic-pw/abc"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (a *recBackupAgent) has(method string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.calls {
		if m == method {
			return true
		}
	}
	return false
}

type stubBackupUsers struct {
	repository.UserRepository
	u *models.User
}

func (s stubBackupUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if s.u != nil && s.u.ID == id {
		return s.u, nil
	}
	return nil, repository.ErrNotFound
}

type stubBackupDests struct {
	repository.BackupDestinationRepository
	d *models.BackupDestination
}

func (s stubBackupDests) Get(_ context.Context, id string) (*models.BackupDestination, error) {
	if s.d != nil && s.d.ID == id {
		return s.d, nil
	}
	return nil, repository.ErrNotFound
}

type stubBackupJobs struct {
	repository.BackupJobRepository
}

func (stubBackupJobs) Create(_ context.Context, _ *models.BackupJob) error { return nil }
func (stubBackupJobs) MarkStarted(_ context.Context, _ string) error       { return nil }

// JAB-302: backup.create is async — the agent spawns the backup goroutine and
// self-cleans the per-destination password tempfile when the job finishes. The
// admin account-backup dispatch must therefore use the async password helper and
// must NOT unlink the tempfile itself, or it races cleanup_temp against the live
// backup (the exact regression the agent comment records). This pins that the
// dispatch provisions the password + fires backup.create but issues no
// cleanup_temp.
func TestCreateForUser_AsyncDestPassword_NoCleanupRace(t *testing.T) {
	key := ssokey.Key{}
	enc, err := key.Seal([]byte("dest-secret"))
	require.NoError(t, err)
	dest := &models.BackupDestination{ID: "d1", Enabled: true, Kind: "sftp", PasswordEnc: enc}

	uname := "alice"
	ag := &recBackupAgent{}
	h := &backupHandler{cfg: BackupHandlerConfig{
		Users:        stubBackupUsers{u: &models.User{ID: "u1", Username: &uname, Email: "a@x.tld"}},
		Destinations: stubBackupDests{d: dest},
		Jobs:         stubBackupJobs{},
		Agent:        ag,
		SSOKey:       &key,
	}}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "u1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/users/u1/backups",
		strings.NewReader(`{"destination_id":"d1","content":"full","compression":"off"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.createForUser(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.True(t, ag.has("backup.repo.password.write_temp"),
		"must provision the per-destination password tempfile; calls=%v", ag.calls)
	require.True(t, ag.has("backup.create"), "must dispatch the backup; calls=%v", ag.calls)
	require.False(t, ag.has("backup.repo.password.cleanup_temp"),
		"admin account backup must NOT unlink the password tempfile — backup.create is async and the agent owns its lifetime (JAB-302); calls=%v", ag.calls)
}
