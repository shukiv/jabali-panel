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
	reserveErr  error
	allocErr    error
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

func (f *fakeAccounts) ReserveWithinCap(ctx context.Context, _ *models.FtpAccount, _ int, _ uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.reserveErr != nil {
		return f.reserveErr
	}
	*f.log = append(*f.log, "repo.Reserve")
	return nil
}

func (f *fakeAccounts) AllocateUID(context.Context) (uint32, error) {
	if f.allocErr != nil {
		return 0, f.allocErr
	}
	*f.log = append(*f.log, "repo.AllocateUID")
	return 1000000007, nil
}

// fakeAgent records each call as "agent:<command>[keys]" and honours context
// cancellation like the real client, so a host call made on a cancelled
// (non-detached) context never reaches the transcript.
type fakeAgent struct {
	log   *transcript
	errOn map[string]error
	// afterCall runs once a call is recorded (whatever its outcome), so a test
	// can act at the instant a host call lands (e.g. the client disconnects).
	afterCall func(command string)
}

func (a *fakeAgent) Call(ctx context.Context, command string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*a.log = append(*a.log, "agent:"+command+"["+paramKeys(params)+"]")
	if a.afterCall != nil {
		a.afterCall(command)
	}
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

// Once the host alias is gone the row must follow it: a client disconnect at
// that instant used to abort the row delete, leaving a row whose alias no
// longer exists — which the reconciler then re-provisions with a throwaway
// password, resurrecting an account the user deleted.
func TestDelete_CancelAfterHostDeleteStillRemovesRow(t *testing.T) {
	var log transcript
	d, _, ag := newDeps(&log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ag.afterCall = func(command string) {
		if command == "ftpaccount.delete" {
			cancel()
		}
	}

	if err := Delete(ctx, d, testAccount(), "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !log.has("repo.Delete") || !log.has(syncOp) {
		t.Fatalf("a post-host-delete cancellation must not strand the row or skip the re-render; transcript = %v", log)
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

func TestSetPassword_WeakPasswordMakesNoHostCall(t *testing.T) {
	var log transcript
	d, _, _ := newDeps(&log)

	for _, pw := range []string{"short", strings.Repeat("x", PasswordMaxLen+1)} {
		err := SetPassword(context.Background(), d, testAccount(), "alice", pw)
		var ve *ValidationError
		if !errors.As(err, &ve) || !errors.Is(err, ErrWeakPassword) {
			t.Fatalf("len %d: err = %v, want a ValidationError(ErrWeakPassword)", len(pw), err)
		}
		if ve.Detail != "password must be 12-128 characters" {
			t.Errorf("detail = %q", ve.Detail)
		}
	}
	if len(log) != 0 {
		t.Fatalf("a rejected password must not reach the host; transcript = %v", log)
	}
}

// JAB-261: chpasswd drops the shadow lock, so the desired lock state must ride
// in the same verb or a disabled account is silently unlocked.
func TestSetPassword_SendsLockStateInSameVerb(t *testing.T) {
	var log transcript
	d, _, _ := newDeps(&log)

	if err := SetPassword(context.Background(), d, testAccount(), "alice", "correct-horse-battery"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	want := "agent:ftpaccount.set_password[enabled,password,tenant_username,username]"
	if len(log) != 1 || log[0] != want {
		t.Fatalf("transcript = %v, want exactly %s", log, want)
	}
}

const createOp = "agent:ftpaccount.create[ftp_access,home_path,password,tenant_username,username,webdav_access]"

func createReq() CreateRequest {
	return CreateRequest{Label: "web", HomePath: "/home/alice/public_html", Password: "correct-horse-battery", FTPAccess: true}
}

var testOwner = Owner{UserID: "user_01", Username: "alice"}
var testPkg = &models.HostingPackage{MaxFTPAccounts: 5, DiskQuotaMB: 1024}

func TestCreate_ReservesThenCreatesThenSyncs(t *testing.T) {
	var log transcript
	d, _, _ := newDeps(&log)

	acct, err := Create(context.Background(), d, testOwner, testPkg, createReq())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(log) < 3 || log[0] != "repo.Reserve" || log[1] != createOp || log[2] != syncOp {
		t.Fatalf("transcript = %v, want repo.Reserve → %s → %s", log, createOp, syncOp)
	}
	if acct.Username != "alice_web" || !acct.SFTPAccess || !acct.IsEnabled || acct.UserID != "user_01" {
		t.Errorf("created row = %+v, want alice_web, sftp default on, enabled, owned by user_01", acct)
	}
}

// Every rejected input fails before any side effect, with the typed reason and
// the exact detail the adapters surface.
func TestCreate_ValidationRejectsBeforeAnySideEffect(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CreateRequest, *Deps)
		reason error
		detail string
	}{
		{"bad label", func(r *CreateRequest, _ *Deps) { r.Label = "Bad-Label" }, ErrInvalidLabel,
			"label must be lowercase letters, digits, or underscores (max 20 chars)"},
		{"weak password", func(r *CreateRequest, _ *Deps) { r.Password = "short" }, ErrWeakPassword,
			"password must be 12-128 characters"},
		{"relative home", func(r *CreateRequest, _ *Deps) { r.HomePath = "public_html" }, ErrInvalidHomePath,
			"home_path must be an absolute, clean path"},
		{"bad rune home", func(r *CreateRequest, _ *Deps) { r.HomePath = "/home/alice/a b" }, ErrInvalidHomePath,
			"home_path must not contain whitespace, quotes, backslashes, or colons"},
		{"home outside tenant", func(r *CreateRequest, _ *Deps) { r.HomePath = "/home/bob" }, ErrInvalidHomePath,
			"home_path must be inside your home directory (/home/alice)"},
		{"isolated without quota mount", func(r *CreateRequest, _ *Deps) { r.Isolated, r.QuotaMB = true, 100 }, ErrIsolationUnavailable,
			"per-account disk quota is not configured on this host"},
		{"isolated without quota", func(r *CreateRequest, d *Deps) { r.Isolated = true; d.QuotaMount = "/" }, ErrQuotaRequired,
			"an isolated account requires a disk quota (quota_mb, in MB)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log transcript
			d, _, _ := newDeps(&log)
			req := createReq()
			tc.mutate(&req, &d)

			_, err := Create(context.Background(), d, testOwner, testPkg, req)
			var ve *ValidationError
			if !errors.As(err, &ve) || !errors.Is(err, tc.reason) {
				t.Fatalf("err = %v, want ValidationError(%v)", err, tc.reason)
			}
			if ve.Detail != tc.detail {
				t.Errorf("detail = %q, want %q", ve.Detail, tc.detail)
			}
			if len(log) != 0 {
				t.Fatalf("a rejected create must have no side effects; transcript = %v", log)
			}
		})
	}
}

// The 32-char cap is on the FULL account name (<tenant>_<label>): a valid
// 20-char label still overflows under a long tenant name.
func TestCreate_FullNameTooLong(t *testing.T) {
	var log transcript
	d, _, _ := newDeps(&log)
	owner := Owner{UserID: "user_02", Username: "averylongtenant"} // 15 chars
	req := createReq()
	req.Label = strings.Repeat("a", 20)
	req.HomePath = "/home/averylongtenant/public_html"

	_, err := Create(context.Background(), d, owner, testPkg, req)
	var ve *ValidationError
	if !errors.As(err, &ve) || !errors.Is(err, ErrLabelTooLong) {
		t.Fatalf("err = %v, want ValidationError(ErrLabelTooLong)", err)
	}
	want := `full account name "averylongtenant_` + req.Label + `" exceeds 32 characters`
	if ve.Detail != want {
		t.Errorf("detail = %q, want %q", ve.Detail, want)
	}
	if len(log) != 0 {
		t.Fatalf("a rejected create must have no side effects; transcript = %v", log)
	}
}

func TestCreate_ReserveFailureMakesNoHostCall(t *testing.T) {
	var log transcript
	d, accts, _ := newDeps(&log)
	accts.reserveErr = repository.ErrFtpCapExceeded

	_, err := Create(context.Background(), d, testOwner, testPkg, createReq())
	if !errors.Is(err, ErrPersist) || !errors.Is(err, repository.ErrFtpCapExceeded) {
		t.Fatalf("err = %v, want ErrPersist carrying ErrFtpCapExceeded", err)
	}
	if len(log) != 0 {
		t.Fatalf("a failed reservation must not touch the host; transcript = %v", log)
	}
}

// A host create failure compensates the reservation even if the client has
// already disconnected — otherwise a slot-consuming row is stranded.
func TestCreate_HostFailureCompensatesOnDetachedContext(t *testing.T) {
	var log transcript
	d, _, ag := newDeps(&log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ag.errOn["ftpaccount.create"] = &agent.AgentError{Code: "already_exists", Message: "exists"}
	// The client disconnects the instant the host create is attempted.
	ag.afterCall = func(command string) {
		if command == "ftpaccount.create" {
			cancel()
		}
	}

	_, err := Create(ctx, d, testOwner, testPkg, createReq())
	var ae *agent.AgentError
	if !errors.As(err, &ae) || errors.Is(err, ErrPersist) {
		t.Fatalf("err = %v, want the raw agent error", err)
	}
	if !log.has("repo.Delete") || log.has(syncOp) {
		t.Fatalf("the reservation must be compensated (and no sync run) despite the cancel; transcript = %v", log)
	}
}

func TestCreate_IsolatedAllocatesUIDAndJail(t *testing.T) {
	var log transcript
	d, _, _ := newDeps(&log)
	d.QuotaMount = "/"
	req := createReq()
	req.Isolated, req.QuotaMB = true, 256

	acct, err := Create(context.Background(), d, testOwner, testPkg, req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	isoCreate := "agent:ftpaccount.create[ftp_access,home_path,isolated,jail_path,password,quota_mb,quota_mount,tenant_username,uid,username,webdav_access]"
	if len(log) < 4 || log[0] != "repo.AllocateUID" || log[1] != "repo.Reserve" || log[2] != isoCreate {
		t.Fatalf("transcript = %v, want AllocateUID → Reserve → %s", log, isoCreate)
	}
	if acct.UID == nil || *acct.UID != 1000000007 || acct.JailPath != JailRoot+"/alice/alice_web" {
		t.Errorf("isolated row uid=%v jail=%q", acct.UID, acct.JailPath)
	}
}

func TestCreate_UIDAllocationFailureStopsBeforeReserve(t *testing.T) {
	var log transcript
	d, accts, _ := newDeps(&log)
	d.QuotaMount = "/"
	accts.allocErr = errors.New("allocator down")
	req := createReq()
	req.Isolated, req.QuotaMB = true, 256

	_, err := Create(context.Background(), d, testOwner, testPkg, req)
	if !errors.Is(err, ErrUIDAllocation) {
		t.Fatalf("err = %v, want ErrUIDAllocation", err)
	}
	if len(log) != 0 {
		t.Fatalf("a failed uid allocation must stop before the reservation; transcript = %v", log)
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
