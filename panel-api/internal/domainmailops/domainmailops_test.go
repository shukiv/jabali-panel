package domainmailops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The fakes embed the repository interfaces and override only the methods the
// enable/disable flow touches — an un-overridden method panics on the nil
// embed, which is what we want (the test would be calling something it
// shouldn't). Same precedent as internal/dirprivops' fakes.

type fakeDomainRepo struct {
	repository.DomainRepository
	updated   *repository.DomainEmailState
	updateErr error
}

func (f *fakeDomainRepo) UpdateEmailState(_ context.Context, _ string, st repository.DomainEmailState) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = &st
	return nil
}

type fakeZoneRepo struct {
	repository.DNSZoneRepository
	zone *models.DNSZone
	err  error
}

func (f *fakeZoneRepo) FindByDomainID(_ context.Context, _ string) (*models.DNSZone, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.zone, nil
}

type fakeRecordRepo struct {
	repository.DNSRecordRepository
	existing         []models.DNSRecord
	created          []models.DNSRecord
	deletedManagedBy string
	deleted          bool
	createErr        error
}

func (f *fakeRecordRepo) ListByZoneID(_ context.Context, _ string) ([]models.DNSRecord, error) {
	return f.existing, nil
}

func (f *fakeRecordRepo) Create(_ context.Context, r *models.DNSRecord) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, *r)
	return nil
}

func (f *fakeRecordRepo) DeleteByZoneIDAndManagedBy(_ context.Context, _ string, managedBy string) error {
	f.deleted = true
	f.deletedManagedBy = managedBy
	return nil
}

type fakeServerSettings struct {
	repository.ServerSettingsRepository
}

// Get returns a nil settings row; dnscompile.BuildEmailRecords tolerates nil.
func (fakeServerSettings) Get(_ context.Context) (*models.ServerSettings, error) {
	return nil, nil
}

type fakeSSLRepo struct {
	repository.SSLCertificateRepository
	cert          *models.SSLCertificate
	findErr       error
	updatedID     string
	updatedStatus string
	updateErr     error
}

func (f *fakeSSLRepo) FindByDomainID(_ context.Context, _ string) (*models.SSLCertificate, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.cert, nil
}

func (f *fakeSSLRepo) UpdateStatus(_ context.Context, id, status string, _ *string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updatedID = id
	f.updatedStatus = status
	return nil
}

type fakeScheduler struct{ scheduled []string }

func (f *fakeScheduler) Schedule(id string) { f.scheduled = append(f.scheduled, id) }

// okCall returns a well-formed email_enable reply.
func okCall(cmd string, params any) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true,"dkim_selector":"jabali","dkim_public_key":"v=DKIM1; k=ed25519; p=AAA"}`), nil
}

func baseDeps() (*fakeDomainRepo, *fakeZoneRepo, *fakeRecordRepo, Deps) {
	domains := &fakeDomainRepo{}
	zones := &fakeZoneRepo{zone: &models.DNSZone{ID: "zone1", DomainID: "dom1", Name: "example.com"}}
	records := &fakeRecordRepo{}
	d := Deps{
		Domains:        domains,
		DNSZones:       zones,
		DNSRecords:     records,
		ServerSettings: fakeServerSettings{},
		Call:           func(ctx context.Context, cmd string, params any) (json.RawMessage, error) { return okCall(cmd, params) },
	}
	return domains, zones, records, d
}

func newDomain() *models.Domain {
	return &models.Domain{ID: "dom1", UserID: "user1", Name: "example.com"}
}

func m6Count(recs []models.DNSRecord) int {
	n := 0
	for _, r := range recs {
		if r.ManagedBy != nil && *r.ManagedBy == dnscompile.EmailRecordsManagedBy {
			n++
		}
	}
	return n
}

func TestEnable_HappyPath_MutatesPersistsAndWritesDNS(t *testing.T) {
	domains, _, records, d := baseDeps()
	dom := newDomain()

	sel, pub, warnings, err := Enable(context.Background(), d, dom)
	require.NoError(t, err)
	require.Equal(t, "jabali", sel)
	require.Equal(t, "v=DKIM1; k=ed25519; p=AAA", pub)
	require.Empty(t, warnings, "clean zone, no SSL deps → no warnings")

	// Caller struct mutated in place.
	require.True(t, dom.EmailEnabled)
	require.NotNil(t, dom.DkimSelector)
	require.Equal(t, "jabali", *dom.DkimSelector)
	require.NotNil(t, dom.EmailEnabledAt)

	// DB row flipped with DKIM material.
	require.NotNil(t, domains.updated)
	require.True(t, domains.updated.Enabled)
	require.NotNil(t, domains.updated.DkimSelector)
	require.Equal(t, "jabali", *domains.updated.DkimSelector)

	// M6 DNS rows written.
	require.GreaterOrEqual(t, m6Count(records.created), 1)
	require.Equal(t, len(records.created), m6Count(records.created), "every created row is m6-managed")
}

func TestEnable_AgentUnconfigured_NoPersist(t *testing.T) {
	domains, _, records, d := baseDeps()
	d.Call = nil
	dom := newDomain()

	_, _, _, err := Enable(context.Background(), d, dom)
	require.ErrorIs(t, err, ErrAgentUnconfigured)
	require.False(t, dom.EmailEnabled)
	require.Nil(t, domains.updated, "no DB write on agent-unconfigured")
	require.Empty(t, records.created)
}

func TestEnable_AgentFailed_NoPersist(t *testing.T) {
	domains, _, _, d := baseDeps()
	d.Call = func(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
		return nil, errors.New("dial: connection refused")
	}
	_, _, _, err := Enable(context.Background(), d, newDomain())
	require.ErrorIs(t, err, ErrAgentFailed)
	require.Contains(t, err.Error(), "connection refused")
	require.Nil(t, domains.updated)
}

func TestEnable_BadResponse_OkFalse(t *testing.T) {
	_, _, _, d := baseDeps()
	d.Call = func(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":false,"dkim_selector":"","dkim_public_key":""}`), nil
	}
	_, _, _, err := Enable(context.Background(), d, newDomain())
	require.ErrorIs(t, err, ErrAgentBadResponse)
}

func TestEnable_BadResponse_Malformed(t *testing.T) {
	_, _, _, d := baseDeps()
	d.Call = func(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
		return json.RawMessage(`not-json`), nil
	}
	_, _, _, err := Enable(context.Background(), d, newDomain())
	require.ErrorIs(t, err, ErrAgentBadResponse)
}

func TestEnable_DNSZoneMissing_WarnsButSucceeds(t *testing.T) {
	domains, zones, _, d := baseDeps()
	zones.zone = nil
	zones.err = repository.ErrNotFound
	dom := newDomain()

	_, _, warnings, err := Enable(context.Background(), d, dom)
	require.NoError(t, err, "zone-missing must not fail the enable")
	require.True(t, dom.EmailEnabled)
	require.NotNil(t, domains.updated, "DB flip still happens without a zone")
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "no zone on file")
}

func TestEnable_IdempotentDNS_NoDuplicateOnReenable(t *testing.T) {
	_, _, records, d := baseDeps()

	// First enable populates the M6 rows.
	_, _, w1, err := Enable(context.Background(), d, newDomain())
	require.NoError(t, err)
	require.Empty(t, w1)
	first := append([]models.DNSRecord(nil), records.created...)
	require.NotEmpty(t, first)

	// Re-enable with those rows already present → no new creates.
	records.existing = first
	records.created = nil
	_, _, w2, err := Enable(context.Background(), d, newDomain())
	require.NoError(t, err)
	require.Empty(t, w2)
	require.Empty(t, records.created, "existing m6 rows must not be duplicated")
}

func TestEnable_UserEditedConflict_WarnsAndSkips(t *testing.T) {
	_, _, records, d := baseDeps()

	// Discover the intended row set by a dry first run.
	_, _, _, err := Enable(context.Background(), d, newDomain())
	require.NoError(t, err)
	require.NotEmpty(t, records.created)
	victim := records.created[0]

	// Seed a user-edited (ManagedBy=nil) row at the same (name,type): M6 must
	// leave it in place and surface a warning instead of overwriting.
	records.existing = []models.DNSRecord{{Name: victim.Name, Type: victim.Type, ManagedBy: nil}}
	records.created = nil
	_, _, warnings, err := Enable(context.Background(), d, newDomain())
	require.NoError(t, err)
	require.NotEmpty(t, warnings)
	require.Contains(t, warnings[0], "blocking")
	for _, r := range records.created {
		require.False(t, r.Name == victim.Name && r.Type == victim.Type,
			"must not recreate the row a user edited")
	}
}

func TestEnable_SSLFlip_WhenWiredAndCertIssued(t *testing.T) {
	_, _, _, d := baseDeps()
	ssl := &fakeSSLRepo{cert: &models.SSLCertificate{ID: "cert1", Status: models.SSLStatusIssued}}
	sched := &fakeScheduler{}
	d.SSLCerts = ssl
	d.SSLReconciler = sched

	_, _, warnings, err := Enable(context.Background(), d, newDomain())
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, "cert1", ssl.updatedID)
	require.Equal(t, models.SSLStatusRenewing, ssl.updatedStatus)
	require.Equal(t, []string{"dom1"}, sched.scheduled)
}

func TestEnable_SSLFlip_SkippedWhenCertTransitional(t *testing.T) {
	_, _, _, d := baseDeps()
	ssl := &fakeSSLRepo{cert: &models.SSLCertificate{ID: "cert1", Status: models.SSLStatusRenewing}}
	sched := &fakeScheduler{}
	d.SSLCerts = ssl
	d.SSLReconciler = sched

	_, _, warnings, err := Enable(context.Background(), d, newDomain())
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Empty(t, ssl.updatedStatus, "a non-issued/self-signed cert is left alone")
	require.Empty(t, sched.scheduled)
}

func TestEnable_SSLFlip_NoCertYet_NoWarn(t *testing.T) {
	_, _, _, d := baseDeps()
	ssl := &fakeSSLRepo{findErr: repository.ErrNotFound}
	d.SSLCerts = ssl

	_, _, warnings, err := Enable(context.Background(), d, newDomain())
	require.NoError(t, err)
	require.Empty(t, warnings, "no cert yet is not an error; first issuance picks up the SANs")
	require.Empty(t, ssl.updatedStatus)
}

func TestDisable_HappyPath_KeepsDKIM_DeletesManagedDNS(t *testing.T) {
	domains, _, records, d := baseDeps()
	dom := newDomain()
	dom.EmailEnabled = true

	err := Disable(context.Background(), d, dom)
	require.NoError(t, err)

	require.NotNil(t, domains.updated)
	require.False(t, domains.updated.Enabled)
	require.Nil(t, domains.updated.EmailEnabledAt)
	require.Nil(t, domains.updated.DkimSelector, "DKIM material preserved per ADR-0043")
	require.Nil(t, domains.updated.DkimPublicKey)

	require.True(t, records.deleted)
	require.Equal(t, dnscompile.EmailRecordsManagedBy, records.deletedManagedBy)
	require.False(t, dom.EmailEnabled, "caller struct reflects the flip")
}

func TestDisable_AgentUnconfigured(t *testing.T) {
	domains, _, records, d := baseDeps()
	d.Call = nil
	err := Disable(context.Background(), d, newDomain())
	require.ErrorIs(t, err, ErrAgentUnconfigured)
	require.Nil(t, domains.updated)
	require.False(t, records.deleted)
}

func TestDisable_AgentFailed_LeavesDBAndDNS(t *testing.T) {
	domains, _, records, d := baseDeps()
	d.Call = func(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
		return nil, errors.New("stalwart unreachable")
	}
	err := Disable(context.Background(), d, newDomain())
	require.ErrorIs(t, err, ErrAgentFailed)
	require.Nil(t, domains.updated, "agent-first: no DB flip when teardown fails")
	require.False(t, records.deleted, "no DNS delete when teardown fails")
}

func TestDisable_UpdateStateError_NoDNSDelete(t *testing.T) {
	domains, _, records, d := baseDeps()
	domains.updateErr = errors.New("db down")
	err := Disable(context.Background(), d, newDomain())
	require.Error(t, err)
	require.Contains(t, err.Error(), "update email_enabled row")
	require.False(t, records.deleted, "DNS cleanup only runs after the state flip persists")
}

// --- RotateDKIM (JAB-286) ---

// rotateReply is a well-formed domain.email_dkim_rotate response.
func rotateReply(newKey string) json.RawMessage {
	return json.RawMessage(`{"old_dkim_public_key":"v=DKIM1; k=ed25519; p=OLD","new_dkim_public_key":"` +
		newKey + `","old_key_backup_path":"/etc/jabali-panel/dkim/example.com.key.old"}`)
}

// enabledDomain is a domain with email already on and a known enabled-at
// timestamp, so a rotation test can assert the timestamp is preserved.
func enabledDomain() (*models.Domain, time.Time) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d := newDomain()
	d.EmailEnabled = true
	d.EmailEnabledAt = &at
	sel := "jabali"
	old := "v=DKIM1; k=ed25519; p=OLD"
	d.DkimSelector = &sel
	d.DkimPublicKey = &old
	return d, at
}

func TestRotateDKIM_HappyPath_PersistsPair_RepublishesDNS_PreservesEnabledAt(t *testing.T) {
	domains, _, records, d := baseDeps()
	d.Call = func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		require.Equal(t, "domain.email_dkim_rotate", cmd)
		return rotateReply("v=DKIM1; k=ed25519; p=NEW"), nil
	}
	dom, enabledAt := enabledDomain()

	res, warnings, err := RotateDKIM(context.Background(), d, dom)
	require.NoError(t, err)
	require.Empty(t, warnings, "clean zone → no warnings")
	require.Equal(t, "jabali", res.Selector)
	require.Equal(t, "v=DKIM1; k=ed25519; p=NEW", res.NewDKIMPublicKey)
	require.Equal(t, "v=DKIM1; k=ed25519; p=OLD", res.OldDKIMPublicKey)
	require.Equal(t, "/etc/jabali-panel/dkim/example.com.key.old", res.OldKeyBackupPath)

	// One selector/public-key pair persisted (AC1).
	require.NotNil(t, domains.updated)
	require.True(t, domains.updated.Enabled)
	require.NotNil(t, domains.updated.DkimSelector)
	require.Equal(t, "jabali", *domains.updated.DkimSelector)
	require.NotNil(t, domains.updated.DkimPublicKey)
	require.Equal(t, "v=DKIM1; k=ed25519; p=NEW", *domains.updated.DkimPublicKey)

	// email_enabled_at preserved, not NULLed (the latent-bug fix).
	require.NotNil(t, domains.updated.EmailEnabledAt)
	require.Equal(t, enabledAt, *domains.updated.EmailEnabledAt)

	// Old M6 records wiped, then the exact new pair republished (AC1).
	require.True(t, records.deleted)
	require.Equal(t, dnscompile.EmailRecordsManagedBy, records.deletedManagedBy)
	require.GreaterOrEqual(t, m6Count(records.created), 1)
	var sawDKIM bool
	for _, r := range records.created {
		if r.Type == "TXT" && strings.Contains(r.Name, "_domainkey") {
			sawDKIM = true
			require.Contains(t, r.Content, "p=NEW", "republished DKIM TXT must carry the NEW key")
		}
	}
	require.True(t, sawDKIM, "a DKIM TXT record must be republished")

	// Caller struct mutated so the adapter can echo without re-fetching.
	require.NotNil(t, dom.DkimPublicKey)
	require.Equal(t, "v=DKIM1; k=ed25519; p=NEW", *dom.DkimPublicKey)
}

func TestRotateDKIM_NotEnabled_NothingCalled(t *testing.T) {
	domains, _, records, d := baseDeps()
	called := false
	d.Call = func(context.Context, string, any) (json.RawMessage, error) { called = true; return nil, nil }
	dom := newDomain() // EmailEnabled = false

	_, _, err := RotateDKIM(context.Background(), d, dom)
	require.ErrorIs(t, err, ErrEmailNotEnabled)
	require.False(t, called, "agent must not be called for a mail-off domain")
	require.Nil(t, domains.updated)
	require.False(t, records.deleted)
}

func TestRotateDKIM_AgentUnconfigured_NoPersist(t *testing.T) {
	domains, _, records, d := baseDeps()
	d.Call = nil
	dom, _ := enabledDomain()

	_, _, err := RotateDKIM(context.Background(), d, dom)
	require.ErrorIs(t, err, ErrAgentUnconfigured)
	require.Nil(t, domains.updated)
	require.False(t, records.deleted)
}

func TestRotateDKIM_AgentFailed_NoPersist_NoWipe(t *testing.T) {
	domains, _, records, d := baseDeps()
	d.Call = func(context.Context, string, any) (json.RawMessage, error) {
		return nil, errors.New("dial: connection refused")
	}
	dom, _ := enabledDomain()

	_, _, err := RotateDKIM(context.Background(), d, dom)
	require.ErrorIs(t, err, ErrAgentFailed)
	require.Contains(t, err.Error(), "connection refused")
	require.Nil(t, domains.updated, "last usable key stays authoritative")
	require.False(t, records.deleted, "no DNS wipe when the agent fails")
}

// AC2: an incomplete agent response (missing new key) must never overwrite the
// last usable state — no persist, no DNS wipe.
func TestRotateDKIM_EmptyNewKey_NoPersist_NoWipe(t *testing.T) {
	domains, _, records, d := baseDeps()
	d.Call = func(context.Context, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"old_dkim_public_key":"v=DKIM1; k=ed25519; p=OLD","new_dkim_public_key":""}`), nil
	}
	dom, _ := enabledDomain()

	_, _, err := RotateDKIM(context.Background(), d, dom)
	require.ErrorIs(t, err, ErrAgentBadResponse)
	require.Nil(t, domains.updated, "empty new key must not overwrite the last usable key")
	require.False(t, records.deleted, "and must not wipe DNS")
}

func TestRotateDKIM_Malformed_NoPersist(t *testing.T) {
	domains, _, records, d := baseDeps()
	d.Call = func(context.Context, string, any) (json.RawMessage, error) {
		return json.RawMessage(`not-json`), nil
	}
	dom, _ := enabledDomain()

	_, _, err := RotateDKIM(context.Background(), d, dom)
	require.ErrorIs(t, err, ErrAgentBadResponse)
	require.Nil(t, domains.updated)
	require.False(t, records.deleted)
}

// AC3: persistence failure is an explicit typed result, and the DNS wipe only
// runs after the row persists.
func TestRotateDKIM_PersistError_Typed_NoWipe(t *testing.T) {
	domains, _, records, d := baseDeps()
	domains.updateErr = errors.New("db down")
	d.Call = func(context.Context, string, any) (json.RawMessage, error) {
		return rotateReply("v=DKIM1; k=ed25519; p=NEW"), nil
	}
	dom, _ := enabledDomain()

	_, _, err := RotateDKIM(context.Background(), d, dom)
	require.ErrorIs(t, err, ErrPersistFailed)
	require.Contains(t, err.Error(), "db down")
	require.False(t, records.deleted, "DNS wipe only runs after the new key persists")
}
