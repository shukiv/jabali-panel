package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2261: jabali-cache-doctor.timer runs `jabali app cache-doctor-run` every
// hour on every server. These pin that a server with no cache-enabled
// WordPress site gets no run recorded, while one with a site still does.

type doctorRunInstalls struct {
	repository.ApplicationInstallRepository
	rows []models.ApplicationInstall
}

func (r *doctorRunInstalls) List(context.Context, repository.ListOptions) ([]models.ApplicationInstall, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}

type doctorRunRuns struct {
	repository.CacheDoctorRunRepository
	created []models.CacheDoctorRun
	last    models.CacheDoctorRun
}

func (r *doctorRunRuns) Create(_ context.Context, run *models.CacheDoctorRun) error {
	r.created = append(r.created, *run)
	r.last = *run
	return nil
}

func (r *doctorRunRuns) Update(_ context.Context, run *models.CacheDoctorRun) error {
	r.last = *run
	return nil
}

type doctorRunDomains struct{ repository.DomainRepository }

func (doctorRunDomains) FindByID(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}

func doctorRunConfig(rows ...models.ApplicationInstall) (api.ApplicationHandlerConfig, *doctorRunRuns) {
	runs := &doctorRunRuns{}
	return api.ApplicationHandlerConfig{
		ApplicationInstalls: &doctorRunInstalls{rows: rows},
		CacheDoctorRuns:     runs,
		Domains:             doctorRunDomains{},
	}, runs
}

func TestRunRecordedCacheDoctor_NoCacheSiteRecordsNothing(t *testing.T) {
	cfg, runs := doctorRunConfig(
		models.ApplicationInstall{ID: "a", AppType: "wordpress", CacheEnabled: false, Status: "ready"},
		models.ApplicationInstall{ID: "b", AppType: "wordpress", CacheEnabled: true, Status: "installing"},
		models.ApplicationInstall{ID: "c", AppType: "nextcloud", CacheEnabled: true, Status: "ready"},
	)
	var out bytes.Buffer
	// The timer's kind. No Redis either: with nothing to check, that is not
	// worth failing the unit for every hour.
	if err := runRecordedCacheDoctor(context.Background(), cfg, models.CacheDoctorKindRepair, &out); err != nil {
		t.Fatalf("err = %v, want a quiet skip", err)
	}
	if len(runs.created) != 0 {
		t.Errorf("recorded %d runs, want none without a cache-enabled WordPress site", len(runs.created))
	}
	if !strings.Contains(out.String(), "nothing to check") {
		t.Errorf("output %q should say there was nothing to check", out.String())
	}
}

func TestRunRecordedCacheDoctor_CacheSiteRecordsARun(t *testing.T) {
	cfg, runs := doctorRunConfig(
		models.ApplicationInstall{ID: "a", AppType: "wordpress", CacheEnabled: false, Status: "ready"},
		models.ApplicationInstall{ID: "b", AppType: "wordpress", CacheEnabled: true, Status: "ready"},
	)
	var out bytes.Buffer
	if err := runRecordedCacheDoctor(context.Background(), cfg, models.CacheDoctorKindDoctor, &out); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(runs.created) != 1 {
		t.Fatalf("recorded %d runs, want 1", len(runs.created))
	}
	if runs.last.State != models.CacheDoctorStateDone || runs.last.Total != 1 {
		t.Errorf("run state %q total %d, want done with the one cache site", runs.last.State, runs.last.Total)
	}
}
