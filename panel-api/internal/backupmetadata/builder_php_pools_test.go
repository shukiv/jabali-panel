package backupmetadata

import (
	"context"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: an account has one PHP pool per version (GH #329): the default
// pool, and one more for each version a domain was switched to. The backup
// kept only the default pool, so a domain on another version came back
// unbound, on the default version (8.3 restored as 8.4).

type bpPools struct {
	repository.PHPPoolRepository
	rows []models.PHPPool
}

func (r *bpPools) FindByUserID(_ context.Context, userID string) (*models.PHPPool, error) {
	for i := range r.rows {
		if r.rows[i].UserID == userID {
			return &r.rows[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *bpPools) ListByUserID(_ context.Context, userID string) ([]models.PHPPool, error) {
	var out []models.PHPPool
	for _, p := range r.rows {
		if p.UserID == userID {
			out = append(out, p)
		}
	}
	return out, nil
}

type bpIni struct {
	repository.PHPPoolIniOverrideRepository
	byPool map[string][]models.PHPPoolIniOverride
}

func (r *bpIni) ListByPool(_ context.Context, poolID string) ([]models.PHPPoolIniOverride, error) {
	return r.byPool[poolID], nil
}

func TestBuild_KeepsEveryPHPPool(t *testing.T) {
	user := &models.User{ID: "u-alice", Email: "alice@example.com"}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	pools := &bpPools{rows: []models.PHPPool{
		{ID: "p-default", UserID: "u-alice", PHPVersion: "8.4", PmMode: "ondemand", PmMaxChildren: 5, CreatedAt: t0},
		{ID: "p-83", UserID: "u-alice", PHPVersion: "8.3", PmMode: "dynamic", PmMaxChildren: 8, CreatedAt: t0.Add(time.Hour)},
		{ID: "p-bob", UserID: "u-bob", PHPVersion: "8.2"},
	}}
	ini := &bpIni{byPool: map[string][]models.PHPPoolIniOverride{
		"p-default": {{ID: "o1", PoolID: "p-default", Directive: "memory_limit", Value: "256M", Kind: "value"}},
		"p-83":      {{ID: "o2", PoolID: "p-83", Directive: "max_execution_time", Value: "60", Kind: "value"}},
	}}

	m := Build(context.Background(), user, Deps{PHPPools: pools, PHPPoolIni: ini})

	if len(m.PHPPools) != 2 {
		t.Fatalf("php_pools = %+v, want the account's two pools", m.PHPPools)
	}
	for i, want := range []struct{ id, version, directive string }{
		{"p-default", "8.4", "memory_limit"},
		{"p-83", "8.3", "max_execution_time"},
	} {
		p := m.PHPPools[i]
		if p.ID != want.id || p.PHPVersion != want.version {
			t.Errorf("pool %d = %s/%s, want %s/%s (default first)", i, p.ID, p.PHPVersion, want.id, want.version)
		}
		if len(p.IniOverrides) != 1 || p.IniOverrides[0].Directive != want.directive {
			t.Errorf("pool %s ini overrides = %+v, want its own %s", p.ID, p.IniOverrides, want.directive)
		}
	}
	if m.PHPPools[1].PmMode != "dynamic" || m.PHPPools[1].PmMaxChildren != 8 {
		t.Errorf("8.3 pool tuning = %s/%d, want dynamic/8", m.PHPPools[1].PmMode, m.PHPPools[1].PmMaxChildren)
	}
}
