package reconciler

import (
	"context"
	"sync"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeAgent mocks the agent.AgentInterface for testing.
type fakeAgent struct {
	mu         sync.Mutex // JAB-205: ReconcileAll now calls the agent from a worker pool
	calls      []fakeCall
	failMethod string // if set, Call returns an error for this method
}

type fakeCall struct {
	method string
	params interface{}
}

func (f *fakeAgent) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{method, params})
	f.mu.Unlock()

	if method == f.failMethod {
		return nil, fmt.Errorf("agent call failed for method: %s", method)
	}

	switch method {
	case "domain.list":
		return json.Marshal(map[string][]string{
			"sites": {"example.com", "foo.bar.com"},
		})
	case "domain.create":
		return json.Marshal(map[string]string{"domain": "", "status": "created"})
	case "sharedresource.apply":
		return json.Marshal(map[string]string{"host_account_id": "hostAcct1"})
	case "file.share_set":
		return json.Marshal(map[string]any{"ok": true, "node_id": "node1"})
	default:
		return nil, nil
	}
}

// fakeDomainRepo mocks the domain repository.
type fakeDomainRepo struct {
	domains map[string]*models.Domain
}

func (f *fakeDomainRepo) Create(ctx context.Context, d *models.Domain) error {
	f.domains[d.ID] = d
	return nil
}

func (f *fakeDomainRepo) FindByIDs(_ context.Context, ids []string) ([]models.Domain, error) {
	var out []models.Domain
	for _, id := range ids {
		if d, ok := f.domains[id]; ok {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (f *fakeDomainRepo) FindByID(ctx context.Context, id string) (*models.Domain, error) {
	d, ok := f.domains[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return d, nil
}

func (f *fakeDomainRepo) FindByName(ctx context.Context, name string) (*models.Domain, error) {
	for _, d := range f.domains {
		if d.Name == name {
			return d, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeDomainRepo) List(ctx context.Context, opts repository.ListOptions) ([]models.Domain, int64, error) {
	var result []models.Domain
	for _, d := range f.domains {
		result = append(result, *d)
	}
	return result, int64(len(result)), nil
}

func (f *fakeDomainRepo) ListByUserID(ctx context.Context, userID string, opts repository.ListOptions) ([]models.Domain, int64, error) {
	var result []models.Domain
	for _, d := range f.domains {
		if d.UserID == userID {
			result = append(result, *d)
		}
	}
	return result, int64(len(result)), nil
}

func (f *fakeDomainRepo) Update(ctx context.Context, d *models.Domain) error {
	f.domains[d.ID] = d
	return nil
}

func (f *fakeDomainRepo) UpdateCacheTTL(_ context.Context, _ string, _ int) error {
	return nil
}

func (f *fakeDomainRepo) UpdateCacheQueryAllowlist(_ context.Context, _, _ string) error {
	return nil
}

func (f *fakeDomainRepo) BulkSetEnabledByUserID(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}

func (f *fakeDomainRepo) Delete(ctx context.Context, id string) error {
	delete(f.domains, id)
	return nil
}

func (f *fakeDomainRepo) ListForRegistrarRefresh(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error) {
	return nil, nil
}
func (f *fakeDomainRepo) SetRegistrarExpiry(ctx context.Context, id string, expiresAt *time.Time, checkedAt time.Time) error {
	return nil
}
func (f *fakeDomainRepo) CountByUserID(ctx context.Context, userID string) (int64, error) {
	count := 0
	for _, d := range f.domains {
		if d.UserID == userID {
			count++
		}
	}
	return int64(count), nil
}

func (f *fakeDomainRepo) SetPHPPoolID(ctx context.Context, id string, poolID *string) error {
	return nil
}

func (f *fakeDomainRepo) CountByPHPPoolID(ctx context.Context, poolID string) (int64, error) {
	count := 0
	for _, d := range f.domains {
		if d.PHPPoolID != nil && *d.PHPPoolID == poolID {
			count++
		}
	}
	return int64(count), nil
}

func (f *fakeDomainRepo) UpdatePHPSettings(ctx context.Context, id string, settings repository.DomainPHPSettings) error {
	for i, d := range f.domains {
		if d.ID == id {
			f.domains[i].PHPMemoryLimit = settings.MemoryLimit
			f.domains[i].PHPUploadMaxFilesize = settings.UploadMaxFilesize
			f.domains[i].PHPPostMaxSize = settings.PostMaxSize
			f.domains[i].PHPMaxInputVars = settings.MaxInputVars
			f.domains[i].PHPMaxExecutionTime = settings.MaxExecutionTime
			f.domains[i].PHPMaxInputTime = settings.MaxInputTime
			return nil
		}
	}
	return &notFoundErr{}
}

func (f *fakeDomainRepo) UpdateMailProvider(_ context.Context, _ string, _ repository.DomainMailProvider) error {
	return nil
}

func (f *fakeDomainRepo) UpdateSSLMode(context.Context, string, string) error { return nil }
func (f *fakeDomainRepo) SetSharedCertificate(context.Context, string, *string, string) error {
	return nil
}

func (f *fakeDomainRepo) UpdateEmailState(ctx context.Context, id string, state repository.DomainEmailState) error {
	for i, d := range f.domains {
		if d.ID == id {
			f.domains[i].EmailEnabled = state.Enabled
			f.domains[i].EmailEnabledAt = state.EmailEnabledAt
			if state.DkimSelector != nil {
				f.domains[i].DkimSelector = state.DkimSelector
			}
			if state.DkimPublicKey != nil {
				f.domains[i].DkimPublicKey = state.DkimPublicKey
			}
			return nil
		}
	}
	return &notFoundErr{}
}

func (f *fakeDomainRepo) FindPanelPrimary(ctx context.Context) (*models.Domain, error) {
	for _, d := range f.domains {
		if d.IsPanelPrimary {
			return d, nil
		}
	}
	return nil, repository.ErrPanelPrimaryNotFound
}

func (f *fakeDomainRepo) MarkPanelPrimary(ctx context.Context, id string) error {
	target, ok := f.domains[id]
	if !ok {
		return repository.ErrNotFound
	}
	for otherID, d := range f.domains {
		if otherID == id {
			continue
		}
		d.IsPanelPrimary = false
	}
	target.IsPanelPrimary = true
	return nil
}

func (f *fakeDomainRepo) SetListenIPs(ctx context.Context, id string, upd repository.DomainListenIPs) error {
	d, ok := f.domains[id]
	if !ok {
		return &notFoundErr{}
	}
	if upd.ChangeIPv4 {
		d.ListenIPv4ID = upd.IPv4ID
	}
	if upd.ChangeIPv6 {
		d.ListenIPv6ID = upd.IPv6ID
	}
	return nil
}

func (f *fakeDomainRepo) UpdateCatchallTarget(ctx context.Context, id string, target *string) error {
	d, ok := f.domains[id]
	if !ok {
		return &notFoundErr{}
	}
	d.CatchallTarget = target
	return nil
}

func (f *fakeDomainRepo) UpdateDisclaimer(ctx context.Context, id string, enabled bool, text *string) error {
	d, ok := f.domains[id]
	if !ok {
		return &notFoundErr{}
	}
	d.DisclaimerEnabled = enabled
	d.DisclaimerText = text
	return nil
}

func (f *fakeDomainRepo) UpdateCacheEnabled(ctx context.Context, id string, enabled bool) error {
	return nil
}

func (f *fakeDomainRepo) UpdateCachePath(_ context.Context, _, _ string) error { return nil }

func (f *fakeDomainRepo) UpdateSkipAutoSAN(ctx context.Context, id string, enabled bool) error {
	return nil
}

func (f *fakeDomainRepo) UpdateMTASTSEnabled(context.Context, string, bool) (uint64, error) {
	return 0, nil
}

func (f *fakeDomainRepo) UpdateMTASTSAppliedID(ctx context.Context, id string, appliedID uint64) error {
	d, ok := f.domains[id]
	if !ok {
		return &notFoundErr{}
	}
	d.MTASTSAppliedId = appliedID
	return nil
}

func (f *fakeDomainRepo) UpdatePHPPoolID(ctx context.Context, id string, poolID *string) error {
	d, ok := f.domains[id]
	if !ok {
		return &notFoundErr{}
	}
	d.PHPPoolID = poolID
	return nil
}

func (f *fakeDomainRepo) UpdateDNSSECEnabled(ctx context.Context, id string, enabled bool) error {
	d, ok := f.domains[id]
	if !ok {
		return &notFoundErr{}
	}
	d.DNSSECEnabled = enabled
	return nil
}

func (f *fakeDomainRepo) UpdateGhostState(ctx context.Context, id, state string, checkedAt time.Time, detail *string) error {
	d, ok := f.domains[id]
	if !ok {
		return &notFoundErr{}
	}
	d.GhostState = state
	t := checkedAt
	d.GhostCheckedAt = &t
	d.GhostDetail = detail
	return nil
}

func (f *fakeDomainRepo) ListForGhostCheck(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error) {
	return nil, nil
}

type notFoundErr struct{}

func (e *notFoundErr) Error() string { return "not found" }
func (e *notFoundErr) Is(err error) bool {
	_, ok := err.(*notFoundErr)
	return ok
}

// fakeDNSZoneRepo mocks the DNS zone repository.
type fakeDNSZoneRepo struct {
	zones map[string]*models.DNSZone
}

func (f *fakeDNSZoneRepo) Create(ctx context.Context, zone *models.DNSZone) error {
	f.zones[zone.ID] = zone
	return nil
}

func (f *fakeDNSZoneRepo) FindByID(ctx context.Context, id string) (*models.DNSZone, error) {
	z, ok := f.zones[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return z, nil
}

func (f *fakeDNSZoneRepo) FindByName(ctx context.Context, name string) (*models.DNSZone, error) {
	for _, z := range f.zones {
		if z.Name == name {
			return z, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeDNSZoneRepo) FindByDomainID(ctx context.Context, domainID string) (*models.DNSZone, error) {
	for _, z := range f.zones {
		if z.DomainID == domainID {
			return z, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeDNSZoneRepo) ListAll(ctx context.Context) ([]models.DNSZone, error) {
	var result []models.DNSZone
	for _, z := range f.zones {
		result = append(result, *z)
	}
	return result, nil
}

func (f *fakeDNSZoneRepo) Update(ctx context.Context, zone *models.DNSZone) error {
	f.zones[zone.ID] = zone
	return nil
}

func (f *fakeDNSZoneRepo) Delete(ctx context.Context, id string) error {
	delete(f.zones, id)
	return nil
}

// fakeDNSRecordRepo mocks the DNS record repository.
type fakeDNSRecordRepo struct {
	records map[string]*models.DNSRecord
}

func (f *fakeDNSRecordRepo) Create(ctx context.Context, record *models.DNSRecord) error {
	f.records[record.ID] = record
	return nil
}

func (f *fakeDNSRecordRepo) FindByID(ctx context.Context, id string) (*models.DNSRecord, error) {
	r, ok := f.records[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return r, nil
}

func (f *fakeDNSRecordRepo) ListByZoneID(ctx context.Context, zoneID string) ([]models.DNSRecord, error) {
	var result []models.DNSRecord
	for _, r := range f.records {
		if r.ZoneID == zoneID {
			result = append(result, *r)
		}
	}
	return result, nil
}

func (f *fakeDNSRecordRepo) DeleteByZoneID(ctx context.Context, zoneID string) error {
	for id, r := range f.records {
		if r.ZoneID == zoneID {
			delete(f.records, id)
		}
	}
	return nil
}

func (f *fakeDNSRecordRepo) DeleteByZoneIDAndManagedBy(ctx context.Context, zoneID, managedBy string) error {
	for id, r := range f.records {
		if r.ZoneID != zoneID {
			continue
		}
		if r.ManagedBy == nil || *r.ManagedBy != managedBy {
			continue
		}
		delete(f.records, id)
	}
	return nil
}

func (f *fakeDNSRecordRepo) Update(ctx context.Context, record *models.DNSRecord) error {
	f.records[record.ID] = record
	return nil
}

func (f *fakeDNSRecordRepo) Delete(ctx context.Context, id string) error {
	delete(f.records, id)
	return nil
}

// fakeServerSettingsRepo mocks the server settings repository.
type fakeServerSettingsRepo struct {
	settings *models.ServerSettings
}

func (f *fakeServerSettingsRepo) SetDigestLastSent(context.Context, string) error { return nil }

func (f *fakeServerSettingsRepo) Get(ctx context.Context) (*models.ServerSettings, error) {
	if f.settings == nil {
		return nil, repository.ErrNotFound
	}
	return f.settings, nil
}

func (f *fakeServerSettingsRepo) Upsert(ctx context.Context, settings *models.ServerSettings) error {
	f.settings = settings
	return nil
}

// EnsureVAPID is a no-op for reconciler tests — the reconciler path
// doesn't touch VAPID, this stub only exists so the fake satisfies
// the repository.ServerSettingsRepository interface (ADR-0057).
func (f *fakeServerSettingsRepo) EnsureVAPID(ctx context.Context, hostname string) (bool, error) {
	return false, nil
}

// fakeUserRepo mocks the user repository.
type fakeUserRepo struct {
	users map[string]*models.User
}

func (f *fakeUserRepo) Create(ctx context.Context, u *models.User) error {
	f.users[u.ID] = u
	return nil
}

func (f *fakeUserRepo) FindByID(ctx context.Context, id string) (*models.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

func (f *fakeUserRepo) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	for _, u := range f.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeUserRepo) FindByKratosIdentityID(_ context.Context, _ string) (*models.User, error) {
	return nil, repository.ErrNotFound
}

func (f *fakeUserRepo) FindByIDs(_ context.Context, _ []string) ([]models.User, error) {
	return nil, nil
}

func (f *fakeUserRepo) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	for _, u := range f.users {
		if u.Username != nil && *u.Username == username {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (f *fakeUserRepo) List(ctx context.Context, opts repository.ListOptions) ([]models.User, int64, error) {
	var result []models.User
	for _, u := range f.users {
		result = append(result, *u)
	}
	total := int64(len(result))
	// Respect the limit option
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, total, nil
}
func (f *fakeUserRepo) Update(ctx context.Context, u *models.User) error {
	f.users[u.ID] = u
	return nil
}

func (f *fakeUserRepo) UpdateCLIPHPVersion(context.Context, string, *string) error { return nil }

func (f *fakeUserRepo) LinkKratosIdentity(ctx context.Context, userID, kratosID string) error {
	u, ok := f.users[userID]
	if !ok {
		return repository.ErrNotFound
	}
	u.KratosIdentityID = &kratosID
	return nil
}

func (f *fakeUserRepo) SetSuspended(_ context.Context, _ string, _ bool, _ string) error { return nil }

func (f *fakeUserRepo) Delete(ctx context.Context, id string) error {
	delete(f.users, id)
	return nil
}

func (f *fakeUserRepo) SetAdmin(ctx context.Context, id string, isAdmin bool) error {
	if u, ok := f.users[id]; ok {
		u.IsAdmin = isAdmin
	}
	return nil
}

func (f *fakeUserRepo) CountAdmins(ctx context.Context) (int64, error) {
	var n int64
	for _, u := range f.users {
		if u.IsAdmin {
			n++
		}
	}
	return n, nil
}

func (f *fakeUserRepo) FindAdminsByEmail(ctx context.Context) ([]*models.User, error) {
	var admins []*models.User
	for _, u := range f.users {
		if u.IsAdmin {
			u := u
			admins = append(admins, u)
		}
	}
	return admins, nil
}

func (f *fakeUserRepo) SetTOTPSecret(ctx context.Context, id string, encrypted []byte) error {
	return nil
}
func (f *fakeUserRepo) EnableTOTP(ctx context.Context, id string, now time.Time) error { return nil }
func (f *fakeUserRepo) DisableTOTP(ctx context.Context, id string) error               { return nil }

// fakePHPPoolRepo mocks the PHP pool repository.
type fakePHPPoolRepo struct {
	pools map[string]*models.PHPPool
}

func (f *fakePHPPoolRepo) FindByID(ctx context.Context, id string) (*models.PHPPool, error) {
	p, ok := f.pools[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return p, nil
}

func (f *fakePHPPoolRepo) Create(ctx context.Context, p *models.PHPPool) error {
	f.pools[p.ID] = p
	return nil
}

func (f *fakePHPPoolRepo) Update(ctx context.Context, p *models.PHPPool) error {
	f.pools[p.ID] = p
	return nil
}

func (f *fakePHPPoolRepo) Delete(ctx context.Context, id string) error {
	delete(f.pools, id)
	return nil
}

func (f *fakePHPPoolRepo) FindByUserID(ctx context.Context, userID string) (*models.PHPPool, error) {
	for _, p := range f.pools {
		if p.UserID == userID {
			return p, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakePHPPoolRepo) FindByUserAndVersion(ctx context.Context, userID, phpVersion string) (*models.PHPPool, error) {
	for _, p := range f.pools {
		if p.UserID == userID && p.PHPVersion == phpVersion {
			return p, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakePHPPoolRepo) ListByUserID(ctx context.Context, userID string) ([]models.PHPPool, error) {
	var result []models.PHPPool
	for _, p := range f.pools {
		if p.UserID == userID {
			result = append(result, *p)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (f *fakePHPPoolRepo) ListAll(ctx context.Context, opts repository.ListOptions) ([]models.PHPPool, int64, error) {
	var result []models.PHPPool
	for _, p := range f.pools {
		result = append(result, *p)
	}
	return result, int64(len(result)), nil
}

func (f *fakePHPPoolRepo) SetStatus(ctx context.Context, id string, status string, lastErr *string) error {
	if p, ok := f.pools[id]; ok {
		p.Status = status
		p.LastError = lastErr
	}
	return nil
}

// filterCallsByPrefix returns only calls whose method starts with the given prefix.
// Useful when a reconcile tick dispatches to multiple subsystems and a test
// only cares about one of them.
func filterCallsByPrefix(calls []fakeCall, prefix string) []fakeCall {
	var result []fakeCall
	for _, call := range calls {
		if strings.HasPrefix(call.method, prefix) {
			result = append(result, call)
		}
	}
	return result
}

func TestReconcileAll_EnabledDomainMissing(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	// Setup: one enabled domain in DB, but missing from agent
	now := time.Now().UTC()
	username := "alice"
	user := &models.User{
		ID:       "user-1",
		Email:    "alice@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	domain := &models.Domain{
		ID:        "domain-1",
		UserID:    user.ID,
		Name:      "missing.com",
		DocRoot:   "/home/alice/domains/missing.com/public_html",
		IsEnabled: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})

	err := r.ReconcileAll(ctx)
	require.NoError(t, err)

	// Verify that domain.create was called
	domainCalls := filterCallsByPrefix(agent.calls, "domain.")
	require.Len(t, domainCalls, 2) // domain.list + domain.create
	require.Equal(t, "domain.list", domainCalls[0].method)
	require.Equal(t, "domain.create", domainCalls[1].method)
}

func TestReconcileAll_DisabledDomainPresent(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	// Setup: one disabled domain in DB, but present on agent
	now := time.Now().UTC()
	username := "bob"
	user := &models.User{
		ID:       "user-1",
		Email:    "bob@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	domain := &models.Domain{
		ID:        "domain-2",
		UserID:    user.ID,
		Name:      "example.com",
		DocRoot:   "/home/bob/domains/example.com/public_html",
		IsEnabled: false,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})

	err := r.ReconcileAll(ctx)
	require.NoError(t, err)

	// Verify that domain.create was called
	domainCalls := filterCallsByPrefix(agent.calls, "domain.")
	require.Len(t, domainCalls, 2) // domain.list + domain.create
	require.Equal(t, "domain.list", domainCalls[0].method)
	require.Equal(t, "domain.create", domainCalls[1].method)
}

func TestReconcileAll_OrphanLogsWarning(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	// agent returns "orphan.com" which doesn't exist in DB
	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})

	err := r.ReconcileAll(ctx)
	require.NoError(t, err)

	// Verify that domain.list was called but no creates/disables
	domainCalls := filterCallsByPrefix(agent.calls, "domain.")
	require.Len(t, domainCalls, 1)
	require.Equal(t, "domain.list", domainCalls[0].method)
}

func TestReconcileAll_DomainWithPHPPool(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: one user with a domain that has a PHP pool
	now := time.Now().UTC()
	username := "phpuser"
	user := &models.User{
		ID:       "user-1",
		Email:    "phpuser@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	// Create a PHP pool with version 8.2
	phpPoolID := "pool-1"
	phpPool := &models.PHPPool{
		ID:         phpPoolID,
		PHPVersion: "8.2",
	}
	phpPoolRepo.pools[phpPoolID] = phpPool

	// Create a domain with a reference to the PHP pool
	domain := &models.Domain{
		ID:        "domain-1",
		UserID:    user.ID,
		Name:      "phpsite.com",
		DocRoot:   "/home/phpuser/domains/phpsite.com/public_html",
		IsEnabled: true,
		PHPPoolID: &phpPoolID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	err := r.ReconcileAll(ctx)
	require.NoError(t, err)

	// Verify that required calls were made.
	// createDomainOnAgent runs for every enabled domain on every reconcile pass
	// (the agent's writeVhost is content-hash gated, so the no-change case is cheap).
	allCalls := filterCallsByPrefix(agent.calls, "")
	var phpcalls, domainCalls []fakeCall
	for _, call := range allCalls {
		if call.method == "user.slice.ensure" || call.method == "php.pool.apply" {
			phpcalls = append(phpcalls, call)
		} else if call.method == "domain.list" || call.method == "domain.create" {
			domainCalls = append(domainCalls, call)
		}
	}

	require.GreaterOrEqual(t, len(phpcalls), 1, "should call user.slice.ensure and/or php.pool.apply")
	require.Len(t, domainCalls, 2, "should call domain.list and domain.create")

	// Verify that domain.create was called with correct PHP params
	var domainCreateCall *fakeCall
	for _, call := range agent.calls {
		if call.method == "domain.create" {
			domainCreateCall = &call
			break
		}
	}
	require.NotNil(t, domainCreateCall, "domain.create should be called")
	params := domainCreateCall.params.(map[string]any)
	require.Equal(t, true, params["has_php"], "has_php should be true")
	require.Equal(t, "8.2", params["php_version"], "php_version should be 8.2")

	// fs.write_healthcheck was removed 2026-04-21 — the jabali-healthcheck.php
	// file was only consumed by the one-shot per-user-slices cutover (shipped
	// 2026-04-18). Regression guard: ensure no healthcheck RPC fires.
	for _, call := range agent.calls {
		require.NotEqual(t, "fs.write_healthcheck", call.method, "healthcheck RPC should not be called — removed 2026-04-21")
	}
}

func TestReconcileAll_DomainWithPHPSettingsOverrides(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: domain with PHP pool and per-domain INI overrides
	now := time.Now().UTC()
	username := "phpuser"
	user := &models.User{
		ID:       "user-1",
		Email:    "phpuser@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	phpPoolID := "pool-1"
	phpPool := &models.PHPPool{
		ID:         phpPoolID,
		PHPVersion: "8.5",
	}
	phpPoolRepo.pools[phpPoolID] = phpPool

	// Domain with overrides
	mem := "256M"
	upload := "128M"
	post := "64M"
	inputVars := 10000
	execTime := 300
	inputTime := 60

	domain := &models.Domain{
		ID:                   "domain-1",
		UserID:               user.ID,
		Name:                 "phpsite.com",
		DocRoot:              "/home/phpuser/domains/phpsite.com/public_html",
		IsEnabled:            true,
		PHPPoolID:            &phpPoolID,
		PHPMemoryLimit:       &mem,
		PHPUploadMaxFilesize: &upload,
		PHPPostMaxSize:       &post,
		PHPMaxInputVars:      &inputVars,
		PHPMaxExecutionTime:  &execTime,
		PHPMaxInputTime:      &inputTime,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	err := r.ReconcileAll(ctx)
	require.NoError(t, err)

	// Verify domain.create was called
	domainCreateCalls := filterCallsByPrefix(agent.calls, "domain.create")
	require.Len(t, domainCreateCalls, 1, "should call domain.create exactly once")

	// Verify PHP settings were passed through
	params := domainCreateCalls[0].params.(map[string]any)
	require.Equal(t, "256M", params["php_memory_limit"])
	require.Equal(t, "128M", params["php_upload_max_filesize"])
	require.Equal(t, "64M", params["php_post_max_size"])
	require.Equal(t, 10000, params["php_max_input_vars"])
	require.Equal(t, 300, params["php_max_execution_time"])
	require.Equal(t, 60, params["php_max_input_time"])
}

func TestReconcileAll_DomainWithoutPHPSettingsOverrides(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: domain with PHP pool but NO per-domain overrides
	now := time.Now().UTC()
	username := "phpuser"
	user := &models.User{
		ID:       "user-1",
		Email:    "phpuser@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	phpPoolID := "pool-1"
	phpPool := &models.PHPPool{
		ID:         phpPoolID,
		PHPVersion: "8.5",
	}
	phpPoolRepo.pools[phpPoolID] = phpPool

	// Domain without overrides (all nil)
	domain := &models.Domain{
		ID:        "domain-1",
		UserID:    user.ID,
		Name:      "phpsite.com",
		DocRoot:   "/home/phpuser/domains/phpsite.com/public_html",
		IsEnabled: true,
		PHPPoolID: &phpPoolID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	err := r.ReconcileAll(ctx)
	require.NoError(t, err)

	// Verify domain.create was called and NO overrides were passed
	domainCreateCalls := filterCallsByPrefix(agent.calls, "domain.create")
	require.Len(t, domainCreateCalls, 1, "should call domain.create exactly once")

	params := domainCreateCalls[0].params.(map[string]any)
	// When all are nil, they should not be in the params map
	_, hasMemLimit := params["php_memory_limit"]
	_, hasUpload := params["php_upload_max_filesize"]
	_, hasPost := params["php_post_max_size"]
	_, hasVars := params["php_max_input_vars"]
	_, hasExecTime := params["php_max_execution_time"]
	_, hasInputTime := params["php_max_input_time"]

	require.False(t, hasMemLimit, "php_memory_limit should not be present when nil")
	require.False(t, hasUpload, "php_upload_max_filesize should not be present when nil")
	require.False(t, hasPost, "php_post_max_size should not be present when nil")
	require.False(t, hasVars, "php_max_input_vars should not be present when nil")
	require.False(t, hasExecTime, "php_max_execution_time should not be present when nil")
	require.False(t, hasInputTime, "php_max_input_time should not be present when nil")
}

func TestReconcileOne_DomainFound(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	now := time.Now().UTC()
	username := "charlie"
	user := &models.User{
		ID:       "user-1",
		Email:    "charlie@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	domain := &models.Domain{
		ID:        "domain-3",
		UserID:    user.ID,
		Name:      "test.com",
		DocRoot:   "/home/charlie/domains/test.com/public_html",
		IsEnabled: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})

	err := r.ReconcileOne(ctx, domain.ID)
	require.NoError(t, err)

	// ReconcileOne also converges the rate-limit zone fragment (see
	// reconciler.go; fixes nginx -t ordering when rate_limit_rps>0).
	// Assert on the domain.create-specific subset so this test stays
	// focused on the per-domain call shape.
	domainCalls := filterCallsByPrefix(agent.calls, "domain.create")
	require.Len(t, domainCalls, 1)
	require.Equal(t, "domain.create", domainCalls[0].method)
}

func TestReconcileOne_DomainNotFound(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})

	// Non-existent domain ID
	err := r.ReconcileOne(ctx, "nonexistent")
	require.NoError(t, err)

	// Should not call agent since we don't know the domain name
	require.Len(t, agent.calls, 0)
}

func TestReconcileOne_PassesCustomDirectives(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	now := time.Now().UTC()
	username := "bob"
	user := &models.User{
		ID:       "user-2",
		Email:    "bob@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	customDirectives := "add_header X-Foo bar;"
	domain := &models.Domain{
		ID:                    "domain-4",
		UserID:                user.ID,
		Name:                  "test2.com",
		DocRoot:               "/home/bob/domains/test2.com/public_html",
		IsEnabled:             true,
		NginxCustomDirectives: &customDirectives,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})

	err := r.ReconcileOne(ctx, domain.ID)
	require.NoError(t, err)

	// Filter to domain.create — ReconcileOne also pushes the rate-limit
	// zone fragment (empty when no domain opts in).
	domainCalls := filterCallsByPrefix(agent.calls, "domain.create")
	require.Len(t, domainCalls, 1)
	require.Equal(t, "domain.create", domainCalls[0].method)

	// Verify that custom_directives was passed in params
	params := domainCalls[0].params.(map[string]any)
	require.Equal(t, customDirectives, params["custom_directives"])
}

func TestReconcileAllForce_RerendersEveryDomain(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	// Setup: one user with multiple domains (enabled and disabled)
	now := time.Now().UTC()
	username := "testuser"
	user := &models.User{
		ID:       "user-1",
		Email:    "test@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	domain1 := &models.Domain{
		ID:        "domain-1",
		UserID:    user.ID,
		Name:      "enabled.com",
		DocRoot:   "/home/testuser/domains/enabled.com/public_html",
		IsEnabled: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domain2 := &models.Domain{
		ID:        "domain-2",
		UserID:    user.ID,
		Name:      "disabled.com",
		DocRoot:   "/home/testuser/domains/disabled.com/public_html",
		IsEnabled: false,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domain3 := &models.Domain{
		ID:        "domain-3",
		UserID:    user.ID,
		Name:      "another.com",
		DocRoot:   "/home/testuser/domains/another.com/public_html",
		IsEnabled: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[domain1.ID] = domain1
	domainRepo.domains[domain2.ID] = domain2
	domainRepo.domains[domain3.ID] = domain3

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})

	// Run ReconcileAllForce
	err := r.ReconcileAllForce(ctx)
	require.NoError(t, err)

	// ReconcileAllForce also converges the rate-limit zone fragment once
	// at the top (ordering rule — see reconciler.go). Filter to the
	// domain.create subset for assertions.
	domainCalls := filterCallsByPrefix(agent.calls, "domain.create")
	require.Len(t, domainCalls, 3)
	for i := 0; i < 3; i++ {
		require.Equal(t, "domain.create", domainCalls[i].method)
	}

	// Verify all three domains appear in the calls
	domainNames := make(map[string]bool)
	for _, call := range domainCalls {
		params := call.params.(map[string]any)
		domainNames[params["domain"].(string)] = true
	}
	require.True(t, domainNames["enabled.com"])
	require.True(t, domainNames["disabled.com"])
	require.True(t, domainNames["another.com"])
}

// TestReconcileOne_RateLimitZoneFragmentPrecedesDomainCreate asserts the
// ordering invariant that fixes the nginx "zero size shared memory zone"
// abort: nginx.ratelimits.apply (declares zones) must land on the wire
// BEFORE domain.create (writes vhost that references the zone). Regression
// guard — without this, a 0→N rate_limit_rps change fails nginx -t on the
// agent and the domain never lands.
func TestReconcileOne_RateLimitZoneFragmentPrecedesDomainCreate(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	now := time.Now().UTC()
	username := "alice"
	user := &models.User{
		ID:       "user-rl",
		Email:    "alice@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user
	domain := &models.Domain{
		ID:           "domain-rl",
		UserID:       user.ID,
		Name:         "rl.example.com",
		DocRoot:      "/home/alice/domains/rl.example.com/public_html",
		IsEnabled:    true,
		RateLimitRPS: 100, // trigger zone emission
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	domainRepo.domains[domain.ID] = domain

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})
	require.NoError(t, r.ReconcileOne(ctx, domain.ID))

	// Find the index of each method in the recorded call order.
	rlIdx, createIdx := -1, -1
	for i, c := range agent.calls {
		if rlIdx == -1 && c.method == "nginx.ratelimits.apply" {
			rlIdx = i
		}
		if createIdx == -1 && c.method == "domain.create" {
			createIdx = i
		}
	}
	require.NotEqual(t, -1, rlIdx, "nginx.ratelimits.apply must be called")
	require.NotEqual(t, -1, createIdx, "domain.create must be called")
	require.Less(t, rlIdx, createIdx, "nginx.ratelimits.apply must precede domain.create — zone must be declared before vhost references it")
}

func TestSchedule_NonBlocking(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second, QueueLen: 2})

	// Schedule should not block
	r.Schedule("domain-1")
	r.Schedule("domain-2")
	r.Schedule("domain-3") // Should drop silently
}

func TestLinuxUserFromEmail(t *testing.T) {
	tests := []struct {
		email    string
		expected string
	}{
		{"alice@example.com", "alice"},
		{"bob.smith@company.org", "bob.smith"},
		{"user+tag@domain.io", "user+tag"},
		{"simple", "simple"}, // no @ sign
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			result := linuxUserFromEmail(tt.email)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestReconcile_BootstrapsAndPushesZone(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	dnsZoneRepo := &fakeDNSZoneRepo{zones: make(map[string]*models.DNSZone)}
	dnsRecordRepo := &fakeDNSRecordRepo{records: make(map[string]*models.DNSRecord)}
	serverSettingsRepo := &fakeServerSettingsRepo{
		settings: &models.ServerSettings{
			PublicIPv4: "192.0.2.1",
			PublicIPv6: "2001:db8::1",
			NS1Name:    "ns1.example.com",
			NS2Name:    "ns2.example.com",
			AdminEmail: "admin@example.com",
		},
	}

	// Setup: one enabled domain in DB
	now := time.Now().UTC()
	username := "alice"
	user := &models.User{
		ID:       "user-1",
		Email:    "alice@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	domain := &models.Domain{
		ID:        "domain-1",
		UserID:    user.ID,
		Name:      "example.com",
		DocRoot:   "/home/alice/domains/example.com/public_html",
		IsEnabled: true,
		CreateWWW: true, // GH #225: www is opt-in now; this test covers the with-www path (8 records)
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[domain.ID] = domain

	// Create reconciler with DNS repos wired
	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithDNSRepos(dnsZoneRepo, dnsRecordRepo, serverSettingsRepo)

	// Run ReconcileOne on the domain
	err := r.ReconcileOne(ctx, domain.ID)
	require.NoError(t, err)

	// Verify that domain.create was called
	domainCreateFound := false
	dnsZoneUpsertFound := false

	for _, call := range agent.calls {
		if call.method == "domain.create" {
			domainCreateFound = true
		}
		if call.method == "dns.zone.upsert" {
			dnsZoneUpsertFound = true
			params := call.params.(map[string]any)
			require.Equal(t, "example.com", params["zone"])
			// Records should be a slice of compiled DNS records
			require.NotNil(t, params["records"], "expected records in dns.zone.upsert call")
		}
	}

	require.True(t, domainCreateFound, "domain.create should have been called")
	require.True(t, dnsZoneUpsertFound, "dns.zone.upsert should have been called")

	// Verify that a DNS zone was created in the zone repo
	zone, err := dnsZoneRepo.FindByDomainID(ctx, domain.ID)
	require.NoError(t, err)
	require.NotNil(t, zone)
	require.Equal(t, domain.Name, zone.Name)
	require.Equal(t, domain.ID, zone.DomainID)
	require.True(t, zone.IsEnabled)

	// Verify that bootstrap records were created
	records, err := dnsRecordRepo.ListByZoneID(ctx, zone.ID)
	require.NoError(t, err)
	// Bootstrap: A/@, A/mail, AAAA/@, AAAA/mail, CNAME/www (opt-in via
	// CreateWWW, GH #225), MX, SPF, DMARC = 8 records. mail stays A because
	// MX targets can't be CNAMEs (RFC 2181 §10.3).
	require.Len(t, records, 8)
}

func TestReconcile_PassesAXFRToAgent(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	dnsZoneRepo := &fakeDNSZoneRepo{zones: make(map[string]*models.DNSZone)}
	dnsRecordRepo := &fakeDNSRecordRepo{records: make(map[string]*models.DNSRecord)}
	serverSettingsRepo := &fakeServerSettingsRepo{
		settings: &models.ServerSettings{
			PublicIPv4: "192.0.2.1",
			PublicIPv6: "2001:db8::1",
			NS1Name:    "ns1.example.com",
			NS2Name:    "ns2.example.com",
			NS2IPv4:    "198.51.100.7", // Secondary nameserver configured
			AdminEmail: "admin@example.com",
		},
	}

	// Setup: one enabled domain in DB
	now := time.Now().UTC()
	username := "alice"
	user := &models.User{
		ID:       "user-1",
		Email:    "alice@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	domain := &models.Domain{
		ID:        "domain-1",
		UserID:    user.ID,
		Name:      "example.com",
		DocRoot:   "/home/alice/domains/example.com/public_html",
		IsEnabled: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[domain.ID] = domain

	// Create reconciler with DNS repos wired
	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithDNSRepos(dnsZoneRepo, dnsRecordRepo, serverSettingsRepo)

	// Run ReconcileOne on the domain
	err := r.ReconcileOne(ctx, domain.ID)
	require.NoError(t, err)

	// Find the dns.zone.upsert call
	var zoneUpsertCall *fakeCall
	for i := range agent.calls {
		if agent.calls[i].method == "dns.zone.upsert" {
			zoneUpsertCall = &agent.calls[i]
			break
		}
	}

	require.NotNil(t, zoneUpsertCall, "dns.zone.upsert should have been called")

	params, ok := zoneUpsertCall.params.(map[string]any)
	require.True(t, ok, "params should be map[string]any, got %T", zoneUpsertCall.params)

	// Verify allow_axfr_from contains ns2's IPv4 + localhost
	allowAXFRRaw, ok := params["allow_axfr_from"]
	require.True(t, ok, "allow_axfr_from should be present in params")

	var allowAXFR []interface{}
	switch v := allowAXFRRaw.(type) {
	case []interface{}:
		allowAXFR = v
	case []string:
		for _, s := range v {
			allowAXFR = append(allowAXFR, s)
		}
	default:
		t.Fatalf("allow_axfr_from has unexpected type: %T", allowAXFRRaw)
	}
	require.True(t, ok, "allow_axfr_from should be a slice")
	require.Len(t, allowAXFR, 2, "should have 2 entries: ns2 IPv4 and localhost")

	// Check for ns2's IPv4 and localhost in the allow list
	foundNS2 := false
	foundLocal := false
	for _, entry := range allowAXFR {
		if str, ok := entry.(string); ok {
			if str == "198.51.100.7" {
				foundNS2 = true
			}
			if str == "127.0.0.1" {
				foundLocal = true
			}
		}
	}
	require.True(t, foundNS2, "allow_axfr_from should contain ns2 IPv4 (198.51.100.7)")
	require.True(t, foundLocal, "allow_axfr_from should contain localhost (127.0.0.1)")

	// Verify also_notify contains ns2's IPv4
	alsoNotifyRaw, ok := params["also_notify"]
	require.True(t, ok, "also_notify should be present in params")

	var alsoNotify []interface{}
	switch v := alsoNotifyRaw.(type) {
	case []interface{}:
		alsoNotify = v
	case []string:
		for _, s := range v {
			alsoNotify = append(alsoNotify, s)
		}
	default:
		t.Fatalf("also_notify has unexpected type: %T", alsoNotifyRaw)
	}

	require.Len(t, alsoNotify, 1, "should have 1 entry: ns2 IPv4")
	require.Equal(t, "198.51.100.7", alsoNotify[0])
}

// === PHP Pool Reconciliation Tests ===

func TestReconcilePHPPools_CreateDefaultPool(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: user with no pool
	username := "newuser"
	user := &models.User{
		ID:       "user-1",
		Email:    "newuser@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	// Manually call reconcilePHPPools with mocked socket check
	r.socketReady = func(ctx context.Context, socketPath string, timeout, pollInterval time.Duration) bool {
		return true // Socket ready immediately
	}

	r.ReconcilePHPPools(ctx)

	// Verify that a pool was created with default values
	require.Len(t, phpPoolRepo.pools, 1, "should create 1 pool")
	var pool *models.PHPPool
	for _, p := range phpPoolRepo.pools {
		pool = p
	}
	require.NotNil(t, pool)
	require.Equal(t, user.ID, pool.UserID)
	require.Equal(t, "8.4", pool.PHPVersion)
	require.Equal(t, "active", pool.Status)
	require.NoError(t, nil)
}

func TestReconcilePHPPools_SkipActivePool(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: user with active pool
	username := "activeuser"
	user := &models.User{
		ID:       "user-1",
		Email:    "activeuser@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	activePool := &models.PHPPool{
		ID:         "pool-1",
		UserID:     user.ID,
		PHPVersion: "8.3",
		PmMode:     "ondemand",
		Status:     "active",
	}
	phpPoolRepo.pools[activePool.ID] = activePool

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	r.ReconcilePHPPools(ctx)

	// Verify that no agent calls were made (pool already active)
	require.Len(t, agent.calls, 0, "should not call agent for active pool")
}

func TestReconcilePHPPools_RetryPendingPool(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: user with pending pool
	username := "pendinguser"
	user := &models.User{
		ID:       "user-1",
		Email:    "pendinguser@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	pendingPool := &models.PHPPool{
		ID:         "pool-1",
		UserID:     user.ID,
		PHPVersion: "8.3",
		PmMode:     "ondemand",
		Status:     "pending",
	}
	phpPoolRepo.pools[pendingPool.ID] = pendingPool

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	r.socketReady = func(ctx context.Context, socketPath string, timeout, pollInterval time.Duration) bool {
		return true // Socket ready
	}

	r.ReconcilePHPPools(ctx)

	// Verify that agent was called
	require.Greater(t, len(agent.calls), 0, "should call agent for pending pool")

	// Find php.pool.apply call
	var applyCall *fakeCall
	for _, call := range agent.calls {
		if call.method == "php.pool.apply" {
			applyCall = &call
			break
		}
	}
	require.NotNil(t, applyCall, "should call php.pool.apply")

	// Verify pool status changed to active
	pool, err := phpPoolRepo.FindByID(ctx, pendingPool.ID)
	require.NoError(t, err)
	require.Equal(t, "active", pool.Status)
}

func TestReconcilePHPPools_RetryErrorPool(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: user with error pool
	username := "erruser"
	user := &models.User{
		ID:       "user-1",
		Email:    "erruser@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	errMsg := "previous error"
	errorPool := &models.PHPPool{
		ID:         "pool-1",
		UserID:     user.ID,
		PHPVersion: "8.3",
		PmMode:     "ondemand",
		Status:     "error",
		LastError:  &errMsg,
	}
	phpPoolRepo.pools[errorPool.ID] = errorPool

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	r.socketReady = func(ctx context.Context, socketPath string, timeout, pollInterval time.Duration) bool {
		return true
	}

	r.ReconcilePHPPools(ctx)

	// Verify that agent was called to retry
	agentCallCount := 0
	for _, call := range agent.calls {
		if call.method == "php.pool.apply" {
			agentCallCount++
		}
	}
	require.Greater(t, agentCallCount, 0, "should retry error pool")

	// Verify pool status changed to active
	pool, err := phpPoolRepo.FindByID(ctx, errorPool.ID)
	require.NoError(t, err)
	require.Equal(t, "active", pool.Status)
}

func TestReconcilePHPPools_AgentFailureMarksError(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{
		failMethod: "php.pool.apply",
	}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: user with pending pool
	username := "failuser"
	user := &models.User{
		ID:       "user-1",
		Email:    "failuser@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	pendingPool := &models.PHPPool{
		ID:     "pool-1",
		UserID: user.ID,
		Status: "pending",
	}
	phpPoolRepo.pools[pendingPool.ID] = pendingPool

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	r.ReconcilePHPPools(ctx)

	// Verify pool marked as error
	pool, err := phpPoolRepo.FindByID(ctx, pendingPool.ID)
	require.NoError(t, err)
	require.Equal(t, "error", pool.Status)
	require.NotNil(t, pool.LastError)
	require.Contains(t, *pool.LastError, "agent apply failed")
}

func TestReconcilePHPPools_SocketTimeoutMarksError(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: user with pending pool
	username := "timeoutuser"
	user := &models.User{
		ID:       "user-1",
		Email:    "timeoutuser@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	pendingPool := &models.PHPPool{
		ID:     "pool-1",
		UserID: user.ID,
		Status: "pending",
	}
	phpPoolRepo.pools[pendingPool.ID] = pendingPool

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	// Socket never becomes ready
	r.socketReady = func(ctx context.Context, socketPath string, timeout, pollInterval time.Duration) bool {
		return false
	}

	r.ReconcilePHPPools(ctx)

	// Verify pool marked as error
	pool, err := phpPoolRepo.FindByID(ctx, pendingPool.ID)
	require.NoError(t, err)
	require.Equal(t, "error", pool.Status)
	require.NotNil(t, pool.LastError)
	require.Equal(t, "socket did not become ready after agent apply", *pool.LastError)
}

func TestReconcilePHPPools_NginxRegenForBoundDomains(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: user with pending pool and two domains bound to it
	username := "phphost"
	user := &models.User{
		ID:       "user-1",
		Email:    "phphost@example.com",
		Username: &username,
	}
	userRepo.users[user.ID] = user

	pendingPool := &models.PHPPool{
		ID:     "pool-1",
		UserID: user.ID,
		Status: "pending",
	}
	phpPoolRepo.pools[pendingPool.ID] = pendingPool

	// Create two domains bound to this pool
	now := time.Now().UTC()
	domain1 := &models.Domain{
		ID:        "domain-1",
		UserID:    user.ID,
		Name:      "site1.com",
		DocRoot:   "/home/phphost/domains/site1.com/public_html",
		IsEnabled: true,
		PHPPoolID: &pendingPool.ID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domain2 := &models.Domain{
		ID:        "domain-2",
		UserID:    user.ID,
		Name:      "site2.com",
		DocRoot:   "/home/phphost/domains/site2.com/public_html",
		IsEnabled: true,
		PHPPoolID: &pendingPool.ID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[domain1.ID] = domain1
	domainRepo.domains[domain2.ID] = domain2

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	r.socketReady = func(ctx context.Context, socketPath string, timeout, pollInterval time.Duration) bool {
		return true
	}

	r.ReconcilePHPPools(ctx)

	// Verify that domain.create was called for each bound domain
	domainCreateCount := 0
	for _, call := range agent.calls {
		if call.method == "domain.create" {
			domainCreateCount++
		}
	}
	require.Equal(t, 2, domainCreateCount, "should call domain.create for each bound domain")
}

func TestReconcilePHPPools_ContinueOnUserWithoutUsername(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	// Setup: first user without username (will fail), second user with username (should succeed)
	user1 := &models.User{
		ID:       "user-1",
		Email:    "nouser@example.com",
		Username: nil, // No username
	}
	userRepo.users[user1.ID] = user1

	username2 := "gooduser"
	user2 := &models.User{
		ID:       "user-2",
		Email:    "gooduser@example.com",
		Username: &username2,
	}
	userRepo.users[user2.ID] = user2

	// Both users have pending pools
	pool1 := &models.PHPPool{
		ID:     "pool-1",
		UserID: user1.ID,
		Status: "pending",
	}
	pool2 := &models.PHPPool{
		ID:     "pool-2",
		UserID: user2.ID,
		Status: "pending",
	}
	phpPoolRepo.pools[pool1.ID] = pool1
	phpPoolRepo.pools[pool2.ID] = pool2

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithPHPPools(phpPoolRepo)

	r.socketReady = func(ctx context.Context, socketPath string, timeout, pollInterval time.Duration) bool {
		return true
	}

	r.ReconcilePHPPools(ctx)

	// Verify pool1 marked as error (no username)
	p1, _ := phpPoolRepo.FindByID(ctx, pool1.ID)
	require.Equal(t, "error", p1.Status)

	// Verify pool2 became active (username exists)
	p2, _ := phpPoolRepo.FindByID(ctx, pool2.ID)
	require.Equal(t, "active", p2.Status)
}

// fakeSSO provides a mock SSO service for testing.
type fakeSSO struct {
	ensureShadowCalls []string // Track userID calls
	ensureShadowError error    // Error to return
}

func (f *fakeSSO) EnsureShadow(ctx context.Context, userID string) error {
	f.ensureShadowCalls = append(f.ensureShadowCalls, userID)
	return f.ensureShadowError
}

func TestReconcileMysqlAdminShadow_SkipsIfNoSSO(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second})
	// No WithSSO call; sso field is nil

	// Should not panic and should just return
	r.reconcileMysqlAdminShadow(ctx)
}

func TestReconcileMysqlAdminShadow_SkipsUsersWithoutUsername(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	sso := &fakeSSO{}

	// Create a user without username (should be skipped)
	user1 := &models.User{
		ID:       "user-1",
		Email:    "nousername@example.com",
		Username: nil,
	}
	userRepo.users[user1.ID] = user1

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithSSO(sso)

	r.reconcileMysqlAdminShadow(ctx)

	// SSO should never be called for user without username
	require.Equal(t, 0, len(sso.ensureShadowCalls))
}

func TestReconcileMysqlAdminShadow_SkipsUsersWithExistingShadow(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	sso := &fakeSSO{}

	// Create a user with username and existing shadow account
	username := "testuser"
	mysqladminUsername := "admin_testuser"
	user := &models.User{
		ID:                 "user-1",
		Email:              "test@example.com",
		Username:           &username,
		MysqladminUsername: &mysqladminUsername, // Already has shadow
	}
	userRepo.users[user.ID] = user

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithSSO(sso)

	r.reconcileMysqlAdminShadow(ctx)

	// SSO should not be called since shadow already exists
	require.Equal(t, 0, len(sso.ensureShadowCalls))
}

func TestReconcileMysqlAdminShadow_EnsuresForUsersNeedingShadow(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	sso := &fakeSSO{}

	// Create users: one needs shadow, one doesn't
	username1 := "user1"
	user1 := &models.User{
		ID:       "user-1",
		Email:    "user1@example.com",
		Username: &username1,
		// No shadow yet
	}
	userRepo.users[user1.ID] = user1

	username2 := "user2"
	mysqladminUsername2 := "admin_user2"
	user2 := &models.User{
		ID:                 "user-2",
		Email:              "user2@example.com",
		Username:           &username2,
		MysqladminUsername: &mysqladminUsername2, // Already has shadow
	}
	userRepo.users[user2.ID] = user2

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithSSO(sso)

	r.reconcileMysqlAdminShadow(ctx)

	// SSO should be called only for user1
	require.Equal(t, 1, len(sso.ensureShadowCalls))
	require.Equal(t, "user-1", sso.ensureShadowCalls[0])
}

func TestReconcileMysqlAdminShadow_ContinuesOnPerUserError(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	sso := &fakeSSO{ensureShadowError: errors.New("test error")}

	// Create multiple users needing shadow
	for i := 0; i < 3; i++ {
		username := fmt.Sprintf("user%d", i)
		user := &models.User{
			ID:       fmt.Sprintf("user-%d", i),
			Email:    fmt.Sprintf("user%d@example.com", i),
			Username: &username,
		}
		userRepo.users[user.ID] = user
	}

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithSSO(sso)

	// Should not panic even though SSO fails for all users
	r.reconcileMysqlAdminShadow(ctx)

	// All three should have been attempted (resilience)
	require.Equal(t, 3, len(sso.ensureShadowCalls))
}

func TestReconcileMysqlAdminShadow_BatchLimitOf50(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	sso := &fakeSSO{}

	// Create 75 users (exceeds batch limit of 50)
	for i := 0; i < 75; i++ {
		username := fmt.Sprintf("user%d", i)
		user := &models.User{
			ID:       fmt.Sprintf("user-%d", i),
			Email:    fmt.Sprintf("user%d@example.com", i),
			Username: &username,
		}
		userRepo.users[user.ID] = user
	}

	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithSSO(sso)

	r.reconcileMysqlAdminShadow(ctx)

	// Should only process first 50 users in this pass (batch limit)
	require.Equal(t, 50, len(sso.ensureShadowCalls))
}

// TestSANHostnamesForDomain covers the helper that builds extra
// SAN list for the per-domain cert. Email-enabled domains advertise
// mail.<d> + autoconfig.<d> (pdns CNAMEs autoconfig → mail.<zone>
// via dnscompile/email_records.go, so HTTP-01 challenges resolve);
// others return nil (base [domain, www.domain] set handled by the
// cert gen).
func TestSANHostnamesForDomain(t *testing.T) {
	t.Run("nil domain", func(t *testing.T) {
		if got := sanHostnamesForDomain(nil); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("email disabled, www out", func(t *testing.T) {
		d := &models.Domain{Name: "example.com", EmailEnabled: false, CreateWWW: false}
		if got := sanHostnamesForDomain(d); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("www opt-in adds www (GH #895)", func(t *testing.T) {
		d := &models.Domain{Name: "example.com", EmailEnabled: false, CreateWWW: true}
		got := sanHostnamesForDomain(d)
		if len(got) != 1 || got[0] != "www.example.com" {
			t.Errorf("got %v, want [www.example.com]", got)
		}
	})
	t.Run("www opt-in + email keeps www first", func(t *testing.T) {
		d := &models.Domain{Name: "example.com", EmailEnabled: true, CreateWWW: true}
		got := sanHostnamesForDomain(d)
		want := []string{"www.example.com", "mail.example.com", "autoconfig.example.com", "autodiscover.example.com"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i, w := range want {
			if got[i] != w {
				t.Errorf("[%d]: got %q, want %q", i, got[i], w)
			}
		}
	})
	t.Run("SkipAutoSAN + www opt-in keeps only www", func(t *testing.T) {
		d := &models.Domain{Name: "example.com", EmailEnabled: true, MTASTSEnabled: true, SkipAutoSAN: true, CreateWWW: true}
		got := sanHostnamesForDomain(d)
		if len(got) != 1 || got[0] != "www.example.com" {
			t.Errorf("got %v, want [www.example.com]", got)
		}
	})
	t.Run("email enabled", func(t *testing.T) {
		d := &models.Domain{Name: "example.com", EmailEnabled: true}
		got := sanHostnamesForDomain(d)
		want := []string{"mail.example.com", "autoconfig.example.com", "autodiscover.example.com"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i, w := range want {
			if got[i] != w {
				t.Errorf("[%d]: got %q, want %q", i, got[i], w)
			}
		}
	})
	t.Run("mta_sts only", func(t *testing.T) {
		d := &models.Domain{Name: "example.com", MTASTSEnabled: true}
		got := sanHostnamesForDomain(d)
		want := []string{"mta-sts.example.com"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("email + mta_sts", func(t *testing.T) {
		d := &models.Domain{Name: "example.com", EmailEnabled: true, MTASTSEnabled: true}
		got := sanHostnamesForDomain(d)
		want := []string{"mail.example.com", "autoconfig.example.com", "autodiscover.example.com", "mta-sts.example.com"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i, w := range want {
			if got[i] != w {
				t.Errorf("[%d]: got %q, want %q", i, got[i], w)
			}
		}
	})
}

func (*fakeDomainRepo) AttachDockerApp(context.Context, string, string, models.NginxRules) error {
	return nil
}
func (*fakeDomainRepo) DetachDockerApp(context.Context, string, bool) error {
	return nil
}

// fakePackageRepoMin is a minimal PackageRepository for the GH #402
// disable_functions param test — only FindByID is exercised.
type fakePackageRepoMin struct {
	pkgs map[string]*models.HostingPackage
}

func (f *fakePackageRepoMin) Create(context.Context, *models.HostingPackage) error { return nil }
func (f *fakePackageRepoMin) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if p, ok := f.pkgs[id]; ok {
		return p, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakePackageRepoMin) FindByName(context.Context, string) (*models.HostingPackage, error) {
	return nil, repository.ErrNotFound
}
func (f *fakePackageRepoMin) List(context.Context, repository.ListOptions) ([]models.HostingPackage, int64, error) {
	return nil, 0, nil
}
func (f *fakePackageRepoMin) Update(context.Context, *models.HostingPackage) error { return nil }
func (f *fakePackageRepoMin) Delete(context.Context, string) error                 { return nil }
func (f *fakePackageRepoMin) EnsureDefaults(context.Context) error                 { return nil }

func applyCallParams(t *testing.T, agent *fakeAgent) map[string]any {
	t.Helper()
	for _, call := range agent.calls {
		if call.method == "php.pool.apply" {
			p, ok := call.params.(map[string]any)
			if !ok {
				t.Fatalf("php.pool.apply params not a map: %T", call.params)
			}
			return p
		}
	}
	t.Fatal("no php.pool.apply call")
	return nil
}

func newPoolReconcilerWithPkg(pkg *models.HostingPackage, packageID *string) (*Reconciler, *fakeAgent, *fakePHPPoolRepo) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	agent := &fakeAgent{}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}
	username := "u1"
	userRepo.users["user-1"] = &models.User{ID: "user-1", Email: "u1@example.com", Username: &username, PackageID: packageID}
	phpPoolRepo.pools["pool-1"] = &models.PHPPool{ID: "pool-1", UserID: "user-1", PHPVersion: "8.4", PmMode: "ondemand", Status: "pending"}
	r := New(&fakeDomainRepo{domains: make(map[string]*models.Domain)}, userRepo, agent, log, Config{Interval: time.Second}).
		WithPHPPools(phpPoolRepo)
	if pkg != nil {
		r.WithPackages(&fakePackageRepoMin{pkgs: map[string]*models.HostingPackage{pkg.ID: pkg}})
	}
	r.socketReady = func(context.Context, string, time.Duration, time.Duration) bool { return true }
	return r, agent, phpPoolRepo
}

// GH #402: a package with php_exec_enabled=1 sends disable_functions="" (opt-out);
// a default package (or none) omits the key so the agent applies the #401 default.
func TestApplyPHPPool_DisableFunctionsByPackage(t *testing.T) {
	pkgID := "pkg-1"

	// exec-enabled package -> explicit "" opt-out
	r, agent, _ := newPoolReconcilerWithPkg(&models.HostingPackage{ID: pkgID, Name: "exec", PHPExecEnabled: true}, &pkgID)
	r.ReconcilePHPPools(context.Background())
	p := applyCallParams(t, agent)
	v, ok := p["disable_functions"]
	require.True(t, ok, "exec-enabled package must send disable_functions key")
	require.Equal(t, "", v, "exec-enabled package must send empty disable_functions (opt-out)")

	// default package -> key absent (agent applies its safe default)
	r2, agent2, _ := newPoolReconcilerWithPkg(&models.HostingPackage{ID: pkgID, Name: "std", PHPExecEnabled: false}, &pkgID)
	r2.ReconcilePHPPools(context.Background())
	p2 := applyCallParams(t, agent2)
	_, ok2 := p2["disable_functions"]
	require.False(t, ok2, "default package must NOT send disable_functions (agent uses safe default)")
}

// TestReconcileVersionedPHPPools verifies GH #329: a versioned pool with a
// bound domain is applied (slug + additive), and an orphan versioned pool with
// no bound domains is reaped. The default pool is never applied here or reaped.
func TestReconcileVersionedPHPPools(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	phpPoolRepo := &fakePHPPoolRepo{pools: make(map[string]*models.PHPPool)}

	username := "phpuser"
	user := &models.User{ID: "user-1", Email: "u@e.com", Username: &username}
	userRepo.users[user.ID] = user

	// Base timestamps well in the past so the orphan is older than the reap
	// grace; ordering (default earliest) is preserved.
	base := time.Now().UTC().Add(-1 * time.Hour)
	// Default pool (earliest) — must NOT be touched by the versioned pass.
	phpPoolRepo.pools["pool-default"] = &models.PHPPool{
		ID: "pool-default", UserID: user.ID, PHPVersion: "8.4",
		PmMode: "ondemand", PmMaxChildren: 20, ProcessIdleTimeoutSeconds: 60,
		Status: "active", CreatedAt: base,
	}
	// Versioned pool WITH a bound domain, pending -> should be applied.
	phpPoolRepo.pools["pool-82"] = &models.PHPPool{
		ID: "pool-82", UserID: user.ID, PHPVersion: "8.2",
		PmMode: "ondemand", PmMaxChildren: 20, ProcessIdleTimeoutSeconds: 60,
		Status: "pending", CreatedAt: base.Add(time.Minute),
	}
	pool82 := "pool-82"
	domainRepo.domains["d1"] = &models.Domain{
		ID: "d1", UserID: user.ID, Name: "a.com", IsEnabled: true, PHPPoolID: &pool82,
	}
	// Orphan versioned pool (no domains), OLD -> should be reaped.
	phpPoolRepo.pools["pool-80"] = &models.PHPPool{
		ID: "pool-80", UserID: user.ID, PHPVersion: "8.0",
		PmMode: "ondemand", PmMaxChildren: 20, ProcessIdleTimeoutSeconds: 60,
		Status: "active", CreatedAt: base.Add(2 * time.Minute),
	}
	// FRESH orphan versioned pool (no domains, just created) -> must be SPARED
	// by the reap grace: this models the bind handler's create→bind window.
	phpPoolRepo.pools["pool-83-fresh"] = &models.PHPPool{
		ID: "pool-83-fresh", UserID: user.ID, PHPVersion: "8.3",
		PmMode: "ondemand", PmMaxChildren: 20, ProcessIdleTimeoutSeconds: 60,
		Status: "pending", CreatedAt: time.Now().UTC(),
	}

	r := New(domainRepo, userRepo, agent, log, Config{Interval: time.Second}).
		WithPHPPools(phpPoolRepo)
	// Make the socket-ready check pass instantly for the applied pool.
	r.socketReady = func(context.Context, string, time.Duration, time.Duration) bool { return true }

	r.reconcileVersionedPHPPools(ctx)

	var applied, removed *fakeCall
	for i := range agent.calls {
		switch agent.calls[i].method {
		case "php.pool.apply":
			applied = &agent.calls[i]
		case "php.pool.remove":
			removed = &agent.calls[i]
		}
	}

	// Applied the 8.2 versioned pool with the right slug + additive.
	require.NotNil(t, applied, "expected php.pool.apply for the versioned pool")
	ap := applied.params.(map[string]any)
	require.Equal(t, "phpuser-php8.2", ap["slug"])
	require.Equal(t, true, ap["additive"])
	require.Equal(t, "8.2", ap["php_version"])

	// Reaped the orphan 8.0 pool + deleted its row.
	require.NotNil(t, removed, "expected php.pool.remove for the orphan pool")
	rp := removed.params.(map[string]any)
	require.Equal(t, "phpuser-php8.0", rp["slug"])
	if _, err := phpPoolRepo.FindByID(ctx, "pool-80"); err != repository.ErrNotFound {
		t.Errorf("orphan pool row should be deleted, got err=%v", err)
	}
	// Default pool untouched (still present, never applied/removed with its slug).
	require.Equal(t, "phpuser", models.PoolSlug("phpuser", "8.4", true))
	if _, err := phpPoolRepo.FindByID(ctx, "pool-default"); err != nil {
		t.Errorf("default pool must remain: %v", err)
	}
	// The fresh orphan must NOT be reaped (reap grace protects the bind
	// create→bind window). It also must not have been the one removed.
	if _, err := phpPoolRepo.FindByID(ctx, "pool-83-fresh"); err != nil {
		t.Errorf("fresh versioned pool must be spared by the reap grace: %v", err)
	}
	require.Equal(t, "phpuser-php8.0", rp["slug"], "only the OLD orphan should be reaped, not the fresh one")
}
