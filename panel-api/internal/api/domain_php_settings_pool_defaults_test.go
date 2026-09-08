package api

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1543 (johnnyq): the per-domain PHP tab should show the real inherited
// value per directive ("256M (Default)"), not a generic "Pool default". The
// backend resolves that as the box php.ini baseline (agent) with the pool's own
// ini overrides layered on top.

type pdFakeAgent struct{ defaults map[string]string }

func (f *pdFakeAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	if cmd != "php.ini_defaults" {
		return nil, nil
	}
	return json.Marshal(map[string]any{"php_version": "8.3", "defaults": f.defaults})
}

type pdFakeOverrides struct {
	repository.PHPPoolIniOverrideRepository
	rows []models.PHPPoolIniOverride
}

func (f *pdFakeOverrides) ListByPool(_ context.Context, _ string) ([]models.PHPPoolIniOverride, error) {
	return f.rows, nil
}

func TestResolvePoolDefaults_OverlaysPoolOverridesOnBaseline(t *testing.T) {
	// Bust the process cache so this test isn't served a stale baseline from a
	// sibling test / earlier run of the same version key.
	poolIniDefaultMu.Lock()
	delete(poolIniDefaultCache, "8.3")
	poolIniDefaultMu.Unlock()

	h := &domainPHPSettingsHandler{cfg: DomainPHPSettingsHandlerConfig{
		Agent: &pdFakeAgent{defaults: map[string]string{
			"memory_limit":        "128M",
			"upload_max_filesize": "2M",
			"post_max_size":       "8M",
			"max_input_vars":      "1000",
			"max_execution_time":  "30",
			"max_input_time":      "60",
		}},
		PoolIniOverrides: &pdFakeOverrides{rows: []models.PHPPoolIniOverride{
			// The pool bumps memory_limit; everything else inherits the baseline.
			{Directive: "memory_limit", Value: "512M", Kind: "value"},
			// A flag-kind override must NOT land in these size/int directives.
			{Directive: "display_errors", Value: "on", Kind: "flag"},
		}},
	}}

	got := h.resolvePoolDefaults(context.Background(), &models.PHPPool{ID: "p1", PHPVersion: "8.3"})
	if got == nil {
		t.Fatal("resolvePoolDefaults returned nil with a live agent + overrides")
	}
	if got["memory_limit"] != "512M" {
		t.Errorf("memory_limit = %q, want 512M (pool override wins over baseline)", got["memory_limit"])
	}
	if got["upload_max_filesize"] != "2M" {
		t.Errorf("upload_max_filesize = %q, want 2M (inherited baseline)", got["upload_max_filesize"])
	}
	if _, leaked := got["display_errors"]; leaked {
		t.Error("a flag-kind override leaked into the size/int defaults map")
	}
}

func TestResolvePoolDefaults_NilAgentDegrades(t *testing.T) {
	h := &domainPHPSettingsHandler{cfg: DomainPHPSettingsHandlerConfig{}}
	if got := h.resolvePoolDefaults(context.Background(), &models.PHPPool{ID: "p1", PHPVersion: "8.3"}); got != nil {
		t.Errorf("no agent should omit pool_defaults, got %v", got)
	}
}
