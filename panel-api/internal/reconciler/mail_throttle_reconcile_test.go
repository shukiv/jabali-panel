package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailthrottle"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeThrottleApplier stands in for stalwartadmin.Throttles. Apply hands out a
// new id for an empty one and keeps a known one, unless reassign names a
// replacement (Stalwart lost the object). stalwart is what List returns; a
// created throttle is added to it and a deleted one removed.
type fakeThrottleApplier struct {
	mu       sync.Mutex
	applies  []mailthrottle.ApplyRequest
	deletes  []string
	failOn   map[string]error // keyed "apply:<window>", "delete" or "list"
	reassign map[string]string
	nextID   int
	stalwart []mailthrottle.ListItem
	onApply  func(req mailthrottle.ApplyRequest)
}

func (f *fakeThrottleApplier) List(context.Context) ([]mailthrottle.ListItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failOn["list"]; ok {
		return nil, err
	}
	return append([]mailthrottle.ListItem(nil), f.stalwart...), nil
}

func (f *fakeThrottleApplier) Apply(_ context.Context, req mailthrottle.ApplyRequest) (mailthrottle.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies = append(f.applies, req)
	if f.onApply != nil {
		f.onApply(req)
	}
	if err, ok := f.failOn["apply:"+req.Window]; ok {
		return mailthrottle.ApplyResult{}, err
	}
	if req.StalwartID == "" {
		f.nextID++
		id := fmt.Sprintf("stw-new-%d", f.nextID)
		f.stalwart = append(f.stalwart, mailthrottle.ListItem{StalwartID: id, Description: mailthrottle.Description(req)})
		return mailthrottle.ApplyResult{StalwartID: id, Changed: true}, nil
	}
	if id, ok := f.reassign[req.StalwartID]; ok {
		return mailthrottle.ApplyResult{StalwartID: id, Changed: true}, nil
	}
	return mailthrottle.ApplyResult{StalwartID: req.StalwartID}, nil
}

func (f *fakeThrottleApplier) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failOn["delete"]; ok {
		return err
	}
	f.deletes = append(f.deletes, id)
	for i, it := range f.stalwart {
		if it.StalwartID == id {
			f.stalwart = append(f.stalwart[:i], f.stalwart[i+1:]...)
			break
		}
	}
	return nil
}

type fakeOutboundPolicyRepo struct {
	listCalls int
	listErrOn int   // the List call (1-based) that fails; 0 = none
	stampErr  error // every UpdateApplyState* fails with it
	rows      map[string]*models.MailOutboundPolicy
	stamped   []stampCall
}

type stampCall struct {
	rowID, window, stalwartID string
	lastErr                   string
}

func (f *fakeOutboundPolicyRepo) Create(_ context.Context, p *models.MailOutboundPolicy) error {
	f.rows[p.ID] = p
	return nil
}
func (f *fakeOutboundPolicyRepo) Update(_ context.Context, p *models.MailOutboundPolicy) error {
	f.rows[p.ID] = p
	return nil
}
func (f *fakeOutboundPolicyRepo) FindByID(_ context.Context, id string) (*models.MailOutboundPolicy, error) {
	r, ok := f.rows[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return r, nil
}
func (f *fakeOutboundPolicyRepo) FindByScope(_ context.Context, _ string, _ *string) (*models.MailOutboundPolicy, error) {
	return nil, errors.New("not found")
}
func (f *fakeOutboundPolicyRepo) List(_ context.Context) ([]models.MailOutboundPolicy, error) {
	f.listCalls++
	if f.listCalls == f.listErrOn {
		return nil, errors.New("db: connection reset")
	}
	out := make([]models.MailOutboundPolicy, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, *r)
	}
	return out, nil
}
func (f *fakeOutboundPolicyRepo) Delete(_ context.Context, id string) error {
	delete(f.rows, id)
	return nil
}
func (f *fakeOutboundPolicyRepo) UpdateApplyState(_ context.Context, id, stalwartID string, lastErr *string) error {
	if f.stampErr != nil {
		return f.stampErr
	}
	var le string
	if lastErr != nil {
		le = *lastErr
	}
	f.stamped = append(f.stamped, stampCall{rowID: id, window: mailthrottle.WindowHour, stalwartID: stalwartID, lastErr: le})
	// mutate the row so subsequent ticks see the new state.
	if r, ok := f.rows[id]; ok {
		r.StalwartID = stalwartID
		r.LastError = lastErr
	}
	return nil
}

func (f *fakeOutboundPolicyRepo) UpdateApplyStateDaily(_ context.Context, id, stalwartIDDaily string, lastErr *string) error {
	if f.stampErr != nil {
		return f.stampErr
	}
	var le string
	if lastErr != nil {
		le = *lastErr
	}
	f.stamped = append(f.stamped, stampCall{rowID: id, window: mailthrottle.WindowDay, stalwartID: stalwartIDDaily, lastErr: le})
	if r, ok := f.rows[id]; ok {
		r.StalwartIDDaily = stalwartIDDaily
		r.LastError = lastErr
	}
	return nil
}

func newFakeOutboundPolicyRepo() *fakeOutboundPolicyRepo {
	return &fakeOutboundPolicyRepo{rows: map[string]*models.MailOutboundPolicy{}}
}

func throttleRecForTest(t *testing.T) (*Reconciler, *fakeOutboundPolicyRepo, *fakeThrottleApplier) {
	t.Helper()
	r := &Reconciler{log: slog.Default()}
	repo := newFakeOutboundPolicyRepo()
	cl := &fakeThrottleApplier{}
	r.WithMailThrottles(repo, cl)
	return r, repo, cl
}

func TestReconcileMailThrottles_CreatesWhenStalwartIDEmpty(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 100, Enabled: true,
	}
	r.reconcileMailThrottles(context.Background())
	require.Len(t, cl.applies, 1)
	assert.Equal(t, mailthrottle.ApplyRequest{Scope: "global", Window: "hour", Limit: 100}, cl.applies[0])
	assert.Equal(t, "stw-new-1", repo.rows["row1"].StalwartID)
	assert.Nil(t, repo.rows["row1"].LastError)
}

// A window that already has an id sends it, so the agent can compare and
// leave an unchanged object alone. When nothing changed the row is not
// written at all.
func TestReconcileMailThrottles_KnownIDIsSentAndNothingIsWritten(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 50,
		Enabled: true, StalwartID: "stw-existing",
	}
	r.reconcileMailThrottles(context.Background())
	require.Len(t, cl.applies, 1)
	assert.Equal(t, "stw-existing", cl.applies[0].StalwartID)
	assert.Empty(t, repo.stamped, "no DB write when the ids and the error are unchanged")
}

// Stalwart lost the object (deleted by hand, restore): the agent made a new
// one, and the row must point at it.
func TestReconcileMailThrottles_StampsAReplacedID(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	cl.reassign = map[string]string{"stw-lost": "stw-replacement"}
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 50,
		Enabled: true, StalwartID: "stw-lost",
	}
	r.reconcileMailThrottles(context.Background())
	assert.Equal(t, "stw-replacement", repo.rows["row1"].StalwartID)
}

func TestReconcileMailThrottles_DeletesWhenDisabledWithStalwartID(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 100, Enabled: false, StalwartID: "stw-going-away",
	}
	r.reconcileMailThrottles(context.Background())
	assert.Empty(t, cl.applies)
	require.Equal(t, []string{"stw-going-away"}, cl.deletes)
	assert.Equal(t, "", repo.rows["row1"].StalwartID, "stalwart_id cleared after delete")
}

func TestReconcileMailThrottles_NoOpWhenDisabledWithoutID(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, Enabled: false, StalwartID: "",
	}
	r.reconcileMailThrottles(context.Background())
	assert.Empty(t, cl.applies)
	assert.Empty(t, cl.deletes)
	assert.Empty(t, repo.stamped)
}

func TestReconcileMailThrottles_KeepsStalwartIDOnApplyError(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	cl.failOn = map[string]error{"apply:hour": errors.New("stalwart-cli get: connection refused")}
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 100,
		Enabled: true, StalwartID: "stw-keep-me",
	}
	r.reconcileMailThrottles(context.Background())
	assert.Equal(t, "stw-keep-me", repo.rows["row1"].StalwartID,
		"stalwart_id must NOT clear when apply fails — next tick retries")
	require.NotNil(t, repo.rows["row1"].LastError)
	assert.Contains(t, *repo.rows["row1"].LastError, "connection refused")
}

// Both windows share last_error. The daily window's success used to stamp
// last_error=NULL right after the hourly window stamped its failure, so the
// admin never saw why the hourly cap was not in Stalwart.
func TestReconcileMailThrottles_OneWindowsErrorSurvivesTheOthersSuccess(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	cl.failOn = map[string]error{"apply:hour": errors.New("hourly broke")}
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 100, MaxPerDay: 1000, Enabled: true,
	}
	r.reconcileMailThrottles(context.Background())
	require.NotNil(t, repo.rows["row1"].LastError, "hourly failure was wiped by the daily success")
	assert.Contains(t, *repo.rows["row1"].LastError, "hour: hourly broke")
	assert.Equal(t, "stw-new-1", repo.rows["row1"].StalwartIDDaily, "the daily window still applied")
}

func TestReconcileMailThrottles_ClearsLastErrorAfterRecovery(t *testing.T) {
	r, repo, _ := throttleRecForTest(t)
	old := "hour: stalwart down"
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 100,
		Enabled: true, StalwartID: "stw-1", LastError: &old,
	}
	r.reconcileMailThrottles(context.Background())
	assert.Nil(t, repo.rows["row1"].LastError)
	assert.Equal(t, "stw-1", repo.rows["row1"].StalwartID)
}

func TestThrottleRequest_CarriesTheSenderOrDomain(t *testing.T) {
	addr, dom, stray := "alice@example.com", "example.com", "leftover"
	cases := []struct {
		row  models.MailOutboundPolicy
		want string
	}{
		{models.MailOutboundPolicy{Scope: models.OutboundScopeUser, ScopeRef: &addr}, addr},
		{models.MailOutboundPolicy{Scope: models.OutboundScopeDomain, ScopeRef: &dom}, dom},
		{models.MailOutboundPolicy{Scope: models.OutboundScopeUser}, ""},
		// A global row never sends a ref; the agent would reject it.
		{models.MailOutboundPolicy{Scope: models.OutboundScopeGlobal, ScopeRef: &stray}, ""},
	}
	for _, c := range cases {
		row := c.row
		got := throttleRequest(&row, mailthrottle.WindowDay, 9, "id1")
		assert.Equal(t, mailthrottle.ApplyRequest{StalwartID: "id1", Scope: c.row.Scope, ScopeRef: c.want, Window: "day", Limit: 9}, got)
	}
}

// An admin deletes a row while a tick is applying it. The inline delete
// found no Stalwart id yet (or removed the old one), the tick then created a
// throttle for the row, and the row is gone: nothing references the new
// throttle, and it would cap that sender forever with nothing in the panel
// to show or remove it. The sweep reads the rows again and removes it in the
// same tick.
func TestReconcileMailThrottles_SweepsAThrottleWhoseRowWasDeletedMidTick(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 10, Enabled: true,
	}
	cl.onApply = func(mailthrottle.ApplyRequest) { delete(repo.rows, "row1") }
	r.reconcileMailThrottles(context.Background())
	require.Len(t, cl.applies, 1)
	assert.Equal(t, []string{"stw-new-1"}, cl.deletes, "the throttle created for the deleted row must be removed")
	assert.Empty(t, cl.stalwart)
}

func TestReconcileMailThrottles_SweepTouchesOnlyUnreferencedPanelThrottles(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 10, Enabled: true, StalwartID: "stw-kept",
	}
	cl.stalwart = []mailthrottle.ListItem{
		{StalwartID: "stw-kept", Description: "jabali global *: 10 per hour"},
		{StalwartID: "op1", Description: "operator: cap the newsletter box"},
		// Description format of the release before this one.
		{StalwartID: "old1", Description: "jabali global/-/hourly h=5 d=0"},
	}
	r.reconcileMailThrottles(context.Background())
	assert.Equal(t, []string{"old1"}, cl.deletes)
}

func TestReconcileMailThrottles_SweepGuards(t *testing.T) {
	orphan := mailthrottle.ListItem{StalwartID: "orphan1", Description: "jabali user a@example.com: 5 per hour"}
	cases := map[string]func(repo *fakeOutboundPolicyRepo, cl *fakeThrottleApplier){
		// An unreadable table must not look like an empty one.
		"rows cannot be read again": func(repo *fakeOutboundPolicyRepo, _ *fakeThrottleApplier) { repo.listErrOn = 2 },
		"Stalwart cannot be listed": func(_ *fakeOutboundPolicyRepo, cl *fakeThrottleApplier) {
			cl.failOn = map[string]error{"list": errors.New("agent down")}
		},
		// The id just created was not stamped, so no row references it yet.
		"a stamp failed": func(repo *fakeOutboundPolicyRepo, _ *fakeThrottleApplier) { repo.stampErr = errors.New("db: deadlock") },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r, repo, cl := throttleRecForTest(t)
			repo.rows["row1"] = &models.MailOutboundPolicy{
				ID: "row1", Scope: models.OutboundScopeGlobal, MaxPerHour: 10, Enabled: true,
			}
			cl.stalwart = []mailthrottle.ListItem{orphan}
			setup(repo, cl)
			r.reconcileMailThrottles(context.Background())
			assert.Empty(t, cl.deletes, "nothing may be swept")
		})
	}
}

// With no rows at all, every panel throttle in Stalwart is unreferenced.
func TestReconcileMailThrottles_SweepWithNoRowsRemovesEveryPanelThrottle(t *testing.T) {
	r, _, cl := throttleRecForTest(t)
	cl.stalwart = []mailthrottle.ListItem{
		{StalwartID: "j1", Description: "jabali domain example.com: 5 per day"},
		{StalwartID: "op1", Description: "operator cap"},
	}
	r.reconcileMailThrottles(context.Background())
	assert.Equal(t, []string{"j1"}, cl.deletes)
}
