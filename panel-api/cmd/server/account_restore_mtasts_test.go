package main

import (
	"os"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: the CLI restore runs RestoreBundleDNS after Apply, so it tells
// Apply it restores DNS (a domain's MTA-STS then comes back on), and it runs
// it for a domain whose MTA-STS the backup had on even when the domain has no
// custom records: that is what publishes the MTA-STS records.

func TestBundleHasDNSRecords_CountsADomainWithMTASTS(t *testing.T) {
	meta := &internalbackup.AccountMetadata{Domains: []internalbackup.MetadataDomain{{Name: "a.org"}, {Name: "b.org", MTASTSEnabled: true}}}
	if !bundleHasDNSRecords(meta) {
		t.Fatal("a backup with an MTA-STS domain must run the DNS step")
	}
	if bundleHasDNSRecords(&internalbackup.AccountMetadata{Domains: []internalbackup.MetadataDomain{{Name: "a.org"}}}) {
		t.Fatal("a backup with neither records nor MTA-STS needs no DNS step")
	}
}

func TestAccountRestoreCLI_TellsApplyItRestoresDNS(t *testing.T) {
	src, err := os.ReadFile("account_restore_cmd.go")
	if err != nil || !strings.Contains(string(src), "RestoresDNS: true") {
		t.Fatal("the CLI restore must set backupmetadata.Deps.RestoresDNS: it runs RestoreBundleDNS after Apply")
	}
}
