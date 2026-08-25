package directadmin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeDomainRepo answers FindByName with a fixed domain; every other method
// panics if the code under test unexpectedly reaches for it.
type fakeDomainRepo struct {
	repository.DomainRepository
	dom *models.Domain
}

func (f *fakeDomainRepo) FindByName(_ context.Context, _ string) (*models.Domain, error) {
	return f.dom, nil
}

// fakeFwdRepo captures every forwarder ImportForwarders tries to create so the
// test can assert their Enabled state.
type fakeFwdRepo struct {
	repository.EmailForwarderRepository
	created []*models.EmailForwarder
}

func (f *fakeFwdRepo) Create(_ context.Context, fwd *models.EmailForwarder) error {
	f.created = append(f.created, fwd)
	return nil
}

// writeDAAliases lays out extractDir/etc/<domain>/aliases with one external
// forward and returns extractDir.
func writeDAAliases(t *testing.T, domain, line string) string {
	t.Helper()
	extract := t.TempDir()
	dir := filepath.Join(extract, "etc", domain)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aliases"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return extract
}

// JAB-46: imported DirectAdmin external forwarders must arrive INERT by default
// and only ACTIVE when the operator explicitly opts in — otherwise a compromised
// source keeps exfiltrating inbound mail after cutover.
func TestImportForwarders_InertByDefault(t *testing.T) {
	dom := &models.Domain{ID: "01DOMAINULID00000000000000", Name: "example.com"}

	for _, tc := range []struct {
		name        string
		preserve    bool
		wantEnabled bool
	}{
		{"default disabled", false, false},
		{"opt-in preserves active", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extract := writeDAAliases(t, "example.com", "info: attacker@evil.example")
			domRepo := &fakeDomainRepo{dom: dom}
			fwdRepo := &fakeFwdRepo{}

			res, err := ImportForwarders(context.Background(), fwdRepo, domRepo, extract, "alice", tc.preserve)
			if err != nil {
				t.Fatalf("ImportForwarders: %v", err)
			}
			if len(fwdRepo.created) != 1 {
				t.Fatalf("expected 1 forwarder created, got %d (res=%+v)", len(fwdRepo.created), res)
			}
			if got := fwdRepo.created[0].Enabled; got != tc.wantEnabled {
				t.Fatalf("forwarder Enabled = %v, want %v (preserve=%v)", got, tc.wantEnabled, tc.preserve)
			}
		})
	}
}

func (r *fakeDomainRepo) RewriteDocRootPrefix(context.Context, string, string, string) (int64, error) {
	return 0, nil
}
func (r *fakeDomainRepo) TransferOwner(context.Context, string, string, string) error { return nil }
