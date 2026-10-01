package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/systemjobs"
)

// showFixture is `systemctl show --timestamp=unix -p ...` output as a real box
// printed it (Ubuntu 24.04, systemd 255), trimmed to a few units. Every other
// catalog unit is absent here and so reads as not installed.
const showFixture = `NextElapseUSecRealtime=
Id=jabali-aide-check.timer
LoadState=loaded
ActiveState=active
UnitFileState=enabled
TimersCalendar={ OnCalendar=*-*-* 04:30:00 UTC ; next_elapse=Fri 2026-10-02 04:30:00 UTC }

Result=success
ExecMainStartTimestamp=@1790829606
ExecMainExitTimestamp=@1790829747
Id=jabali-aide-check.service
LoadState=loaded
ActiveState=inactive

Id=jabali-cache-doctor.timer
LoadState=loaded
ActiveState=inactive
UnitFileState=disabled
TimersMonotonic={ OnUnitActiveUSec=1h ; next_elapse=0 }
TimersMonotonic={ OnBootUSec=15min ; next_elapse=0 }

Result=success
ExecMainStartTimestamp=
ExecMainExitTimestamp=
Id=jabali-cache-doctor.service
LoadState=loaded
ActiveState=inactive

Id=jabali-retention-sweep.timer
LoadState=loaded
ActiveState=active
UnitFileState=enabled
TimersCalendar={ OnCalendar=*-*-* 03:40:00 ; next_elapse=Fri 2026-10-02 03:40:00 UTC }

Result=exit-code
ExecMainStartTimestamp=@1790826327
ExecMainExitTimestamp=@1790826328
Id=jabali-retention-sweep.service
LoadState=loaded
ActiveState=failed

Id=jabali-hostname-heartbeat.timer
LoadState=not-found
ActiveState=inactive
UnitFileState=

Id=jabali-hostname-heartbeat.service
LoadState=not-found
ActiveState=inactive`

const listTimersFixture = `[{"next":1790915527065624,"left":1790915527065624,"last":1790829606573451,"passed":1,"unit":"jabali-aide-check.timer","activates":"jabali-aide-check.service"},{"next":null,"left":null,"last":0,"passed":0,"unit":"jabali-retention-sweep.timer","activates":"jabali-retention-sweep.service"}]`

func stubJobSystemctl(t *testing.T, show string) *[][]string {
	t.Helper()
	orig := systemctlRunner
	t.Cleanup(func() { systemctlRunner = orig })
	calls := &[][]string{}
	systemctlRunner = func(_ context.Context, args ...string) (string, error) {
		*calls = append(*calls, args)
		if args[0] == "show" {
			return show, nil
		}
		return "", nil
	}
	return calls
}

func stubListTimers(t *testing.T, out string, err error) {
	t.Helper()
	orig := listTimersJSON
	t.Cleanup(func() { listTimersJSON = orig })
	listTimersJSON = func(context.Context, []string) ([]byte, error) { return []byte(out), err }
}

func listJobs(t *testing.T) map[string]systemjobs.State {
	t.Helper()
	res, err := systemJobsListHandler(context.Background(), nil)
	require.NoError(t, err)
	byID := map[string]systemjobs.State{}
	for _, s := range res.(systemjobs.ListResponse).Jobs {
		byID[s.ID] = s
	}
	return byID
}

func TestSystemJobsList_ReadsStateAndDropsUninstalled(t *testing.T) {
	stubJobSystemctl(t, showFixture)
	stubListTimers(t, listTimersFixture, nil)

	jobs := listJobs(t)
	require.Len(t, jobs, 3, "only the three installed timers are listed")
	assert.NotContains(t, jobs, "hostname-heartbeat")

	aide := jobs["aide-check"]
	assert.Equal(t, systemjobs.StatusScheduled, aide.Status())
	assert.Equal(t, systemjobs.LastSuccess, aide.LastResult())
	assert.Equal(t, "2026-10-01T04:40:06Z", aide.LastStartedAt)
	assert.Equal(t, "2026-10-01T04:42:27Z", aide.LastFinishedAt)
	assert.Equal(t, "2026-10-02T04:32:07Z", aide.NextRunAt)
	assert.Equal(t, []string{"*-*-* 04:30:00 UTC"}, aide.Calendar)

	cache := jobs["cache-doctor"]
	assert.Equal(t, systemjobs.StatusDisabled, cache.Status())
	assert.Equal(t, systemjobs.LastNever, cache.LastResult())
	assert.Equal(t, "1h", cache.Every, "OnBootUSec is a start delay, not the repeat interval")
	assert.Empty(t, cache.NextRunAt)

	sweep := jobs["retention-sweep"]
	assert.Equal(t, systemjobs.LastFailed, sweep.LastResult())
	assert.Empty(t, sweep.NextRunAt, "a null next run stays empty")
}

// Without list-timers JSON (an older systemd, or a failure) the list still
// works; next runs are just unknown rather than guessed.
func TestSystemJobsList_ListTimersFailureLeavesNextEmpty(t *testing.T) {
	stubJobSystemctl(t, showFixture)
	for _, tc := range []struct {
		out string
		err error
	}{{"", errors.New("unknown option --output")}, {"not json", nil}} {
		stubListTimers(t, tc.out, tc.err)
		jobs := listJobs(t)
		require.Len(t, jobs, 3)
		assert.Empty(t, jobs["aide-check"].NextRunAt)
		assert.Equal(t, systemjobs.LastSuccess, jobs["aide-check"].LastResult())
	}
}

// The show call asks for every catalog unit and nothing else.
func TestSystemJobsList_QueriesOnlyCatalogUnits(t *testing.T) {
	calls := stubJobSystemctl(t, showFixture)
	stubListTimers(t, "[]", nil)
	listJobs(t)
	require.Len(t, *calls, 1)
	units := (*calls)[0][4:]
	var want []string
	for _, j := range systemjobs.All() {
		want = append(want, j.Timer, j.Service)
	}
	assert.Equal(t, want, units)
}

func runJob(id string) (any, error) {
	raw, _ := json.Marshal(map[string]string{"id": id})
	return systemJobRunHandler(context.Background(), raw)
}

func agentCode(t *testing.T, err error) string {
	t.Helper()
	var ae *agentwire.AgentError
	require.ErrorAs(t, err, &ae)
	return ae.Code
}

func startCall(calls [][]string) []string {
	for _, c := range calls {
		if c[0] == "start" {
			return c
		}
	}
	return nil
}

func TestSystemJobRun_StartsServiceWithoutBlocking(t *testing.T) {
	calls := stubJobSystemctl(t, "Id=jabali-retention-sweep.service\nLoadState=loaded\nActiveState=inactive")
	res, err := runJob("retention-sweep")
	require.NoError(t, err)
	assert.Equal(t, systemjobs.RunResponse{Started: true}, res)
	assert.Equal(t, []string{"start", "--no-block", "jabali-retention-sweep.service"}, startCall(*calls))
}

// The agent enforces the catalog's "no manual run" itself: a compromised or
// buggy panel-api cannot start a self-update or an OS upgrade through it.
func TestSystemJobRun_RefusesJobsWithoutRunNow(t *testing.T) {
	for _, id := range []string{"panel-update", "os-updates", "sso-cleanup"} {
		calls := stubJobSystemctl(t, "Id=x\nLoadState=loaded\nActiveState=inactive")
		_, err := runJob(id)
		assert.Equal(t, agentwire.CodePermissionDenied, agentCode(t, err), id)
		assert.Nil(t, startCall(*calls), "%s must not be started", id)
	}
}

func TestSystemJobRun_UnknownIDAndUnitNames(t *testing.T) {
	for _, id := range []string{"nope", "jabali-panel", "jabali-retention-sweep.service", ""} {
		calls := stubJobSystemctl(t, "")
		_, err := runJob(id)
		assert.Equal(t, agentwire.CodeNotFound, agentCode(t, err), id)
		assert.Empty(t, *calls, "%q must not reach systemctl", id)
	}
}

func TestSystemJobRun_NotInstalled(t *testing.T) {
	calls := stubJobSystemctl(t, "Id=jabali-hostname-heartbeat.service\nLoadState=not-found\nActiveState=inactive")
	_, err := runJob("hostname-heartbeat")
	assert.Equal(t, agentwire.CodeNotFound, agentCode(t, err))
	assert.Nil(t, startCall(*calls))
}

func TestSystemJobRun_AlreadyRunningStartsNothing(t *testing.T) {
	calls := stubJobSystemctl(t, "Id=jabali-maldet-scan-daily.service\nLoadState=loaded\nActiveState=activating")
	res, err := runJob("malware-scan")
	require.NoError(t, err)
	assert.Equal(t, systemjobs.RunResponse{AlreadyRunning: true}, res)
	assert.Nil(t, startCall(*calls))
}

func TestSystemJobLog_ClampsLinesAndUsesCatalogUnit(t *testing.T) {
	orig := journalTail
	t.Cleanup(func() { journalTail = orig })
	var gotUnit string
	var gotLines int
	journalTail = func(_ context.Context, unit string, lines int) ([]byte, error) {
		gotUnit, gotLines = unit, lines
		return []byte("line1\nline2\n"), nil
	}
	raw, _ := json.Marshal(systemjobs.LogParams{ID: "aide-check", Lines: 100000})
	res, err := systemJobLogHandler(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, "jabali-aide-check.service", gotUnit)
	assert.Equal(t, systemjobs.MaxLogLines, gotLines)
	assert.Equal(t, systemjobs.LogResponse{Log: "line1\nline2\n", Lines: systemjobs.MaxLogLines}, res)

	raw, _ = json.Marshal(systemjobs.LogParams{ID: "../etc/passwd"})
	_, err = systemJobLogHandler(context.Background(), raw)
	assert.Equal(t, agentwire.CodeNotFound, agentCode(t, err))
}

func TestSystemJobLog_CapsBytesKeepingTheTail(t *testing.T) {
	orig := journalTail
	t.Cleanup(func() { journalTail = orig })
	big := strings.Repeat("x", maxJobLogBytes) + "\nlast line\n"
	journalTail = func(context.Context, string, int) ([]byte, error) { return []byte(big), nil }
	raw, _ := json.Marshal(systemjobs.LogParams{ID: "aide-check"})
	res, err := systemJobLogHandler(context.Background(), raw)
	require.NoError(t, err)
	log := res.(systemjobs.LogResponse).Log
	assert.LessOrEqual(t, len(log), maxJobLogBytes)
	assert.True(t, strings.HasSuffix(log, "last line\n"))
}
