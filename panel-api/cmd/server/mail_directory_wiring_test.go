package main

import (
	"strings"
	"testing"
)

// GH #1637: the directory pass does nothing until serve wires its repos.
func TestServe_WiresTheMailDirectory(t *testing.T) {
	src := stripLineComments(readGoSource(t, "serve.go"))
	if !strings.Contains(src, "rec.WithMailDirectory(mailboxRepo, mailGroupRepo, repository.NewSharedResourceRepository(sharedDB))") {
		t.Fatal("serve must wire the mail directory reconciler pass")
	}
}
