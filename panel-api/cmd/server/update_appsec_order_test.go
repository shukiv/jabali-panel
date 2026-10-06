package main

import (
	"strings"
	"testing"
)

// The post-build appsec render reads the operator CRS exclusions and host
// modes from the database (crs_rule_exclusions, migration 000251;
// crs_host_modes, 000302). Before the migrations, a box updating across them
// doesn't have those tables, so the render leaves the operator CRS config as
// it was and the update only gets it right on the next run. It must run
// after them.
func TestUpdate_RerendersAppsecAfterMigrations(t *testing.T) {
	src := stripLineComments(readGoSource(t, "update.go"))
	migrate := strings.Index(src, `{"run migrations", func() error {`)
	render := strings.Index(src, `{"re-render appsec config (post-build)", func() error {`)
	if migrate < 0 || render < 0 {
		t.Fatalf("steps not found: run migrations at %d, re-render at %d", migrate, render)
	}
	if render < migrate {
		t.Fatal("the post-build appsec re-render must run after the migrations")
	}
	step := src[render:]
	if end := strings.Index(step[1:], `{"`); end > 0 {
		step = step[:end+1]
	}
	if !strings.Contains(step, `"appsec", "render-config", "--reconcile"`) {
		t.Fatal("the step must run appsec render-config --reconcile")
	}
}
