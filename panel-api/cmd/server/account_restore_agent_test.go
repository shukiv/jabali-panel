package main

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
)

// The CLI restore hands the shared agent client to the panel rebuild, which
// creates the restored database users' accounts and pushes the forwarders
// through it. Left unset, the rebuild got a nil client and the restore
// crashed after the database rows were written.
func TestAccountRestoreCLI_SetsUpTheAgentClient(t *testing.T) {
	savedDB, savedCfg, savedAgent := sharedDB, sharedCfg, sharedAgent
	t.Cleanup(func() { sharedDB, sharedCfg, sharedAgent = savedDB, savedCfg, savedAgent })
	sharedDB = &gorm.DB{} // set, so initDB doesn't connect; nothing reads it before the check below
	sharedCfg = &config.Config{}
	sharedAgent = nil

	cmd := newBackupAccountRestoreCmd()
	cmd.SetArgs([]string{"--force", "--snapshot", "abc123"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--destination required") {
		t.Fatalf("err = %v, want the missing --destination refusal", err)
	}
	if sharedAgent == nil {
		t.Fatal("account-restore ran without setting up the agent client")
	}
}
