// system_jobs.go — agent side of the admin "System jobs" list (GH #1686):
// system.jobs_list, system.job_run and system.job_log.
//
// Every request names a job by its id in the compiled-in catalog
// (internal/systemjobs) and the agent looks up the unit names itself, so a
// caller can never point these verbs at an arbitrary unit. The catalog also
// says which jobs may be run by hand; the agent enforces that here, not only
// panel-api.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/systemjobs"
)

// Seams for tests; production runs systemctl / journalctl through the
// package exec boundary.
var (
	listTimersJSON = realListTimersJSON
	journalTail    = realJournalTail
	unitJobJournal = realUnitJobJournal
)

func realListTimersJSON(ctx context.Context, timers []string) ([]byte, error) {
	args := append([]string{"list-timers", "--all", "--output=json"}, timers...)
	return execCommandContext(ctx, "systemctl", args...).Output()
}

func realJournalTail(ctx context.Context, unit string, lines int) ([]byte, error) {
	return execCommandContext(ctx, "journalctl", "-u", unit, "-n", strconv.Itoa(lines),
		"--no-pager", "-o", "short-iso").Output()
}

// realUnitJobJournal reads systemd's own recent messages about unit (the
// "Starting", "Finished" and "Failed" lines), one JSON object per line.
func realUnitJobJournal(ctx context.Context, unit string) ([]byte, error) {
	return execCommandContext(ctx, "journalctl", "_PID=1", "UNIT="+unit, "-n", "20",
		"-o", "json", "--no-pager").Output()
}

// jobShowProps is read for every timer and service in one `systemctl show`.
// --timestamp=unix makes the ExecMain* times "@<seconds>", which parses
// without guessing a time zone. It does not apply to the timers' own
// next/last properties, so next runs come from list-timers instead.
const jobShowProps = "Id,LoadState,UnitFileState,ActiveState,Result,ExecMainStartTimestamp,ExecMainExitTimestamp,TimersCalendar,TimersMonotonic"

var (
	onCalendarRe   = regexp.MustCompile(`OnCalendar=(.+?) ;`)
	onUnitActiveRe = regexp.MustCompile(`OnUnitActiveUSec=(\S+) ;`)
)

// maxJobLogBytes caps a log reply; the tail is kept.
const maxJobLogBytes = 256 * 1024

func systemJobsListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	jobs := systemjobs.All()
	units := make([]string, 0, 2*len(jobs))
	timers := make([]string, 0, len(jobs))
	for _, j := range jobs {
		units = append(units, j.Timer, j.Service)
		timers = append(timers, j.Timer)
	}
	out, err := systemctlRunner(ctx, append([]string{"show", "--timestamp=unix", "-p", jobShowProps}, units...)...)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemctl show: %v: %s", err, out)}
	}
	props := parseShowBlocks(out)
	next := nextTimerRuns(ctx, timers)

	resp := systemjobs.ListResponse{Jobs: []systemjobs.State{}}
	for _, j := range jobs {
		t := props[j.Timer]
		// Not installed on this box (or masked): not shown.
		if firstProp(t, "LoadState") != "loaded" {
			continue
		}
		s := props[j.Service]
		st := systemjobs.State{
			ID:             j.ID,
			TimerActive:    firstProp(t, "ActiveState"),
			TimerEnabled:   firstProp(t, "UnitFileState"),
			ServiceActive:  firstProp(s, "ActiveState"),
			Result:         firstProp(s, "Result"),
			LastStartedAt:  unixStamp(firstProp(s, "ExecMainStartTimestamp")),
			LastFinishedAt: unixStamp(firstProp(s, "ExecMainExitTimestamp")),
			NextRunAt:      next[j.Timer],
		}
		if st.LastStartedAt == "" && st.ServiceActive == "inactive" {
			lastRunFromJournal(ctx, j.Service, &st)
		}
		for _, v := range t["TimersCalendar"] {
			if m := onCalendarRe.FindStringSubmatch(v); m != nil {
				st.Calendar = append(st.Calendar, m[1])
			}
		}
		for _, v := range t["TimersMonotonic"] {
			if m := onUnitActiveRe.FindStringSubmatch(v); m != nil {
				st.Every = m[1]
			}
		}
		resp.Jobs = append(resp.Jobs, st)
	}
	return resp, nil
}

// systemd message ids (see `journalctl --catalog`) for a unit's start job.
const (
	msgUnitStarting  = "7d4958e842da4a758f6c1cdc7b36dcc5" // "Starting <unit>..."
	msgUnitStarted   = "39f53479d3a045ac8e11786248231fbf" // "Finished <unit>." (JOB_RESULT=done)
	msgUnitFailed    = "be02cf6855d2428ba40df7e9d022f03d" // "Failed to start <unit>." (JOB_RESULT=failed, ...)
	msgUnitResultBad = "d9b373ed55a64feb8242e02dbe79a49c" // "Failed with result 'exit-code'." (UNIT_RESULT)
)

// lastRunFromJournal fills in a job's last run from the journal when systemd
// no longer holds it. systemd unloads a finished service that nothing
// references; a disabled timer does not reference its service, so after a
// Run now the service's ExecMain* times are gone and the run read as
// "never" (GH #1686). A reboot clears them for every job the same way. The
// journal keeps systemd's own start and finish lines for the unit. Best
// effort: if journalctl fails, the job keeps what systemctl reported.
func lastRunFromJournal(ctx context.Context, service string, st *systemjobs.State) {
	out, err := unitJobJournal(ctx, service)
	if err != nil {
		return
	}
	var started, finished, jobResult, unitResult string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var e struct {
			MessageID  string `json:"MESSAGE_ID"`
			JobType    string `json:"JOB_TYPE"`
			JobResult  string `json:"JOB_RESULT"`
			UnitResult string `json:"UNIT_RESULT"`
			Realtime   string `json:"__REALTIME_TIMESTAMP"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		at := journalStamp(e.Realtime)
		if at == "" {
			continue
		}
		switch {
		case e.MessageID == msgUnitStarting && e.JobType == "start":
			started, finished, jobResult, unitResult = at, "", "", ""
		case e.MessageID == msgUnitResultBad:
			unitResult = e.UnitResult
		case (e.MessageID == msgUnitStarted || e.MessageID == msgUnitFailed) && e.JobType == "start":
			finished, jobResult = at, e.JobResult
		}
	}
	if started == "" {
		return
	}
	st.LastStartedAt, st.LastFinishedAt = started, finished
	switch {
	case jobResult == "done":
		st.Result = "success"
	case unitResult != "":
		st.Result = unitResult
	case jobResult != "":
		st.Result = jobResult
	default:
		// Started with no finish line: it never completed (the box went down
		// mid-run, say), which is not a success.
		st.Result = "unknown"
	}
}

// journalStamp turns the journal's __REALTIME_TIMESTAMP (microseconds since
// the epoch) into RFC 3339 UTC.
func journalStamp(v string) string {
	us, err := strconv.ParseInt(v, 10, 64)
	if err != nil || us <= 0 {
		return ""
	}
	return time.UnixMicro(us).UTC().Format(time.RFC3339)
}

// nextTimerRuns reads each timer's next run from `systemctl list-timers
// --output=json`, whose times are microseconds since the epoch. Best effort:
// if the call fails or the output does not parse, next runs are left empty
// rather than guessed from systemd's local-time text.
func nextTimerRuns(ctx context.Context, timers []string) map[string]string {
	next := map[string]string{}
	out, err := listTimersJSON(ctx, timers)
	if err != nil {
		return next
	}
	var rows []struct {
		Next *int64 `json:"next"`
		Unit string `json:"unit"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return next
	}
	for _, r := range rows {
		if r.Next != nil && *r.Next > 0 {
			next[r.Unit] = time.UnixMicro(*r.Next).UTC().Format(time.RFC3339)
		}
	}
	return next
}

// parseShowBlocks splits `systemctl show` output for several units into
// per-unit property maps keyed by Id. A property can repeat within a block
// (a timer with two OnCalendar lines prints TimersCalendar twice), so values
// are lists.
func parseShowBlocks(out string) map[string]map[string][]string {
	units := map[string]map[string][]string{}
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		props := map[string][]string{}
		for _, line := range strings.Split(block, "\n") {
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				continue
			}
			props[line[:eq]] = append(props[line[:eq]], line[eq+1:])
		}
		if id := firstProp(props, "Id"); id != "" {
			units[id] = props
		}
	}
	return units
}

func firstProp(props map[string][]string, key string) string {
	if v := props[key]; len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

// unixStamp turns systemd's "@<seconds>" into RFC 3339 UTC; anything else
// (empty, "n/a") is no time.
func unixStamp(v string) string {
	if !strings.HasPrefix(v, "@") {
		return ""
	}
	sec, err := strconv.ParseInt(strings.TrimPrefix(v, "@"), 10, 64)
	if err != nil || sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

// resolveSystemJob parses the job id from params and finds it in the catalog.
func resolveSystemJob(raw json.RawMessage) (systemjobs.Job, *agentwire.AgentError) {
	var p struct {
		ID string `json:"id"`
	}
	if len(raw) == 0 {
		return systemjobs.Job{}, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return systemjobs.Job{}, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	job, ok := systemjobs.Lookup(p.ID)
	if !ok {
		return systemjobs.Job{}, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("unknown system job %q", p.ID)}
	}
	return job, nil
}

func systemJobRunHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	job, ae := resolveSystemJob(raw)
	if ae != nil {
		return nil, ae
	}
	if !job.RunNow {
		return nil, &agentwire.AgentError{Code: agentwire.CodePermissionDenied, Message: fmt.Sprintf("%s cannot be run by hand", job.ID)}
	}
	out, _ := systemctlRunner(ctx, "show", "-p", "Id,LoadState,ActiveState", job.Service)
	s := parseShowBlocks(out)[job.Service]
	if firstProp(s, "LoadState") != "loaded" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("%s is not installed on this server", job.ID)}
	}
	if (systemjobs.State{ServiceActive: firstProp(s, "ActiveState")}).Status() == systemjobs.StatusRunning {
		return systemjobs.RunResponse{AlreadyRunning: true}, nil
	}
	// --no-block: the job may run for minutes (a full malware scan); the
	// caller polls the list for its state.
	if out, err := systemctlRunner(ctx, "start", "--no-block", job.Service); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemctl start %s: %s", job.Service, strings.TrimSpace(out))}
	}
	return systemjobs.RunResponse{Started: true}, nil
}

func systemJobLogHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	job, ae := resolveSystemJob(raw)
	if ae != nil {
		return nil, ae
	}
	var p systemjobs.LogParams
	_ = json.Unmarshal(raw, &p) // already parsed once by resolveSystemJob
	lines := systemjobs.ClampLogLines(p.Lines)
	out, err := journalTail(ctx, job.Service, lines)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("journalctl %s: %v", job.Service, err)}
	}
	if len(out) > maxJobLogBytes {
		out = out[len(out)-maxJobLogBytes:]
		if i := strings.IndexByte(string(out), '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return systemjobs.LogResponse{Log: string(out), Lines: lines}, nil
}

func init() {
	Default.Register("system.jobs_list", systemJobsListHandler)
	Default.Register("system.job_run", systemJobRunHandler)
	Default.Register("system.job_log", systemJobLogHandler)
}
