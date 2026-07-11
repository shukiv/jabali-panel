package cpanel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-57: privileged restore side effects must not outlive a failed panel row
// insert. These tests force the repository Create to fail *after* the mocked
// agent call succeeds and assert the compensating cleanup (db.drop /
// domain.delete) fires, so no invisible restored DB or unmanaged vhost is left.

// rollbackAgent records every command it receives (with the db_name / domain
// arg) and always succeeds, so the failure under test is the row insert.
type rollbackAgent struct{ calls []string }

func (a *rollbackAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	arg := ""
	switch m := params.(type) {
	case map[string]any:
		if v, ok := m["db_name"].(string); ok {
			arg = v
		}
	case map[string]string:
		if v, ok := m["domain"]; ok {
			arg = v
		}
	}
	a.calls = append(a.calls, command+":"+arg)
	return json.RawMessage(`{}`), nil
}

func (a *rollbackAgent) called(want string) bool {
	for _, c := range a.calls {
		if c == want {
			return true
		}
	}
	return false
}

// failingDBRepo: the DB does not already exist, but every Create fails.
type failingDBRepo struct{ repository.DatabaseRepository }

func (failingDBRepo) ExistsByUserAndName(context.Context, string, string) (bool, error) {
	return false, nil
}
func (failingDBRepo) Create(context.Context, *models.Database) error {
	return errors.New("simulated databases row insert failure")
}

func TestImportDatabases_RollsBackRestoredDBOnRowFailure(t *testing.T) {
	dir := t.TempDir()
	dump := filepath.Join(dir, "alice_shop.sql")
	if err := os.WriteFile(dump, []byte("-- dump\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := &rollbackAgent{}
	parsed := &ParsedTarball{MySQLDumps: []string{dump}, ExtractDir: dir, SourceUser: "alice"}

	res, err := ImportDatabases(context.Background(), failingDBRepo{}, nil, nil, ag,
		parsed, "01USERULID0000000000000000", "alice", false)
	if err != nil {
		t.Fatalf("ImportDatabases returned hard error, want per-entry skip: %v", err)
	}
	if res.Created != 0 {
		t.Fatalf("res.Created = %d, want 0 (row insert failed)", res.Created)
	}
	// db.create + db.restore ran, then the row failed → db.drop must roll it back.
	if !ag.called("db.drop:alice_shop") {
		t.Fatalf("expected compensating db.drop:alice_shop after row-insert failure; calls=%v", ag.calls)
	}
	// The skip note must explain the rollback (deterministic, not silent).
	joined := strings.Join(res.Skipped, "|")
	if !strings.Contains(joined, "rolled back") {
		t.Errorf("skip note should record the rollback; got %q", joined)
	}
}

// failingDomainRepo: the domain does not already exist, but every Create fails.
type failingDomainRepo struct{ repository.DomainRepository }

func (failingDomainRepo) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, errors.New("not found")
}
func (failingDomainRepo) Create(context.Context, *models.Domain) error {
	return errors.New("simulated domains row insert failure")
}

func TestImportDomains_RollsBackVhostOnRowFailure(t *testing.T) {
	ag := &rollbackAgent{}
	parsed := &ParsedTarball{DomainNames: []string{"example.com"}}

	res, err := ImportDomains(context.Background(), failingDomainRepo{}, ag,
		parsed, "01USERULID0000000000000000", "alice")
	if err != nil {
		t.Fatalf("ImportDomains returned hard error, want per-entry skip: %v", err)
	}
	if res.Created != 0 {
		t.Fatalf("res.Created = %d, want 0 (row insert failed)", res.Created)
	}
	// domain.create ran, then the row failed → domain.delete must roll it back.
	if !ag.called("domain.delete:example.com") {
		t.Fatalf("expected compensating domain.delete:example.com after row-insert failure; calls=%v", ag.calls)
	}
	joined := strings.Join(res.Skipped, "|")
	if !strings.Contains(joined, "vhost_rolled_back") {
		t.Errorf("skip note should record the vhost rollback; got %q", joined)
	}
}
