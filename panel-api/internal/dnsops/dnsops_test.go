package dnsops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Fakes embed the repository interface so only the methods the leaf touches are
// implemented; any other call panics (guards against silent scope creep).

type fakeDomainRepo struct {
	repository.DomainRepository
	flipCalls int
	flipErr   error
	flippedTo bool
}

func (r *fakeDomainRepo) UpdateDNSDisabled(_ context.Context, _ string, disabled bool) error {
	r.flipCalls++
	if r.flipErr != nil {
		return r.flipErr
	}
	r.flippedTo = disabled
	return nil
}

type fakeZoneRepo struct {
	repository.DNSZoneRepository
	zone        *models.DNSZone
	deletedZone string
}

func (r *fakeZoneRepo) FindByDomainID(_ context.Context, domainID string) (*models.DNSZone, error) {
	if r.zone == nil || r.zone.DomainID != domainID {
		return nil, repository.ErrNotFound
	}
	return r.zone, nil
}
func (r *fakeZoneRepo) Delete(_ context.Context, id string) error {
	r.deletedZone = id
	return nil
}

type fakeRecordRepo struct {
	repository.DNSRecordRepository
	deletedByZone string
}

func (r *fakeRecordRepo) DeleteByZoneID(_ context.Context, zoneID string) error {
	r.deletedByZone = zoneID
	return nil
}

// recordingCall is a CallFunc that records the last command and returns a
// configurable error.
type recordingCall struct {
	count       int
	lastCommand string
	err         error
}

func (r *recordingCall) fn() CallFunc {
	return func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		r.count++
		r.lastCommand = cmd
		return nil, r.err
	}
}

func newDeps(dr *fakeDomainRepo, zr *fakeZoneRepo, rr *fakeRecordRepo, call *recordingCall) Deps {
	d := Deps{Domains: dr, Zones: zr, Records: rr}
	if call != nil {
		d.Call = call.fn()
	}
	return d
}

func TestTearDownFacet_HappyPath(t *testing.T) {
	dr := &fakeDomainRepo{}
	zr := &fakeZoneRepo{zone: &models.DNSZone{ID: "z1", DomainID: "d1"}}
	rr := &fakeRecordRepo{}
	call := &recordingCall{}
	dom := &models.Domain{ID: "d1", Name: "ex.com"}

	warnings, err := TearDownFacet(context.Background(), newDeps(dr, zr, rr, call), dom)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, 1, dr.flipCalls)
	assert.True(t, dr.flippedTo, "flipped to disabled")
	assert.Equal(t, "dns.zone.delete", call.lastCommand)
	assert.Equal(t, "z1", rr.deletedByZone, "records cleared")
	assert.Equal(t, "z1", zr.deletedZone, "zone row cleared")
}

func TestTearDownFacet_FlipFail_NoTeardown(t *testing.T) {
	dr := &fakeDomainRepo{flipErr: errors.New("db down")}
	zr := &fakeZoneRepo{zone: &models.DNSZone{ID: "z1", DomainID: "d1"}}
	rr := &fakeRecordRepo{}
	call := &recordingCall{}

	_, err := TearDownFacet(context.Background(), newDeps(dr, zr, rr, call), &models.Domain{ID: "d1", Name: "ex.com"})
	require.Error(t, err)
	assert.Equal(t, 1, dr.flipCalls, "flip attempted")
	assert.Equal(t, 0, call.count, "no pdns delete after a failed flip")
	assert.Empty(t, zr.deletedZone, "zone row untouched")
	assert.Empty(t, rr.deletedByZone, "records untouched")
}

func TestTearDownFacet_AgentNil_FailsClosed(t *testing.T) {
	dr := &fakeDomainRepo{}
	_, err := TearDownFacet(context.Background(), Deps{Domains: dr}, &models.Domain{ID: "d1", Name: "ex.com"})
	require.ErrorIs(t, err, ErrAgentUnavailable)
	assert.Equal(t, 0, dr.flipCalls, "no flip when the teardown cannot run")
}

func TestTearDownFacet_PdnsBackendMissing_NoWarning(t *testing.T) {
	dr := &fakeDomainRepo{}
	zr := &fakeZoneRepo{zone: &models.DNSZone{ID: "z1", DomainID: "d1"}}
	rr := &fakeRecordRepo{}
	call := &recordingCall{err: errors.New("powerdns backend not available")}

	warnings, err := TearDownFacet(context.Background(), newDeps(dr, zr, rr, call), &models.Domain{ID: "d1", Name: "ex.com"})
	require.NoError(t, err)
	assert.Empty(t, warnings, "a box without the DNS module is not a failure")
	assert.Equal(t, "z1", zr.deletedZone, "rows still cleared")
}

func TestTearDownFacet_PdnsError_Warns(t *testing.T) {
	dr := &fakeDomainRepo{}
	zr := &fakeZoneRepo{zone: &models.DNSZone{ID: "z1", DomainID: "d1"}}
	rr := &fakeRecordRepo{}
	call := &recordingCall{err: errors.New("connection refused")}

	warnings, err := TearDownFacet(context.Background(), newDeps(dr, zr, rr, call), &models.Domain{ID: "d1", Name: "ex.com"})
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "remove it manually")
	assert.Equal(t, "z1", zr.deletedZone, "rows still cleared despite the pdns warning")
}

func TestTearDownFacet_NoZoneRow_NothingToClean(t *testing.T) {
	dr := &fakeDomainRepo{}
	zr := &fakeZoneRepo{} // no zone → FindByDomainID ErrNotFound
	rr := &fakeRecordRepo{}
	call := &recordingCall{}

	warnings, err := TearDownFacet(context.Background(), newDeps(dr, zr, rr, call), &models.Domain{ID: "d1", Name: "ex.com"})
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, 1, call.count, "pdns delete still attempted")
	assert.Empty(t, zr.deletedZone)
	assert.Empty(t, rr.deletedByZone)
}

func TestDeleteZone_Guards(t *testing.T) {
	cases := []struct {
		name string
		dom  *models.Domain
		want error
	}{
		{"panel primary", &models.Domain{ID: "d1", Name: "p", IsPanelPrimary: true, EmailEnabled: true}, ErrPanelPrimary},
		{"dnssec signed", &models.Domain{ID: "d1", Name: "p", DNSSECEnabled: true, EmailEnabled: true}, ErrDNSSECEnabled},
		{"last facet", &models.Domain{ID: "d1", Name: "p", WebDisabled: true, EmailEnabled: false}, ErrLastFacet},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dr := &fakeDomainRepo{}
			call := &recordingCall{}
			_, err := DeleteZone(context.Background(), newDeps(dr, &fakeZoneRepo{}, &fakeRecordRepo{}, call), tc.dom)
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, 0, dr.flipCalls, "no flip on a refused guard")
			assert.Equal(t, 0, call.count, "no teardown on a refused guard")
		})
	}
}

func TestDeleteZone_HappyPath(t *testing.T) {
	dr := &fakeDomainRepo{}
	zr := &fakeZoneRepo{zone: &models.DNSZone{ID: "z1", DomainID: "d1"}}
	rr := &fakeRecordRepo{}
	call := &recordingCall{}
	dom := &models.Domain{ID: "d1", Name: "ex.com", EmailEnabled: true}

	warnings, err := DeleteZone(context.Background(), newDeps(dr, zr, rr, call), dom)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, 1, dr.flipCalls)
	assert.Equal(t, "dns.zone.delete", call.lastCommand)
}

func TestDeleteZone_AlreadyDisabled_SkipsGuards(t *testing.T) {
	// dns_disabled already true → the transition guards (here DNSSEC, which would
	// otherwise refuse) are skipped and the idempotent teardown runs.
	dr := &fakeDomainRepo{}
	zr := &fakeZoneRepo{} // no row left
	rr := &fakeRecordRepo{}
	call := &recordingCall{}
	dom := &models.Domain{ID: "d1", Name: "ex.com", DNSDisabled: true, DNSSECEnabled: true, WebDisabled: true}

	_, err := DeleteZone(context.Background(), newDeps(dr, zr, rr, call), dom)
	require.NoError(t, err)
	assert.Equal(t, 1, dr.flipCalls, "idempotent re-flip")
	assert.Equal(t, 1, call.count, "teardown runs")
}

func TestDeleteZone_AgentNil_FailsClosed(t *testing.T) {
	dr := &fakeDomainRepo{}
	_, err := DeleteZone(context.Background(), Deps{Domains: dr}, &models.Domain{ID: "d1", Name: "ex.com", EmailEnabled: true})
	require.ErrorIs(t, err, ErrAgentUnavailable)
	assert.Equal(t, 0, dr.flipCalls)
}

func TestEnableZone_Flip(t *testing.T) {
	dr := &fakeDomainRepo{}
	dom := &models.Domain{ID: "d1", Name: "ex.com", DNSDisabled: true}
	require.NoError(t, EnableZone(context.Background(), Deps{Domains: dr}, dom))
	assert.Equal(t, 1, dr.flipCalls)
	assert.False(t, dr.flippedTo, "flipped to enabled (dns_disabled=false)")
	assert.False(t, dom.DNSDisabled, "in-memory struct updated")
}

func TestEnableZone_Idempotent(t *testing.T) {
	dr := &fakeDomainRepo{}
	dom := &models.Domain{ID: "d1", Name: "ex.com", DNSDisabled: false}
	require.NoError(t, EnableZone(context.Background(), Deps{Domains: dr}, dom))
	assert.Equal(t, 0, dr.flipCalls, "already enabled → no write")
}

func TestEnableZone_FlipErr(t *testing.T) {
	dr := &fakeDomainRepo{flipErr: errors.New("db down")}
	dom := &models.Domain{ID: "d1", Name: "ex.com", DNSDisabled: true}
	require.Error(t, EnableZone(context.Background(), Deps{Domains: dr}, dom))
	assert.True(t, dom.DNSDisabled, "left disabled on write failure")
}
