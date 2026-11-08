package main

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// A password the panel generates for the bootstrap tenant is not written to
// the journal; the operator is told how to set one instead.
func TestTenantBootstrapPassword_GeneratedIsNotLogged(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	pw := tenantBootstrapPassword(log, "tenant@example.test", "")

	if len(pw) < 20 {
		t.Fatalf("generated password %d chars, want at least 20", len(pw))
	}
	if strings.Contains(buf.String(), pw) {
		t.Fatalf("generated password written to the log:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "jabali user password tenant@example.test") {
		t.Errorf("log should tell the operator how to set a password; got:\n%s", buf.String())
	}
	if again := tenantBootstrapPassword(log, "tenant@example.test", ""); again == pw {
		t.Error("two generated passwords are equal")
	}
}

// A password the operator gave is used as is, and nothing is logged.
func TestTenantBootstrapPassword_OperatorPasswordKept(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	if got := tenantBootstrapPassword(log, "tenant@example.test", "operator-pw-placeholder"); got != "operator-pw-placeholder" {
		t.Fatalf("got %q, want the operator's password", got)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing should be logged; got:\n%s", buf.String())
	}
}

// The panel drops the bootstrap passwords from its environment once the
// bootstrap has run, so nothing it starts inherits them.
func TestClearBootstrapPasswordEnv(t *testing.T) {
	t.Setenv("JABALI_BOOTSTRAP_ADMIN_PASSWORD", "admin-pw-placeholder")
	t.Setenv("JABALI_BOOTSTRAP_TENANT_PASSWORD", "tenant-pw-placeholder")
	t.Setenv("JABALI_BOOTSTRAP_ADMIN_EMAIL", "admin@example.test")

	clearBootstrapPasswordEnv()

	for _, k := range []string{"JABALI_BOOTSTRAP_ADMIN_PASSWORD", "JABALI_BOOTSTRAP_TENANT_PASSWORD"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s still set (%d chars)", k, len(v))
		}
	}
	if v := os.Getenv("JABALI_BOOTSTRAP_ADMIN_EMAIL"); v != "admin@example.test" {
		t.Errorf("admin email should stay; got %q", v)
	}
}
