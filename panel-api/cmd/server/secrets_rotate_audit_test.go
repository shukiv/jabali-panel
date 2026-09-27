package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// JAB-357: docs/secret-rotation.md promises that every rotation writes a
// secrets.rotate.* audit event. cliAudit is a silent no-op while sharedDB is
// nil, so every rotate subcommand must open the panel DB before it runs.
func TestSecretsRotateSubcommandsOpenTheAuditDB(t *testing.T) {
	rotate := newSecretsRotateCmd()
	subs := rotate.Commands()
	if len(subs) == 0 {
		t.Fatal("no rotate subcommands")
	}
	for _, sub := range subs {
		if sub.PreRunE == nil {
			t.Errorf("secrets rotate %s has no PreRunE: its audit event is silently dropped", sub.Name())
		}
	}
}

// A remediation must run even when the panel DB or its config is
// unavailable, so opening the audit DB never fails the command; the operator
// is told the audit event will be missing instead.
func TestRotateAuditPreRun_NeverBlocksAndWarns(t *testing.T) {
	cases := []struct {
		name   string
		config string // written to the config file; "" leaves it absent
	}{
		// A missing config file loads defaults; the DB then has no URL.
		{name: "no database URL"},
		{name: "config does not load", config: "[[[not toml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			savedPath, savedCfg, savedDB := cfgPath, sharedCfg, sharedDB
			t.Cleanup(func() { cfgPath, sharedCfg, sharedDB = savedPath, savedCfg, savedDB })
			cfgPath = filepath.Join(t.TempDir(), "panel.toml")
			if tc.config != "" {
				if err := os.WriteFile(cfgPath, []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			sharedCfg, sharedDB = nil, nil
			t.Setenv("DATABASE_URL", "")

			cmd := &cobra.Command{}
			var errOut bytes.Buffer
			cmd.SetErr(&errOut)

			if err := rotateAuditPreRun(cmd, nil); err != nil {
				t.Fatalf("rotateAuditPreRun = %v, want nil: an audit DB problem must not block a rotation", err)
			}
			if sharedDB != nil {
				t.Fatal("no DB should have been opened")
			}
			if !strings.Contains(errOut.String(), "no audit event") {
				t.Fatalf("stderr = %q, want a warning that no audit event will be recorded", errOut.String())
			}
		})
	}
}
