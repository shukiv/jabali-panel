package main

import (
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1637: `jabali mail-group create` refuses the address of the domain
// directory's host principal, like the REST handler.
func TestMailGroupCreateCmd_RefusesTheDirectoryAddress(t *testing.T) {
	src := stripLineComments(readGoSource(t, "mail_group_cmd.go"))
	if !strings.Contains(src, "err = mailaddr.CheckNotReservedOn(canonLocal, dom.IsPanelPrimary)") {
		t.Fatal("the CLI mail group create must refuse the reserved directory and postmaster addresses")
	}
}

// ADR-0110: `jabali mailbox forwarder add` refuses a postmaster alias on a
// tenant domain, like the REST handler.
func TestMailboxForwarderAddCmd_RefusesPostmaster(t *testing.T) {
	src := stripLineComments(readGoSource(t, "mailbox_extras_cmd.go"))
	if !strings.Contains(src, "if err := checkAliasLocalPart(fwdType, localPart, dom); err != nil {\n\t\t\t\treturn err\n\t\t\t}") {
		t.Fatal("the CLI alias add must call checkAliasLocalPart before it writes the row")
	}
	tenant := &models.Domain{Name: "tenant.example.com"}
	panel := &models.Domain{Name: "panel.example.com", IsPanelPrimary: true}
	for _, local := range []string{"postmaster", "Postmaster"} {
		if err := checkAliasLocalPart("alias", local, tenant); !errors.Is(err, mailaddr.ErrPostmasterReserved) {
			t.Errorf("alias %q on a tenant domain: got %v, want ErrPostmasterReserved", local, err)
		}
	}
	if err := checkAliasLocalPart("alias", "postmaster", panel); err != nil {
		t.Errorf("alias postmaster on the panel domain: %v", err)
	}
	if err := checkAliasLocalPart("alias", "sales", tenant); err != nil {
		t.Errorf("alias sales on a tenant domain: %v", err)
	}
	if err := checkAliasLocalPart("external", "postmaster", tenant); err != nil {
		t.Errorf("an external forward has no alias address: %v", err)
	}
}
