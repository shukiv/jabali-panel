package main

import (
	"strings"
	"testing"
)

// GH #1637: `jabali mail-group create` refuses the address of the domain
// directory's host principal, like the REST handler.
func TestMailGroupCreateCmd_RefusesTheDirectoryAddress(t *testing.T) {
	src := stripLineComments(readGoSource(t, "mail_group_cmd.go"))
	if !strings.Contains(src, "err = mailaddr.CheckNotReserved(canonLocal)") {
		t.Fatal("the CLI mail group create must refuse the reserved directory address")
	}
}
