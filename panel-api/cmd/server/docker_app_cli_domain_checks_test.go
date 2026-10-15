package main

import (
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"
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
	check := strings.Index(fn, "api.DockerDomainClaimCheck(")
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
	call := fn[check:]
	if !strings.Contains(call[:strings.Index(call, "\n\t\t}")], "return fmt.Errorf(") {
		t.Fatal("a refused name does not stop the install")
	}
}
