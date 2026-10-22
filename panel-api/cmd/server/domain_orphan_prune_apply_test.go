package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
)

// GH #2237: the Repair Center deletes orphan sites through
// `jabali domain prune-orphans --apply --json`. That run must delete them,
// and its result must name what is left.

// fakeSitesAgent serves domain.list and domain.delete over sites-enabled
// names. domain.delete removes the domain's site and its derived vhosts, like
// the agent does; failDelete names a domain whose delete errors, and keep one
// whose delete succeeds but leaves its site (a file the delete doesn't own).
type fakeSitesAgent struct {
	mu         sync.Mutex
	sites      map[string]bool
	deletes    []string
	failDelete string
	keep       string
}

func newFakeSitesAgent(sites ...string) *fakeSitesAgent {
	f := &fakeSitesAgent{sites: map[string]bool{}}
	for _, s := range sites {
		f.sites[s] = true
	}
	return f
}

func (f *fakeSitesAgent) handle(req agent.Request) agent.Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := agent.Response{ID: req.ID, Ok: true}
	switch req.Command {
	case "domain.list":
		names := make([]string, 0, len(f.sites))
		for s := range f.sites {
			names = append(names, s)
		}
		sort.Strings(names)
		resp.Data, _ = json.Marshal(map[string]any{"sites": names})
	case "domain.delete":
		var p struct {
			Domain string `json:"domain"`
		}
		_ = json.Unmarshal(req.Params, &p)
		f.deletes = append(f.deletes, p.Domain)
		if p.Domain == f.failDelete {
			resp.Ok, resp.Error = false, &agent.AgentError{Code: "internal", Message: "systemctl reload nginx failed"}
			return resp
		}
		if p.Domain != f.keep {
			for _, suffix := range []string{"", "-mail", "-mta-sts", "-dockerapp"} {
				delete(f.sites, p.Domain+suffix)
			}
		}
		resp.Data, _ = json.Marshal(map[string]any{"domain": p.Domain, "deleted": true})
	default:
		resp.Ok, resp.Error = false, &agent.AgentError{Code: "not_found", Message: "unknown command " + req.Command}
	}
	return resp
}

// client serves f through the real agent client, over a pipe per call.
func (f *fakeSitesAgent) client() *agent.Client {
	return agent.NewClient(agent.Config{
		SocketPath: "/nonexistent/agent.sock",
		Dial: func(context.Context, string) (net.Conn, error) {
			c, s := net.Pipe()
			go func() {
				defer s.Close()
				line, err := bufio.NewReader(s).ReadBytes('\n')
				if err != nil {
					return
				}
				var req agent.Request
				if err := json.Unmarshal(line, &req); err != nil {
					return
				}
				b, _ := json.Marshal(f.handle(req))
				_, _ = s.Write(append(b, '\n'))
			}()
			return c, nil
		},
	})
}

// pruneDB points sharedDB at a mock whose domains table holds names.
func pruneDB(t *testing.T, names ...string) {
	t.Helper()
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: raw}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	rows := sqlmock.NewRows([]string{"id", "name"})
	for i, n := range names {
		rows.AddRow(string(rune('a'+i)), n)
	}
	mock.ExpectQuery("SELECT count").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(len(names)))
	mock.ExpectQuery("FROM `domains`").WillReturnRows(rows)
	if len(names) > 0 {
		mock.ExpectQuery("ssl_certificates").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	}

	savedDB, savedCfg, savedAgent, savedJSON := sharedDB, sharedCfg, sharedAgent, jsonOutput
	t.Cleanup(func() { sharedDB, sharedCfg, sharedAgent, jsonOutput = savedDB, savedCfg, savedAgent, savedJSON })
	sharedDB = gdb
	sharedCfg = &config.Config{}
}

// runPruneJSON runs `domain prune-orphans --json [--apply]` and decodes its
// stdout.
func runPruneJSON(t *testing.T, apply bool) (map[string]any, error) {
	t.Helper()
	jsonOutput = true
	cmd := newDomainPruneOrphansCmd()
	if apply {
		if err := cmd.Flags().Set("apply", "true"); err != nil {
			t.Fatal(err)
		}
	}
	cmd.SetContext(context.Background())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	runErr := cmd.RunE(cmd, nil)
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)

	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("stdout is not one JSON object (%v): %s", err, out)
	}
	return res, runErr
}

func strs(v any) []string {
	out := []string{}
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestPruneOrphans_JSONApplyDeletesTheOrphans(t *testing.T) {
	pruneDB(t, "keep.com")
	ag := newFakeSitesAgent("default", "keep.com", "keep.com-mail", "gone.example", "gone.example-mail", "old.example")
	sharedAgent = ag.client()

	res, err := runPruneJSON(t, true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"gone.example", "old.example"}; !reflect.DeepEqual(ag.deletes, want) {
		t.Fatalf("domain.delete calls = %v, want %v (one per domain)", ag.deletes, want)
	}
	if got, want := strs(res["deleted"]), []string{"gone.example", "gone.example-mail", "old.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted = %v, want %v", got, want)
	}
	if got := res["failed"].([]any); len(got) != 0 {
		t.Fatalf("failed = %v, want none", got)
	}
	if len(ag.sites) != 3 {
		t.Fatalf("sites left = %v, want default, keep.com, keep.com-mail", ag.sites)
	}
}

func TestPruneOrphans_JSONScanDeletesNothing(t *testing.T) {
	pruneDB(t, "keep.com")
	ag := newFakeSitesAgent("keep.com", "gone.example")
	sharedAgent = ag.client()

	res, err := runPruneJSON(t, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(ag.deletes) != 0 {
		t.Fatalf("a scan called domain.delete for %v", ag.deletes)
	}
	if got := strs(res["orphans"]); !reflect.DeepEqual(got, []string{"gone.example"}) {
		t.Fatalf("orphans = %v", got)
	}
	if got := strs(res["deleted"]); len(got) != 0 {
		t.Fatalf("deleted = %v on a scan", got)
	}
}

// A site still there after its delete is reported with the reason, and the
// command exits non-zero, with the JSON still the only thing on stdout.
func TestPruneOrphans_JSONApplyReportsWhatIsLeft(t *testing.T) {
	pruneDB(t)
	ag := newFakeSitesAgent("bad.example", "stuck.example", "ok.example")
	ag.failDelete, ag.keep = "bad.example", "stuck.example"
	sharedAgent = ag.client()

	res, err := runPruneJSON(t, true)
	if err == nil || !strings.Contains(err.Error(), "2 orphan deletion(s) failed") {
		t.Fatalf("err = %v, want 2 failed deletions", err)
	}
	if got := strs(res["deleted"]); !reflect.DeepEqual(got, []string{"ok.example"}) {
		t.Fatalf("deleted = %v, want [ok.example]", got)
	}
	why := map[string]string{}
	for _, f := range res["failed"].([]any) {
		m := f.(map[string]any)
		why[m["site"].(string)] = m["error"].(string)
	}
	if !strings.Contains(why["bad.example"], "systemctl reload nginx failed") {
		t.Errorf("bad.example: %q, want the agent's error", why["bad.example"])
	}
	if !strings.Contains(why["stuck.example"], "still in /etc/nginx/sites-enabled") {
		t.Errorf("stuck.example: %q, want still present", why["stuck.example"])
	}
}
