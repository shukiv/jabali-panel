package main

import (
	"os"
	"strings"
	"testing"
)

// GH #1903: `jabali docker-app start|restart|rebuild` brought a tenant app's
// on-disk compose up with no tenant gate. cmd/server has no DB/agent fixture
// (see docker_app_cmd_effectiveslug_test.go), so pin the wiring in source:
// every bring-up verb runs cliApplyTenantValidate, and stop does not.
func TestDockerAppLifecycleCmd_BringUpsCarryTheTenantGate(t *testing.T) {
	b, err := os.ReadFile("docker_app_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "func newDockerAppLifecycleCmd(")
	if start < 0 {
		t.Fatal("newDockerAppLifecycleCmd not found")
	}
	body := src[start:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	for _, want := range []string{
		`if verb != "stop" {`,
		`if verr := cliApplyTenantValidate(ctx, app, params); verr != nil {`,
		`sharedAgent.Call(ctx, "docker_app."+verb, params)`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("newDockerAppLifecycleCmd lost the tenant gate wiring: missing %q", want)
		}
	}
}
