package backupscheduler

import (
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a scheduled backup carries each domain's web domain aliases, as
// the admin and tenant backups do: the scheduler hands its alias store to the
// shared builder, and both processes that run the scheduler wire it.
func TestScheduleMetaDeps_CarriesWebDomainAliases(t *testing.T) {
	al := struct {
		repository.WebDomainAliasRepository
	}{}
	if got := scheduleMetaDeps(Deps{WebDomainAliases: al}, nil).WebDomainAliases; got != al {
		t.Fatalf("builder alias store %v, want the scheduler's", got)
	}
	for _, f := range []string{"../../cmd/server/serve.go", "../../cmd/server/backup_scheduler_tick_cmd.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// The literal runs from backupscheduler.Deps{ to the "})" indented as
		// the line that opens it.
		s := string(src)
		i := strings.Index(s, "backupscheduler.Deps{")
		if i < 0 {
			t.Fatalf("%s: no backupscheduler.Deps literal", f)
		}
		line := s[strings.LastIndex(s[:i], "\n")+1 : i]
		indent := line[:len(line)-len(strings.TrimLeft(line, "\t"))]
		j := strings.Index(s[i:], "\n"+indent+"})")
		if j < 0 || !strings.Contains(s[i:i+j], "WebDomainAliases:") {
			t.Errorf("%s: the scheduler's Deps must wire WebDomainAliases", f)
		}
	}
}
