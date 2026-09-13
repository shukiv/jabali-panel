package cpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-404: the cpanel importer's php.pool.apply call omitted pm_mode (and
// process_idle_timeout_seconds), so the agent rejected every migrated pool and
// the domain silently fell back to the box default PHP version. These tests
// exercise ImportExtras end-to-end against a fake agent that MIRRORS the real
// panel-agent php.pool.apply validation, so the test is red on the unfixed map
// (agent rejects -> pool never created, domain never bound) and green only when
// the importer sends the pool row's tuning AND the reconciler-consistent slug.

// pmModeSet mirrors panel-agent/internal/commands/php_pool_apply.go: the valid
// pm_mode values.
var pmModeSet = map[string]bool{"static": true, "ondemand": true, "dynamic": true}

// toU32 coerces a JSON-ish param value to uint32 the way the agent's parse +
// validation would see it. The importer sends the pool row's uint32 fields
// directly, but be liberal about the numeric type so the mirror is faithful
// regardless of how the map was built.
func toU32(v any) uint32 {
	switch n := v.(type) {
	case uint32:
		return n
	case int:
		return uint32(n)
	case int64:
		return uint32(n)
	case float64:
		return uint32(n)
	default:
		return 0
	}
}

// poolApplyAgent records php.pool.apply params and mirrors the agent's
// argument validation (pm_mode, pm_max_children, process_idle_timeout_seconds,
// and the dynamic-pm spare-server sizing). php.version.install and any other
// verb succeed as no-ops.
type poolApplyAgent struct {
	applyCalls int
	lastApply  map[string]any
}

func (a *poolApplyAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	if command != "php.pool.apply" {
		return json.RawMessage(`{}`), nil
	}
	m, ok := params.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("php.pool.apply: unexpected params type %T", params)
	}
	a.applyCalls++
	a.lastApply = m

	// --- mirror of the agent's validation, in the same order ---
	pm, _ := m["pm_mode"].(string)
	if !pmModeSet[pm] {
		return nil, fmt.Errorf("invalid pm_mode (must be static, ondemand, or dynamic)")
	}
	if toU32(m["pm_max_children"]) == 0 {
		return nil, fmt.Errorf("pm_max_children must be > 0")
	}
	if toU32(m["process_idle_timeout_seconds"]) == 0 {
		return nil, fmt.Errorf("process_idle_timeout_seconds must be > 0")
	}
	if pm == "dynamic" {
		start := toU32(m["pm_start_servers"])
		minSpare := toU32(m["pm_min_spare_servers"])
		maxSpare := toU32(m["pm_max_spare_servers"])
		maxChildren := toU32(m["pm_max_children"])
		if start == 0 || minSpare == 0 || maxSpare == 0 {
			return nil, fmt.Errorf("dynamic pm requires pm_start_servers, pm_min_spare_servers, pm_max_spare_servers > 0")
		}
		if !(minSpare <= start && start <= maxSpare && maxSpare <= maxChildren) {
			return nil, fmt.Errorf("dynamic pm requires pm_min_spare_servers <= pm_start_servers <= pm_max_spare_servers <= pm_max_children")
		}
	}
	return json.RawMessage(`{"socket_path":"/run/php/jabali-someuser/fpm.sock","pool_name":"jabali-someuser"}`), nil
}

// createPoolRepo: no (user, version) pool exists yet, so ImportExtras creates
// one (the ondemand default) and then applies it. ListByUserID returns what has
// been created so far, so the freshly-created pool resolves as the default.
type createPoolRepo struct {
	repository.PHPPoolRepository
	created []*models.PHPPool
}

func (r *createPoolRepo) FindByUserAndVersion(context.Context, string, string) (*models.PHPPool, error) {
	return nil, repository.ErrNotFound
}
func (r *createPoolRepo) Create(_ context.Context, p *models.PHPPool) error {
	r.created = append(r.created, p)
	return nil
}
func (r *createPoolRepo) ListByUserID(context.Context, string) ([]models.PHPPool, error) {
	out := make([]models.PHPPool, 0, len(r.created))
	for _, p := range r.created {
		out = append(out, *p)
	}
	return out, nil
}

// existingDynamicPoolRepo returns a pre-existing pool the tenant already tuned
// to dynamic pm. The importer must send that row's spare-server sizing so the
// agent's dynamic-pm validation passes — sending only pm_mode would fail the
// "dynamic pm requires ..." check, which is why the fix builds the whole map
// from the row.
type existingDynamicPoolRepo struct {
	repository.PHPPoolRepository
	pool *models.PHPPool
}

func (r *existingDynamicPoolRepo) FindByUserAndVersion(context.Context, string, string) (*models.PHPPool, error) {
	return r.pool, nil
}
func (r *existingDynamicPoolRepo) ListByUserID(context.Context, string) ([]models.PHPPool, error) {
	return []models.PHPPool{*r.pool}, nil
}

// nonDefaultPoolRepo models a user who ALREADY has an earlier (default) pool —
// e.g. the box-default 8.4 pool provisioned before the import. The version
// being imported is therefore NOT the earliest, so it must get a versioned slug
// and additive=true; a default slug here would glob-delete the 8.4 default's
// conf. list[0] is the older pool.
type nonDefaultPoolRepo struct {
	repository.PHPPoolRepository
	older   models.PHPPool
	created []*models.PHPPool
}

func (r *nonDefaultPoolRepo) FindByUserAndVersion(context.Context, string, string) (*models.PHPPool, error) {
	return nil, repository.ErrNotFound
}
func (r *nonDefaultPoolRepo) Create(_ context.Context, p *models.PHPPool) error {
	r.created = append(r.created, p)
	return nil
}
func (r *nonDefaultPoolRepo) ListByUserID(context.Context, string) ([]models.PHPPool, error) {
	out := []models.PHPPool{r.older}
	for _, p := range r.created {
		out = append(out, *p)
	}
	return out, nil
}

// bindDomainRepo returns a domain row for any name (so the bind step runs) and
// records SetPHPPoolID calls.
type bindDomainRepo struct {
	repository.DomainRepository
	bound map[string]string // domainID -> poolID
}

func (r *bindDomainRepo) FindByName(_ context.Context, name string) (*models.Domain, error) {
	return &models.Domain{ID: "dom-" + name, Name: name}, nil
}
func (r *bindDomainRepo) SetPHPPoolID(_ context.Context, id string, poolID *string) error {
	if r.bound == nil {
		r.bound = map[string]string{}
	}
	if poolID != nil {
		r.bound[id] = *poolID
	}
	return nil
}

func hasSkipPrefix(skipped []string, prefix string) bool {
	for _, s := range skipped {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// TestImportExtras_PoolApplyCarriesPmMode is the JAB-404 regression guard.
// A freshly-created ondemand pool must apply: the map must carry pm_mode and
// process_idle_timeout_seconds, the pool must be created, the domain bound, and
// the applied version reported (not left blank, the user-visible symptom). The
// only pool for the user is the default, so the slug is the bare username.
func TestImportExtras_PoolApplyCarriesPmMode(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"app.example": "ea-php81"})
	ag := &poolApplyAgent{}
	pools := &createPoolRepo{}
	domains := &bindDomainRepo{}

	res, err := ImportExtras(
		context.Background(),
		domains, // domainsRepo
		nil,     // mailboxesRepo
		nil,     // forwardersRepo
		nil,     // autoRespondersRepo
		nil,     // filtersRepo
		pools,   // poolsRepo
		ag,      // agentCli
		parsed,
		"user-123", "someuser",
		false, // preserveMailRouting
	)
	if err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}

	if ag.applyCalls != 1 {
		t.Fatalf("php.pool.apply calls = %d, want 1", ag.applyCalls)
	}
	if ag.lastApply == nil {
		t.Fatal("no php.pool.apply params captured")
	}
	if pm, _ := ag.lastApply["pm_mode"].(string); pm != "ondemand" {
		t.Errorf("pm_mode = %q, want ondemand", pm)
	}
	if got := toU32(ag.lastApply["process_idle_timeout_seconds"]); got == 0 {
		t.Errorf("process_idle_timeout_seconds = %d, want > 0 (the second validation the old map failed)", got)
	}
	if got := toU32(ag.lastApply["pm_max_children"]); got == 0 {
		t.Errorf("pm_max_children = %d, want > 0", got)
	}
	// Only pool for the user -> default -> slug == username, non-additive.
	if slug, _ := ag.lastApply["slug"].(string); slug != "someuser" {
		t.Errorf("slug = %q, want someuser (the default pool)", slug)
	}
	if add, _ := ag.lastApply["additive"].(bool); add != false {
		t.Errorf("additive = %v, want false for the default pool", add)
	}

	if res.PHPPoolsCreated != 1 {
		t.Errorf("PHPPoolsCreated = %d, want 1", res.PHPPoolsCreated)
	}
	if res.PHPVersionApplied != "8.1" {
		t.Errorf("PHPVersionApplied = %q, want 8.1 (blank = the domain silently fell back to the box default)", res.PHPVersionApplied)
	}
	if hasSkipPrefix(res.Skipped, "php_pool_apply_skip") {
		t.Errorf("php_pool_apply_skip present (agent rejected the pool): %v", res.Skipped)
	}
	if len(pools.created) != 1 {
		t.Fatalf("pools created = %d, want 1", len(pools.created))
	}
	if got := domains.bound["dom-app.example"]; got != pools.created[0].ID {
		t.Errorf("domain not bound to created pool: bound=%v poolID=%s", domains.bound, pools.created[0].ID)
	}
}

// TestImportExtras_PoolApplyDynamicRowSendsSpareServers covers a looked-up pool
// the tenant already set to dynamic pm. The fix must forward the row's
// spare-server sizing, or the agent's dynamic-pm validation rejects the apply.
func TestImportExtras_PoolApplyDynamicRowSendsSpareServers(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"dyn.example": "ea-php82"})
	ag := &poolApplyAgent{}
	pools := &existingDynamicPoolRepo{pool: &models.PHPPool{
		ID:                        "pool-dyn",
		UserID:                    "user-123",
		PHPVersion:                "8.2",
		PmMode:                    "dynamic",
		PmMaxChildren:             20,
		ProcessIdleTimeoutSeconds: 60,
		PmStartServers:            2,
		PmMinSpareServers:         1,
		PmMaxSpareServers:         3,
		Status:                    "ready",
	}}
	domains := &bindDomainRepo{}

	res, err := ImportExtras(
		context.Background(),
		domains, nil, nil, nil, nil, pools, ag, parsed,
		"user-123", "someuser", false,
	)
	if err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}
	if ag.applyCalls != 1 || ag.lastApply == nil {
		t.Fatalf("php.pool.apply calls = %d (params captured: %v)", ag.applyCalls, ag.lastApply != nil)
	}
	if pm, _ := ag.lastApply["pm_mode"].(string); pm != "dynamic" {
		t.Errorf("pm_mode = %q, want dynamic", pm)
	}
	if toU32(ag.lastApply["pm_start_servers"]) == 0 ||
		toU32(ag.lastApply["pm_min_spare_servers"]) == 0 ||
		toU32(ag.lastApply["pm_max_spare_servers"]) == 0 {
		t.Errorf("dynamic pool missing spare-server sizing: %v", ag.lastApply)
	}
	if res.PHPVersionApplied != "8.2" {
		t.Errorf("PHPVersionApplied = %q, want 8.2", res.PHPVersionApplied)
	}
	if hasSkipPrefix(res.Skipped, "php_pool_apply_skip") {
		t.Errorf("dynamic pool rejected by agent validation: %v", res.Skipped)
	}
}

// TestImportExtras_PoolApplyNonDefaultUsesVersionedSlug proves the importer does
// NOT clobber a pre-existing (earlier) default pool: when the user already has
// an older pool, the imported version is not the default, so it must apply under
// a versioned slug with additive=true. A bare-username slug here would
// glob-delete the older default's conf from every other version dir.
func TestImportExtras_PoolApplyNonDefaultUsesVersionedSlug(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"new.example": "ea-php81"})
	ag := &poolApplyAgent{}
	pools := &nonDefaultPoolRepo{older: models.PHPPool{
		ID:         "older-default-84",
		UserID:     "user-123",
		PHPVersion: "8.4",
	}}
	domains := &bindDomainRepo{}

	res, err := ImportExtras(
		context.Background(),
		domains, nil, nil, nil, nil, pools, ag, parsed,
		"user-123", "someuser", false,
	)
	if err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}
	if ag.lastApply == nil {
		t.Fatal("no php.pool.apply params captured")
	}
	wantSlug := models.PoolSlug("someuser", "8.1", false) // "someuser-php8.1"
	if slug, _ := ag.lastApply["slug"].(string); slug != wantSlug {
		t.Errorf("slug = %q, want %q (versioned — must not clobber the older default)", slug, wantSlug)
	}
	if add, _ := ag.lastApply["additive"].(bool); add != true {
		t.Errorf("additive = %v, want true for a non-default pool", add)
	}
	if res.PHPVersionApplied != "8.1" {
		t.Errorf("PHPVersionApplied = %q, want 8.1", res.PHPVersionApplied)
	}
	if hasSkipPrefix(res.Skipped, "php_pool_apply_skip") {
		t.Errorf("non-default pool rejected: %v", res.Skipped)
	}
}
