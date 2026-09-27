package main

import (
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
)

// GH #1903: the CLI re-render (env set/regenerate, update) rendered a tenant
// app's compose with no tenant hardening, so it rewrote the app as an
// unsandboxed admin compose. renderForOwnerCLI keeps the hardening and returns
// the pinned service set for a tenant app, and leaves an admin app as it was.
func TestRenderForOwnerCLI_TenantKeepsTheSandbox(t *testing.T) {
	cat, _ := dockerapp.LoadDir("../../../install/docker-apps")
	entry, ok := cat.Get("freshrss")
	if !ok {
		t.Fatal("catalog entry freshrss not found")
	}
	params := dockerapp.RenderParams{
		Slug: "freshrss-t", ImageChannel: entry.ImageChannel, DataRoot: "/var/lib/jabali/docker-apps/freshrss-t",
		CPULimit: "1.0", MemoryLimit: "512m", PIDsLimit: 256,
	}

	compose, services, err := renderForOwnerCLI(entry, params, "bob")
	if err != nil {
		t.Fatalf("tenant render: %v", err)
	}
	for _, want := range []string{"cgroup_parent: jabali-user-bob.slice", "no-new-privileges:true", "- ALL"} {
		if !strings.Contains(compose, want) {
			t.Errorf("tenant compose lacks %q:\n%s", want, compose)
		}
	}
	if len(services) == 0 {
		t.Fatal("a tenant render must return the pinned service set")
	}

	compose, services, err = renderForOwnerCLI(entry, params, "")
	if err != nil {
		t.Fatalf("admin render: %v", err)
	}
	if strings.Contains(compose, "cgroup_parent") || services != nil {
		t.Fatalf("an admin render must stay unhardened and unpinned: services=%v\n%s", services, compose)
	}
}

// cmd/server has no DB/agent fixture (see docker_app_cmd_effectiveslug_test.go),
// so these pin the RunE wiring in source: every CLI door that writes a tenant
// compose carries the tenant gate and the pinned set.
func TestCLITenantComposeDoors_CarryTheGate(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		return string(b)
	}
	for file, snippets := range map[string][]string{
		"docker_app_cmd.go": {
			`if app.UserID != nil {
					return fmt.Errorf("re-render failed; refusing to update a tenant app from its unvalidated on-disk compose: %w", rerr)`,
			`updateParams["tenant_services"] = tenantServices`,
			`if verr := cliApplyTenantValidate(ctx, app, updateParams); verr != nil {`,
		},
		"docker_app_parity_cmd.go": {
			`params["tenant_services"] = tenantServices`,
		},
		"docker_app_tenant_cmd.go": {
			`"tenant_cgroup":               "jabali-user-" + username + ".slice",`,
			`"tenant_services":             tenantServices,`,
		},
	} {
		src := read(file)
		for _, s := range snippets {
			if !strings.Contains(src, s) {
				t.Errorf("%s lost the tenant gate wiring: missing %q", file, s)
			}
		}
	}
}
