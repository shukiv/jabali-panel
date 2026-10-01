// Package systemjobs is the fixed catalog of scheduled jobs Jabali installs on
// a server, shown to admins as "System jobs" under Cron Jobs (GH #1686).
//
// Both binaries compile this list in. The agent resolves a job id to its
// systemd units only through it, so a caller names a job and never a unit:
// that is the security boundary, the same shape as the service allow-list in
// panel-agent/internal/commands/service_list.go. panel-api uses the same list
// for the admin view and refuses the same requests before they reach the
// agent. The RPC payloads live here too, so the two sides cannot drift.
package systemjobs

import (
	"fmt"
	"regexp"
	"strings"
)

// Category groups jobs in the admin list.
type Category string

const (
	CategorySecurity    Category = "security"
	CategoryMaintenance Category = "maintenance"
	CategoryMail        Category = "mail"
	CategoryBackups     Category = "backups"
	CategoryStatistics  Category = "statistics"
	CategoryUpdates     Category = "updates"
)

// ManagedByUpdates marks a job whose schedule and on/off switch belong to the
// Updates page. The list links there instead of offering a second editor.
const ManagedByUpdates = "updates"

// Job is one catalog entry. Timer and Service are full systemd unit names.
type Job struct {
	ID          string
	Timer       string
	Service     string
	Label       string
	Description string
	Category    Category
	// RunNow allows a manual run from the panel. Off where a manual run is
	// not meaningful (a job that already fires every 30 seconds) or belongs
	// to another page that runs it with its own progress view (updates).
	RunNow    bool
	ManagedBy string
}

// catalog lists every job Jabali installs. A job whose timer is not installed
// on a box (for example the free-hostname heartbeat) is simply not shown.
// Nothing here can be disabled from the list: the security jobs must stay on,
// and the two update timers are switched on the Updates page.
var catalog = []Job{
	{ID: "aide-check", Timer: "jabali-aide-check.timer", Service: "jabali-aide-check.service",
		Label: "File integrity check (AIDE)", Category: CategorySecurity, RunNow: true,
		Description: "Compares system files against the AIDE baseline and reports anything that changed."},
	{ID: "malware-signatures", Timer: "jabali-maldet-update-signatures.timer", Service: "jabali-maldet-update-signatures.service",
		Label: "Malware signature update", Category: CategorySecurity, RunNow: true,
		Description: "Downloads the latest Linux Malware Detect signatures."},
	{ID: "yara-rules", Timer: "jabali-signature-base-update.timer", Service: "jabali-signature-base-update.service",
		Label: "YARA rule update", Category: CategorySecurity, RunNow: true,
		Description: "Refreshes the pinned signature-base YARA rule pack that malware scans use."},
	{ID: "malware-scan", Timer: "jabali-maldet-scan-daily.timer", Service: "jabali-maldet-scan-daily.service",
		Label: "Daily malware scan", Category: CategorySecurity, RunNow: true,
		Description: "Scans every user's home directory for malware. A full scan can take a while on a large server."},
	{ID: "quarantine-purge", Timer: "jabali-malware-quarantine-purge.timer", Service: "jabali-malware-quarantine-purge.service",
		Label: "Malware quarantine purge", Category: CategorySecurity, RunNow: true,
		Description: "Deletes quarantined files that are past the quarantine retention period."},
	{ID: "crowdsec-hub", Timer: "jabali-crowdsec-hub-refresh.timer", Service: "jabali-crowdsec-hub-refresh.service",
		Label: "CrowdSec hub refresh", Category: CategorySecurity, RunNow: true,
		Description: "Updates CrowdSec parsers, scenarios and collections."},
	{ID: "apparmor-enforce", Timer: "jabali-apparmor-flip-mature.timer", Service: "jabali-apparmor-flip-mature.service",
		Label: "AppArmor profile enforcement", Category: CategorySecurity, RunNow: true,
		Description: "Switches AppArmor profiles that ran 7 days without a violation from complain mode to enforce mode."},
	{ID: "egress-enforce", Timer: "jabali-per-user-egress-flip.timer", Service: "jabali-per-user-egress-flip.service",
		Label: "Outbound traffic enforcement", Category: CategorySecurity, RunNow: true,
		Description: "Switches per-user outbound traffic policies that finished their 7-day learning period to enforced."},
	{ID: "migration-secrets", Timer: "jabali-migration-secrets-reap.timer", Service: "jabali-migration-secrets-reap.service",
		Label: "Migration secrets cleanup", Category: CategorySecurity, RunNow: true,
		Description: "Wipes the stored credentials of account migrations that have finished."},
	{ID: "sso-cleanup", Timer: "jabali-sso-reaper.timer", Service: "jabali-sso-reaper.service",
		Label: "Single sign-on cleanup", Category: CategorySecurity, RunNow: false,
		Description: "Removes expired one-time login files. It runs every 30 seconds, so there is nothing to run by hand."},
	{ID: "backup-retention", Timer: "jabali-backup-retention.timer", Service: "jabali-backup-retention.service",
		Label: "Backup retention", Category: CategoryBackups, RunNow: true,
		Description: "Removes backup snapshots that fall outside the retention policy and frees their space."},
	{ID: "disk-maintenance", Timer: "jabali-disk-maintenance.timer", Service: "jabali-disk-maintenance.service",
		Label: "Disk maintenance", Category: CategoryMaintenance, RunNow: true,
		Description: "Prunes old mail-server logs and unused container images, and refreshes the disk usage figures of every account."},
	{ID: "retention-sweep", Timer: "jabali-retention-sweep.timer", Service: "jabali-retention-sweep.service",
		Label: "Log retention sweep", Category: CategoryMaintenance, RunNow: true,
		Description: "Deletes expired rows from the panel's log and report tables."},
	{ID: "cache-doctor", Timer: "jabali-cache-doctor.timer", Service: "jabali-cache-doctor.service",
		Label: "WordPress cache health check", Category: CategoryMaintenance, RunNow: true,
		Description: "Checks the page-cache setup of WordPress sites and repairs drift."},
	{ID: "hostname-heartbeat", Timer: "jabali-hostname-heartbeat.timer", Service: "jabali-hostname-heartbeat.service",
		Label: "Free hostname check-in", Category: CategoryMaintenance, RunNow: true,
		Description: "Checks in so this server's free hostname stays registered, and logs a warning when the server's public IP no longer matches it."},
	{ID: "spam-rules", Timer: "jabali-spam-rules-update.timer", Service: "jabali-spam-rules-update.service",
		Label: "Spam filter rule update", Category: CategoryMail, RunNow: true,
		Description: "Pulls the latest spam-filter rules for the mail server."},
	{ID: "web-statistics", Timer: "jabali-goaccess.timer", Service: "jabali-goaccess.service",
		Label: "Web statistics", Category: CategoryStatistics, RunNow: true,
		Description: "Builds the GoAccess traffic reports for each domain."},
	{ID: "os-updates", Timer: "apt-daily-upgrade.timer", Service: "apt-daily-upgrade.service",
		Label: "Operating system updates", Category: CategoryUpdates, RunNow: false, ManagedBy: ManagedByUpdates,
		Description: "Installs operating-system security updates. The schedule and the on/off switch are on the Updates page."},
	{ID: "panel-update", Timer: "jabali-autoupdate.timer", Service: "jabali-autoupdate.service",
		Label: "Panel auto-update", Category: CategoryUpdates, RunNow: false, ManagedBy: ManagedByUpdates,
		Description: "Updates Jabali on its release channel. Off unless it is turned on on the Updates page."},
}

// All returns a copy of the catalog in display order.
func All() []Job {
	out := make([]Job, len(catalog))
	copy(out, catalog)
	return out
}

// Lookup returns the job with this id.
func Lookup(id string) (Job, bool) {
	for _, j := range catalog {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}

// ---- agent RPC payloads (system.jobs_list / system.job_run / system.job_log) ----

// State is one installed job's live systemd state, as the agent reads it.
// Times are RFC 3339 in UTC; empty when systemd has none.
type State struct {
	ID string `json:"id"`
	// TimerActive is the timer's ActiveState: "active" means it will fire.
	TimerActive string `json:"timer_active"`
	// TimerEnabled is the timer's UnitFileState (enabled, disabled, ...).
	TimerEnabled string `json:"timer_enabled"`
	// ServiceActive is the service's ActiveState: "activating" while it runs.
	ServiceActive string `json:"service_active"`
	// Result is systemd's verdict on the last run (success, exit-code, ...).
	// It is the right success signal: some jobs exit non-zero by design and
	// their unit says which exit codes count as success.
	Result         string   `json:"result"`
	LastStartedAt  string   `json:"last_started_at,omitempty"`
	LastFinishedAt string   `json:"last_finished_at,omitempty"`
	NextRunAt      string   `json:"next_run_at,omitempty"`
	Calendar       []string `json:"calendar,omitempty"` // OnCalendar expressions
	Every          string   `json:"every,omitempty"`    // OnUnitActiveSec, e.g. "1h"
}

// ListResponse is the system.jobs_list result: installed jobs only.
type ListResponse struct {
	Jobs []State `json:"jobs"`
}

// RunParams names a job by catalog id.
type RunParams struct {
	ID string `json:"id"`
}

// RunResponse reports whether a run was started. AlreadyRunning means the job
// was running and nothing new was started.
type RunResponse struct {
	Started        bool `json:"started"`
	AlreadyRunning bool `json:"already_running"`
}

// LogParams names a job by catalog id and asks for its last Lines log lines.
type LogParams struct {
	ID    string `json:"id"`
	Lines int    `json:"lines"`
}

type LogResponse struct {
	Log   string `json:"log"`
	Lines int    `json:"lines"`
}

// Log line bounds, shared so the API and the agent clamp the same way.
const (
	DefaultLogLines = 200
	MaxLogLines     = 500
)

// ClampLogLines maps a requested line count to the allowed range.
func ClampLogLines(n int) int {
	switch {
	case n <= 0:
		return DefaultLogLines
	case n > MaxLogLines:
		return MaxLogLines
	}
	return n
}

// Status values for the admin list.
const (
	StatusRunning   = "running"
	StatusScheduled = "scheduled"
	StatusDisabled  = "disabled"
)

// Status says whether the job is running now, waiting for its next run, or
// not scheduled at all.
func (s State) Status() string {
	switch s.ServiceActive {
	case "activating", "active", "reloading", "deactivating":
		return StatusRunning
	}
	if s.TimerActive == "active" {
		return StatusScheduled
	}
	return StatusDisabled
}

// Last-run values for the admin list.
const (
	LastNever   = "never"
	LastSuccess = "success"
	LastFailed  = "failed"
	LastRunning = "running"
)

// LastResult summarises the most recent run.
func (s State) LastResult() string {
	if s.Status() == StatusRunning {
		return LastRunning
	}
	if s.LastStartedAt == "" {
		return LastNever
	}
	if s.Result == "success" {
		return LastSuccess
	}
	return LastFailed
}

// ---- schedule text ----

var (
	dailyRe   = regexp.MustCompile(`^\*-\*-\* (\d{2}):(\d{2}):00(?: (\S+))?$`)
	weeklyRe  = regexp.MustCompile(`^(Mon|Tue|Wed|Thu|Fri|Sat|Sun) \*-\*-\* (\d{2}):(\d{2}):00(?: (\S+))?$`)
	minutesRe = regexp.MustCompile(`^\*-\*-\* \*:00/(\d+):00(?: (\S+))?$`)
	spanRe    = regexp.MustCompile(`^(\d+)(s|min|h|d)$`)
)

var weekdays = map[string]string{
	"Mon": "Monday", "Tue": "Tuesday", "Wed": "Wednesday", "Thu": "Thursday",
	"Fri": "Friday", "Sat": "Saturday", "Sun": "Sunday",
}

// DescribeSchedule turns the timer's normalized OnCalendar expressions (as
// `systemctl show` prints them) or its repeat interval into short text, for
// example "Daily at 04:30 UTC" or "Every hour". A form it does not know comes
// back as the raw expression, which is still correct, just less friendly.
// Times without a zone are the server's local time.
func DescribeSchedule(calendar []string, every string) string {
	parts := make([]string, 0, len(calendar)+1)
	for _, expr := range calendar {
		parts = append(parts, describeCalendar(strings.TrimSpace(expr)))
	}
	if every != "" {
		parts = append(parts, "Every "+describeSpan(every))
	}
	return strings.Join(parts, "; ")
}

func describeCalendar(expr string) string {
	if m := dailyRe.FindStringSubmatch(expr); m != nil {
		return withZone(fmt.Sprintf("Daily at %s:%s", m[1], m[2]), m[3])
	}
	if m := weeklyRe.FindStringSubmatch(expr); m != nil {
		return withZone(fmt.Sprintf("Weekly on %s at %s:%s", weekdays[m[1]], m[2], m[3]), m[4])
	}
	if m := minutesRe.FindStringSubmatch(expr); m != nil {
		return withZone(fmt.Sprintf("Every %s minutes", m[1]), m[2])
	}
	return expr
}

func withZone(s, zone string) string {
	if zone == "" {
		return s
	}
	return s + " " + zone
}

func describeSpan(span string) string {
	m := spanRe.FindStringSubmatch(span)
	if m == nil {
		return span
	}
	units := map[string][2]string{
		"s":   {"second", "seconds"},
		"min": {"minute", "minutes"},
		"h":   {"hour", "hours"},
		"d":   {"day", "days"},
	}[m[2]]
	if m[1] == "1" {
		return units[0]
	}
	return m[1] + " " + units[1]
}
