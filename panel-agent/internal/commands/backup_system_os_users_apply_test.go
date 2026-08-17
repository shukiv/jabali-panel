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
