package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// recordedOp is one step in a handler's state-transition transcript: either a
// repository write ("repo.Update") or an Agent command ("agent:<command>"),
// captured in the order it happened. Keys is the sorted comma-joined set of
// top-level parameter keys for an Agent call (empty for a repo write) so the
// transcript pins the wire contract's SHAPE without pinning volatile values
// (a fresh sshd_sync generation, the resolved tenant username).
type recordedOp struct {
	Name string
	Keys string
}

// transcriptAgent records every Agent call — command plus sorted param keys —
// into a shared ordered log it also shares with the repo's onUpdate hook, so a
// test sees repo writes and host calls interleaved in one transcript. It honours
// context cancellation like the real agent path, and can be told to fail a chosen
// command while still recording the attempt.
type transcriptAgent struct {
	ops   *[]recordedOp
	errOn map[string]error
}

func (a *transcriptAgent) Call(ctx context.Context, command string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*a.ops = append(*a.ops, recordedOp{Name: "agent:" + command, Keys: sortedParamKeys(params)})
	if a.errOn != nil {
		if err, ok := a.errOn[command]; ok {
			return nil, err
		}
	}
	return json.RawMessage(`{}`), nil
}

func sortedParamKeys(params any) string {
	b, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// runFtpUpdateTranscript drives one access-update door (admin or tenant) against
// the same account with the same body and returns the recorded transcript plus
// the handler's status code. The two doors resolve authorization differently —
// admin loads any account by id, the tenant loads its own — which is by design
// (JAB-276: "Adapters should resolve authorization only"); everything after that
// resolution is the shared mutation transcript this test locks.
func runFtpUpdateTranscript(t *testing.T, admin bool, setAccessErr error) ([]recordedOp, int) {
	t.Helper()
	repo := newFakeFtpRepo()
	repo.rows["acc1"] = &models.FtpAccount{ID: "acc1", UserID: "u1", Username: "shop_dev", IsEnabled: false, SFTPAccess: true}

	ops := []recordedOp{}
	repo.onUpdate = func() { ops = append(ops, recordedOp{Name: "repo.Update"}) }

	ag := &transcriptAgent{ops: &ops}
	if setAccessErr != nil {
		ag.errOn = map[string]error{"ftpaccount.set_access": setAccessErr}
	}

	uname := "shop"
	pkgID := "pkg1"
	users := &usersMap{m: map[string]*models.User{"u1": {ID: "u1", Username: &uname, PackageID: &pkgID}}}
	h := &ftpAccountsHandler{cfg: FtpAccountsHandlerConfig{
		Repo: repo, Users: users, Packages: &fakePkgRepo{pkg: ftpPkg(3)}, Agent: ag, QuotaMount: "/",
	}}

	r := gin.New()
	claimUID := "u1"
	path := "/me/ftp-accounts/:id"
	reqPath := "/me/ftp-accounts/acc1"
	handler := h.update
	if admin {
		claimUID = "admin1"
		path = "/admin/ftp-accounts/:id"
		reqPath = "/admin/ftp-accounts/acc1"
		handler = h.adminUpdate
	}
	r.Use(func(c *gin.Context) { ginctx.SetClaims(c, &auth.AccessClaims{UserID: claimUID}); c.Next() })
	r.PATCH(path, handler)

	rec := doReq(t, r, http.MethodPatch, reqPath, `{"is_enabled":true}`)
	return ops, rec.Code
}

func opNames(ops []recordedOp) []string {
	names := make([]string, len(ops))
	for i, o := range ops {
		names[i] = o.Name
	}
	return names
}

// TestFtpAPIUpdate_AdminTenantTranscriptsIdentical locks JAB-276 AC1: the admin
// and tenant access-update doors produce an identical state-transition transcript
// once authorization is resolved. Both persist the row first, then apply the host
// on a detached context via ftpaccount.set_access, then re-render the sshd drop-in
// via ftpaccount.sshd_sync — in that order, with the same set_access wire shape.
//
// The two per-door tests already prove each door is DB-first individually; this
// test is the parity lock they don't provide — it pins that the two transcripts
// are byte-for-byte equal, so admin and tenant can never silently diverge again
// (the pre-JAB-276 admin door was host-first, a different transcript).
func TestFtpAPIUpdate_AdminTenantTranscriptsIdentical(t *testing.T) {
	tenant, tenantCode := runFtpUpdateTranscript(t, false, nil)
	admin, adminCode := runFtpUpdateTranscript(t, true, nil)

	require.Equal(t, http.StatusOK, tenantCode, "tenant update happy path returns 200")
	require.Equal(t, http.StatusOK, adminCode, "admin update happy path returns 200")

	// The command sequence is pinned explicitly: persist, then host apply, then
	// drop-in re-render.
	wantSequence := []string{
		"repo.Update",
		"agent:ftpaccount.set_access",
		"agent:ssh.user.home_chown",
		"agent:ftpaccount.sshd_sync",
	}
	require.Equal(t, wantSequence, opNames(tenant), "tenant update transcript order")

	// The set_access wire contract is pinned by its key set (values differ by the
	// resolved tenant, which is the authorization adapter's job).
	require.Equal(t, recordedOp{
		Name: "agent:ftpaccount.set_access",
		Keys: "enabled,ftp_access,tenant_username,username,webdav_access",
	}, tenant[1], "set_access payload shape")

	// The parity property: the two doors' full transcripts are identical.
	require.Equal(t, tenant, admin,
		"admin and tenant access-update transcripts must be identical (JAB-276 AC1)")
}

// TestFtpAPIUpdate_AdminTenantErrorTranscriptsIdentical locks the failure-path
// half of JAB-276 AC1: when the host unix-lock (set_access) fails after the DB
// commit, both doors keep the identical transcript — the row is committed once,
// set_access is attempted, and sshd_sync still runs (the SFTP revocation is
// defense-in-depth, rendered from the committed row) — and both surface a
// non-2xx error rather than reverting the row.
func TestFtpAPIUpdate_AdminTenantErrorTranscriptsIdentical(t *testing.T) {
	boom := errors.New("agent boom")
	tenant, tenantCode := runFtpUpdateTranscript(t, false, boom)
	admin, adminCode := runFtpUpdateTranscript(t, true, boom)

	require.NotEqual(t, http.StatusOK, tenantCode, "a host-apply failure must surface an error on the tenant door")
	require.Equal(t, tenantCode, adminCode, "admin and tenant doors return the same status on a host-apply failure")

	wantSequence := []string{
		"repo.Update",
		"agent:ftpaccount.set_access",
		"agent:ssh.user.home_chown",
		"agent:ftpaccount.sshd_sync",
	}
	require.Equal(t, wantSequence, opNames(tenant),
		"a set_access failure still commits the row once and still re-renders the drop-in")

	require.Equal(t, tenant, admin,
		"admin and tenant access-update failure transcripts must be identical (JAB-276 AC1)")
}
