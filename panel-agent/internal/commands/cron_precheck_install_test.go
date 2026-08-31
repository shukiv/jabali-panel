package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

// libexecRefRe finds every /usr/local/libexec/jabali/<name> path a
// generated unit references in an ExecStart* line.
var libexecRefRe = regexp.MustCompile(`/usr/local/libexec/jabali/([A-Za-z0-9._-]+)`)

// TestGeneratedUnitLibexecHelpersAreShipped guards the cron-precheck
// 203/EXEC regression: a unit builder must never reference a libexec
// helper that install.sh doesn't ship. For every /usr/local/libexec/
// jabali/<name> in a rendered cron service unit, assert install/systemd/
// <name> exists in the repo AND install.sh installs it.
func TestGeneratedUnitLibexecHelpersAreShipped(t *testing.T) {
	cmd, err := cronvalidate.ValidateCommand("php /home/u/public_html/wp-cron.php", []string{"/home/u/public_html"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	svc := buildCronServiceContent("job1", "test", []*cronvalidate.Command{cmd}, "u", []string{"/home/u/public_html"})

	refs := libexecRefRe.FindAllStringSubmatch(svc, -1)
	if len(refs) == 0 {
		t.Skip("no libexec helper referenced in the generated unit")
	}

	// repo root = panel-agent/internal/commands -> ../../..
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	installSh, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}

	for _, m := range refs {
		name := m[1]
		src := filepath.Join(root, "install", "systemd", name)
		if _, err := os.Stat(src); err != nil {
			t.Errorf("unit references /usr/local/libexec/jabali/%s but install/systemd/%s does not exist (203/EXEC at runtime)", name, name)
		}
		if !strings.Contains(string(installSh), "install/systemd/"+name) {
			t.Errorf("install.sh never installs install/systemd/%s — generated unit would hit 203/EXEC on the host", name)
		}
	}
}
