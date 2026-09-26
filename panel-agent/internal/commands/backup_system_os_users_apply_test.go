package commands

import (
	"strings"
	"testing"
)

// GH #331: the os_users merge parsers are the safety boundary — a malformed
// line must warn, never crash or half-apply.
func TestParsePasswdLine(t *testing.T) {
	e, err := parsePasswdLine("shukivaknin:x:1000:1000:Shuki:/home/shukivaknin:/bin/bash")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "shukivaknin" || e.UID != 1000 || e.GID != 1000 ||
		e.Home != "/home/shukivaknin" || e.Shell != "/bin/bash" {
		t.Errorf("parsed wrong: %+v", e)
	}
	for _, bad := range []string{"", "a:b", "u:x:notnum:1::/h:/s", "u:x:1:notnum::/h:/s"} {
		if _, err := parsePasswdLine(bad); err == nil {
			t.Errorf("line %q must fail", bad)
		}
	}
}

func TestParseGroupLine(t *testing.T) {
	name, gid, members, err := parseGroupLine("jabali-sftp:x:986:shukivaknin, demolab_ftp")
	if err != nil {
		t.Fatal(err)
	}
	if name != "jabali-sftp" || gid != 986 {
		t.Errorf("got %s/%d", name, gid)
	}
	if strings.Join(members, "|") != "shukivaknin|demolab_ftp" {
		t.Errorf("members: %v", members)
	}
	if _, _, m, err := parseGroupLine("g:x:1:"); err != nil || len(m) != 0 {
		t.Errorf("empty member list must parse clean, got %v %v", m, err)
	}
}

func TestShadowHashes(t *testing.T) {
	h := shadowHashes([]string{
		"u1:$y$j9T$abc$def:19000:0:99999:7:::",
		"locked:!:1:0:99999:7:::",
		"malformed",
	})
	if h["u1"] != "$y$j9T$abc$def" || h["locked"] != "!" {
		t.Errorf("hashes: %v", h)
	}
}

// JAB-357: the system backup captures the /etc/group line of every allowlisted
// group, member list included. A restore that re-adds those members verbatim
// re-grants whatever the source host had — a pre-fix host still carried
// jabali-webmail in the broad jabali group, the group that owns the root Agent
// socket — and a tampered backup could name any local account. install.sh owns
// the members of the privileged service groups; a restore must never add to
// them, nor create a restored user with one as its primary group.
func TestPlanMembershipRestores_NeverGrantsInstallerManagedGroups(t *testing.T) {
	local := map[string]bool{
		"jabali": true, "jabali-sockets": true, "jabali-mail": true, "pdns": true, "jabali-sftp": true,
		"jabali-webmail": true, "tenant1": true,
	}
	exists := func(n string) bool { return local[n] }
	groups := []wantedGroup{
		{name: "jabali", members: []string{"jabali-webmail", "tenant1"}},
		{name: "jabali-sockets", members: []string{"tenant1"}},
		{name: "jabali-mail", members: []string{"tenant1"}},
		{name: "pdns", members: []string{"tenant1"}},
		{name: "jabali-sftp", members: []string{"tenant1"}},
	}
	adds, warnings := planMembershipRestores(groups, exists, exists)
	if len(adds) != 1 || adds[0] != (membershipAdd{group: "jabali-sftp", member: "tenant1"}) {
		t.Fatalf("adds = %+v, want only jabali-sftp/tenant1 — a restore must not add members to an installer-managed group", adds)
	}
	for _, refused := range []string{
		`"jabali-webmail" in group "jabali"`, `"tenant1" in group "jabali"`,
		`"tenant1" in group "jabali-sockets"`, `"tenant1" in group "jabali-mail"`, `"tenant1" in group "pdns"`,
	} {
		found := false
		for _, w := range warnings {
			if strings.Contains(w, refused) {
				found = true
			}
		}
		if !found {
			t.Errorf("no warning names the refused membership %s; warnings = %q", refused, warnings)
		}
	}
}

func TestPlanMembershipRestores_SkipsMissingSidesSilently(t *testing.T) {
	local := map[string]bool{"jabali-sftp": true, "tenant1": true}
	exists := func(n string) bool { return local[n] }
	adds, warnings := planMembershipRestores([]wantedGroup{
		{name: "jabali-sftp", members: []string{"tenant1", "ghost"}},
		{name: "gone-group", members: []string{"tenant1"}},
	}, exists, exists)
	if len(adds) != 1 || adds[0] != (membershipAdd{group: "jabali-sftp", member: "tenant1"}) {
		t.Fatalf("adds = %+v, want only jabali-sftp/tenant1", adds)
	}
	if len(warnings) != 0 {
		t.Fatalf("a membership whose member or group does not exist is skipped without a warning; got %q", warnings)
	}
}

func TestRestorePrimaryGID_RefusesInstallerManagedGroup(t *testing.T) {
	names := map[string]string{"989": "jabali", "990": "jabali-sockets", "991": "jabali-mail", "992": "pdns", "1001": "tenant1"}
	byID := func(gid string) (string, bool) { n, ok := names[gid]; return n, ok }

	for gid, group := range map[int]string{989: "jabali", 990: "jabali-sockets", 991: "jabali-mail", 992: "pdns"} {
		keep, warn := restorePrimaryGID("tenant1", gid, byID)
		if keep {
			t.Errorf("gid %d (%s): a restored user must not get an installer-managed group as its primary group", gid, group)
		}
		if !strings.Contains(warn, `"tenant1"`) || !strings.Contains(warn, `"`+group+`"`) {
			t.Errorf("gid %d: warning %q must name the user and the refused group", gid, warn)
		}
	}
	for gid, account := range map[int]string{989: "jabali", 991: "jabali-mail", 992: "pdns"} {
		if keep, warn := restorePrimaryGID(account, gid, byID); !keep || warn != "" {
			t.Errorf("the %s service account keeps its own group as its primary group: keep=%v warn=%q", account, keep, warn)
		}
	}
	if keep, warn := restorePrimaryGID("tenant1", 1001, byID); !keep || warn != "" {
		t.Errorf("an ordinary existing primary group is kept: keep=%v warn=%q", keep, warn)
	}
	if keep, warn := restorePrimaryGID("tenant1", 4242, byID); keep || !strings.Contains(warn, "absent") {
		t.Errorf("a missing primary group falls back to a fresh usergroup: keep=%v warn=%q", keep, warn)
	}
}
