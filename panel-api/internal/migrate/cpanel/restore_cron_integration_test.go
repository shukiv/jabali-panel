package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeCronRepo records created rows + satisfies CronJobRepository.
type fakeCronRepo struct{ rows []*models.CronJob }

func (r *fakeCronRepo) Create(_ context.Context, j *models.CronJob) error {
	r.rows = append(r.rows, j)
	return nil
}
func (r *fakeCronRepo) FindByID(context.Context, string) (*models.CronJob, error) { return nil, nil }
func (r *fakeCronRepo) ListByUserID(context.Context, string) ([]*models.CronJob, error) {
	return nil, nil
}
func (r *fakeCronRepo) ListAll(context.Context) ([]*models.CronJob, error) { return nil, nil }
func (r *fakeCronRepo) Update(context.Context, *models.CronJob) error      { return nil }
func (r *fakeCronRepo) UpdateStatus(context.Context, string, time.Time, int, string) error {
	return nil
}
func (r *fakeCronRepo) Delete(context.Context, string) error { return nil }

// TestImportCron_CurlRewriteEndToEnd drives the full ImportCron loop with
// a crontab containing the spec's representative lines and asserts the
// resulting rows + their enabled/command state.
func TestImportCron_CurlRewriteEndToEnd(t *testing.T) {
	home := t.TempDir()
	// DEST docroot the rewrite resolves to: /home/<user>/domains/<d>/public_html.
	// We point DocRoots at a real temp dir holding the .php files so rule 5
	// ("must exist") passes without faking /home.
	docroot := filepath.Join(home, "docroot")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"cron-pong.php", "wp-cron.php"} {
		if err := os.WriteFile(filepath.Join(docroot, f), []byte("<?php\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cronFile := filepath.Join(home, "crontab")
	crontab := strings.Join([]string{
		`MAILTO=""`, // env → ignored
		`*/5 * * * * curl https://own.example.com/cron-pong.php >/dev/null 2>&1`,      // rewrite
		`0 * * * * wget -q -O /dev/null https://own.example.com/wp-cron.php`,          // rewrite
		`0 0 * * * curl https://own.example.com/cron-pong.php?tok=1`,                  // disabled (query)
		`0 0 * * * curl https://evil.com/x.php`,                                       // disabled (foreign)
		`0 0 * * * php ` + filepath.Join(docroot, "wp-cron.php") + ` >/dev/null 2>&1`, // valid php, redirect stripped
	}, "\n") + "\n"
	if err := os.WriteFile(cronFile, []byte(crontab), 0o644); err != nil {
		t.Fatal(err)
	}

	parsed := &ParsedTarball{
		CronFiles:   []string{cronFile},
		DomainNames: []string{"own.example.com"},
		DocRoots:    map[string]string{"own.example.com": docroot},
	}
	repo := &fakeCronRepo{}
	res, err := ImportCron(context.Background(), repo, parsed, "user-ulid", "alice")
	if err != nil {
		t.Fatalf("ImportCron: %v", err)
	}

	// 5 rows: 2 rewritten + 2 disabled-import + 1 valid php. (MAILTO is env-skipped.)
	if len(repo.rows) != 5 {
		t.Fatalf("created %d rows, want 5: %+v", len(repo.rows), repo.rows)
	}
	for _, row := range repo.rows {
		if row.Enabled {
			t.Errorf("imported cron must be disabled, got enabled: %q", row.Command)
		}
	}

	cmds := map[string]bool{}
	for _, row := range repo.rows {
		cmds[row.Command] = true
	}
	// Rewritten commands present.
	if !cmds["php "+filepath.Join(docroot, "cron-pong.php")] {
		t.Errorf("missing rewritten cron-pong row; got %v", cmds)
	}
	if !cmds["php "+filepath.Join(docroot, "wp-cron.php")] {
		t.Errorf("missing rewritten/valid wp-cron row; got %v", cmds)
	}
	// Disabled-import preserves the ORIGINAL command verbatim.
	if !cmds["curl https://own.example.com/cron-pong.php?tok=1"] {
		t.Errorf("query-string cron should be disabled-imported with original; got %v", cmds)
	}
	if !cmds["curl https://evil.com/x.php"] {
		t.Errorf("foreign-host cron should be disabled-imported with original; got %v", cmds)
	}

	// Manifest notes: at least one rewrite + one disabled_import line.
	var rewrote, disabled bool
	for _, sk := range res.Skipped {
		if strings.Contains(sk, "cron rewritten curl→php") {
			rewrote = true
		}
		if strings.Contains(sk, "cron_disabled_import") {
			disabled = true
		}
	}
	if !rewrote {
		t.Errorf("expected a 'cron rewritten' manifest note; got %v", res.Skipped)
	}
	if !disabled {
		t.Errorf("expected a 'cron_disabled_import' manifest note; got %v", res.Skipped)
	}
}
