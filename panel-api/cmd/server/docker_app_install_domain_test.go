package main

import (
	"strings"
	"testing"
)

// `jabali docker-app install <slug> --domain x` without --user dropped the
// domain without a word and installed the app with none.
func TestDockerAppInstall_DomainWithoutUserIsRefused(t *testing.T) {
	cmd := newDockerAppInstallCmd()
	cmd.PreRunE = nil
	cmd.SetArgs([]string{"ghost", "--name", "blog", "--domain", "blog.example.com"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--domain needs --user") {
		t.Fatalf("want --domain without --user refused, got %v", err)
	}
}
