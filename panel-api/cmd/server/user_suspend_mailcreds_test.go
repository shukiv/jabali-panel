package main

import (
	"testing"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailcreds"
)

// `jabali user suspend` removes the user's mail app passwords and API keys
// at once, like the admin API: the CLI suspend wires the same sweeper. Not
// parallel: it swaps the package database.
func TestCLISuspendDeps_WiresTheMailCredentialsSweeper(t *testing.T) {
	savedCfg, savedDB := sharedCfg, sharedDB
	t.Cleanup(func() { sharedCfg, sharedDB = savedCfg, savedDB })
	sharedCfg = nil

	sharedDB = &gorm.DB{}
	sw, ok := cliSuspendDeps().MailCredentials.(mailcreds.Sweeper)
	if !ok || sw.Registry == nil || sw.Logins == nil {
		t.Fatalf("MailCredentials = %#v, want a wired mailcreds.Sweeper", cliSuspendDeps().MailCredentials)
	}

	sharedDB = nil
	if d := cliSuspendDeps(); d.MailCredentials != nil {
		t.Fatalf("MailCredentials = %#v without a database, want nil", d.MailCredentials)
	}
}
