package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The CLI doors that put a Docker app on a domain run the same name checks as
// the panel's API (alias, panel hostname and mail hostname). The edit door
// once left out the repositories behind them, so every name passed: an edit
// onto the panel's mail hostname went through on a test server.

func TestCLIDockerEditConfig_WiresTheDomainCheckRepos(t *testing.T) {
	// A handle that never connects: the constructors only keep it, and some
	// return nil for a nil handle.
	old := sharedDB
	sharedDB = &gorm.DB{}
	t.Cleanup(func() { sharedDB = old })
	cfg := cliDockerEditConfig(nil)
	if cfg.ServerSettings == nil {
		t.Error("no ServerSettings: the panel hostname and mail hostname check passes every name")
	}
	if cfg.WebDomainAliases == nil {
		t.Error("no WebDomainAliases: the alias check passes every name")
	}
	if cfg.DomainOwnership == nil {
		t.Error("no DomainOwnership: a new domain row gets no ownership state")
	}
	if cfg.Repo == nil || cfg.Domains == nil || cfg.Users == nil {
		t.Errorf("missing a core repo: %+v", cfg)
	}
}

// The tenant install checks a new name before it writes the app row; the
// row and the domain are only made once the name is known to be free.
func TestCLITenantInstall_ChecksTheNameBeforeCreatingTheApp(t *testing.T) {
	b, err := os.ReadFile("docker_app_tenant_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "func runTenantDockerInstall(")
	if start < 0 {
		t.Fatal("runTenantDockerInstall not found")
	}
	fn := src[start:]
	if end := strings.Index(fn, "\n}\n"); end > 0 {
		fn = fn[:end]
	}
	check := strings.Index(fn, "if cerr := cliTenantDomainCheck(ctx, existingDom, domain, user.ID,")
	create := strings.Index(fn, "repo.Create(ctx, app)")
	if check < 0 {
		t.Fatal("the tenant install does not run the domain name checks")
	}
	if create < 0 {
		t.Fatal("the app row create moved; update this test")
	}
	if check > create {
		t.Fatal("the domain name checks run after the app row is written")
	}
	if !strings.Contains(fn[check:create], "cerr != nil {\n\t\treturn fmt.Errorf(") {
		t.Fatal("a refused name does not stop the install")
	}
}

// The edit command uses the wired config, not one of its own.
func TestCLIDockerEdit_UsesTheWiredConfig(t *testing.T) {
	b, err := os.ReadFile("docker_app_edit_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "cfg := cliDockerEditConfig(cat)") {
		t.Fatal("the edit command does not use cliDockerEditConfig")
	}
	if n := strings.Count(src, "api.DockerAppHandlerConfig{"); n != 1 {
		t.Fatalf("%d handler configs in the edit command, want only cliDockerEditConfig's", n)
	}
}

type cliAliasHit struct {
	repository.WebDomainAliasRepository
	host string
}

func (a cliAliasHit) FindByHostname(_ context.Context, host string) (*models.WebDomainAlias, error) {
	if host == a.host {
		return &models.WebDomainAlias{Hostname: host}, nil
	}
	return nil, repository.ErrNotFound
}

type cliSettingsDown struct {
	repository.ServerSettingsRepository
}

func (cliSettingsDown) Get(context.Context) (*models.ServerSettings, error) {
	return nil, errors.New("database is down")
}

func TestCLITenantDomainCheck(t *testing.T) {
	ctx := context.Background()
	aliases := cliAliasHit{host: "shop.example.com"}
	for _, tc := range []struct {
		name     string
		existing *models.Domain
		settings repository.ServerSettingsRepository
		refused  bool
	}{
		{"wiki.example.com", nil, cliSettings{}, false},
		{"shop.example.com", nil, cliSettings{}, true},  // another domain's alias
		{"panel.example.com", nil, cliSettings{}, true}, // the panel's hostname
		{"wiki.example.com", nil, cliSettingsDown{}, true},
		// Attached, not created: the user's own domain is not checked again.
		{"panel.example.com", &models.Domain{ID: "d1", UserID: "u1", Name: "panel.example.com"}, cliSettingsDown{}, false},
	} {
		err := cliTenantDomainCheck(ctx, tc.existing, tc.name, "u1", aliases, tc.settings, cliDomains{})
		if refused := err != nil; refused != tc.refused {
			t.Errorf("%s (existing=%v): err = %v, want refused=%v", tc.name, tc.existing != nil, err, tc.refused)
		}
	}
}
