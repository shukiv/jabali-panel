package ftpops

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// transcript is the ordered log of repository writes and agent calls a test
// inspects. Agent entries carry their sorted param keys so the wire shape is
// pinned without depending on volatile values.
type transcript []string

func (t transcript) has(op string) bool {
	for _, o := range t {
		if o == op {
			return true
		}
	}
	return false
}

func (t transcript) index(op string) int {
	for i, o := range t {
		if o == op {
			return i
		}
	}
	return -1
}

// fakeAccounts records Update/Delete into the transcript. Both honour context
// cancellation like the real GORM repository; afterUpdate lets a test act at the
// instant the row commits (e.g. cancel the request).
type fakeAccounts struct {
	repository.FtpAccountRepository
	log         *transcript
	updateErr   error
	deleteErr   error
	afterUpdate func()
}

func (f *fakeAccounts) Update(ctx context.Context, _ *models.FtpAccount) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.updateErr != nil {
		return f.updateErr
	}
	*f.log = append(*f.log, "repo.Update")
	if f.afterUpdate != nil {
		f.afterUpdate()
	}
	return nil
}

func (f *fakeAccounts) Delete(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	*f.log = append(*f.log, "repo.Delete")
	return nil
}

// List backs the sshd re-render; an empty desired set is enough to observe it.
func (f *fakeAccounts) List(context.Context) ([]models.FtpAccount, error) { return nil, nil }

// fakeAgent records each call as "agent:<command>[keys]" and honours context
// cancellation like the real client, so a host call made on a cancelled
// (non-detached) context never reaches the transcript.
type fakeAgent struct {
	log   *transcript
	errOn map[string]error
}

func (a *fakeAgent) Call(ctx context.Context, command string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*a.log = append(*a.log, "agent:"+command+"["+paramKeys(params)+"]")
	if err, ok := a.errOn[command]; ok {
		return nil, err
	}
	return json.RawMessage(`{}`), nil
}

func paramKeys(params any) string {
	m, _ := params.(map[string]any)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

type fakeUsers struct{ repository.UserRepository }

const (
	setAccessOp = "agent:ftpaccount.set_access[enabled,ftp_access,tenant_username,username,webdav_access]"
	deleteOp    = "agent:ftpaccount.delete[tenant_username,username]"
	syncOp      = "agent:ssh.user.home_chown[mode,username]"
)

func newDeps(log *transcript) (Deps, *fakeAccounts, *fakeAgent) {
	accts := &fakeAccounts{log: log}
	ag := &fakeAgent{log: log, errOn: map[string]error{}}
	return Deps{Agent: ag, Accounts: accts, Users: fakeUsers{}}, accts, ag
}

func testAccount() *models.FtpAccount {
	return &models.FtpAccount{ID: "acct_01", UserID: "user_01", Username: "alice_web", FTPAccess: true, IsEnabled: false}
}

func TestUpdateAccess_PersistsBeforeHostThenSyncs(t *testing.T) {
	var log transcript
	d, _, _ := newDeps(&log)

	if err := UpdateAccess(context.Background(), d, testAccount(), "alice"); err != nil {
		t.Fatalf("UpdateAccess: %v", err)
	}
	if len(log) < 3 || log[0] != "repo.Update" || log[1] != setAccessOp || log[2] != syncOp {
		t.Fatalf("transcript = %v, want repo.Update → %s → %s", log, setAccessOp, syncOp)
	}
}

func TestUpdateAccess_PersistFailMakesNoHostCall(t *testing.T) {
	var log transcript
	d, accts, _ := newDeps(&log)
	dbErr := errors.New("db down")
	accts.updateErr = dbErr

	err := UpdateAccess(context.Background(), d, testAccount(), "alice")
	if !errors.Is(err, ErrPersist) || !errors.Is(err, dbErr) {
		t.Fatalf("err = %v, want ErrPersist wrapping the repository error", err)
	}
	if len(log) != 0 {
		t.Fatalf("a failed persist must touch nothing on the host; transcript = %v", log)
	}
}

func TestUpdateAccess_CancelledBeforePersistChangesNothing(t *testing.T) {
	var log transcript
	d, _, _ := newDeps(&log)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := UpdateAccess(ctx, d, testAccount(), "alice")
	if !errors.Is(err, ErrPersist) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrPersist wrapping context.Canceled", err)
	}
	if len(log) != 0 {
		t.Fatalf("a pre-commit cancellation must change nothing; transcript = %v", log)
	}
}

func TestUpdateAccess_HostApplyDetachedFromCancel(t *testing.T) {
	var log transcript
	d, accts, _ := newDeps(&log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The client disconnects the instant the row commits.
	accts.afterUpdate = cancel

	if err := UpdateAccess(ctx, d, testAccount(), "alice"); err != nil {
		t.Fatalf("UpdateAccess: %v", err)
	}
	if !log.has(setAccessOp) || !log.has(syncOp) {
		t.Fatalf("a post-commit cancellation must not stop the host apply; transcript = %v", log)
	}
}

func TestUpdateAccess_HostFailureStillSyncsAndKeepsRow(t *testing.T) {
	var log transcript
	d, _, ag := newDeps(&log)
	hostErr := &agent.AgentError{Code: "invalid_argument", Message: "bad"}
	ag.errOn["ftpaccount.set_access"] = hostErr

	err := UpdateAccess(context.Background(), d, testAccount(), "alice")
	var ae *agent.AgentError
	if !errors.As(err, &ae) || errors.Is(err, ErrPersist) {
		t.Fatalf("err = %v, want the raw agent error (not ErrPersist)", err)
	}
	if log.index(syncOp) < log.index(setAccessOp) || !log.has(syncOp) {
		t.Fatalf("the sshd re-render must still run after a set_access failure; transcript = %v", log)
	}
	updates := 0
	for _, o := range log {
		if o == "repo.Update" {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("the committed row must not be reverted on a host failure; repo writes = %d (%v)", updates, log)
	}
}

func TestDelete_HostFirstThenRowThenSync(t *testing.T) {
	var log transcript
	d, _, _ := newDeps(&log)

	if err := Delete(context.Background(), d, testAccount(), "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(log) < 3 || log[0] != deleteOp || log[1] != "repo.Delete" || log[2] != syncOp {
		t.Fatalf("transcript = %v, want %s → repo.Delete → %s", log, deleteOp, syncOp)
	}
}

func TestDelete_HostFailureKeepsRow(t *testing.T) {
	var log transcript
	d, _, ag := newDeps(&log)
	ag.errOn["ftpaccount.delete"] = &agent.AgentError{Code: "not_found", Message: "gone"}

	err := Delete(context.Background(), d, testAccount(), "alice")
	var ae *agent.AgentError
	if !errors.As(err, &ae) || errors.Is(err, ErrPersist) {
		t.Fatalf("err = %v, want the raw agent error", err)
	}
	if log.has("repo.Delete") || log.has(syncOp) {
		t.Fatalf("a failed host delete must keep the row (the retry handle) and skip the sync; transcript = %v", log)
	}
}

func TestDelete_RowFailureAfterHostSkipsSync(t *testing.T) {
	var log transcript
	d, accts, _ := newDeps(&log)
	accts.deleteErr = errors.New("db down")

	err := Delete(context.Background(), d, testAccount(), "alice")
	if !errors.Is(err, ErrPersist) {
		t.Fatalf("err = %v, want ErrPersist", err)
	}
	if !log.has(deleteOp) || log.has(syncOp) {
		t.Fatalf("transcript = %v, want the host delete and no sync", log)
	}
}

func TestLifecycle_NilAgentIsUnavailable(t *testing.T) {
	var log transcript
	d, _, _ := newDeps(&log)
	d.Agent = nil

	if err := Delete(context.Background(), d, testAccount(), "alice"); err == nil || err.Error() != "agent unavailable" {
		t.Fatalf("Delete with no agent: err = %v, want \"agent unavailable\"", err)
	}
	if log.has("repo.Delete") {
		t.Fatalf("no agent means no host delete, so the row must stay; transcript = %v", log)
	}
}
