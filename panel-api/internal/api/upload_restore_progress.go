package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// upload_restore_progress.go — GH #1993. Progress by stage for a restore from
// an uploaded account backup, so a restore that runs for minutes shows what it
// is doing instead of a spinner.
//
// restoreUploadedAccount runs in steps (the agent's first pass, the metadata
// rebuild, the agent's mail pass). During an agent pass the panel polls
// backup.restore_progress for what the agent is doing (unpacking, then each
// stage). An agent without that verb leaves the step's detail empty.
//
// Progress lives in memory, keyed per restore, while the restore runs; the
// status endpoints add it to a "restoring" reply.

// restoreProgress is a running upload restore's progress as the API reports it.
type restoreProgress struct {
	Step  int    `json:"step"`
	Steps int    `json:"steps"`
	Label string `json:"label"`
	// Detail is what the step is doing, when known.
	Detail string `json:"detail,omitempty"`
	// Percent is how far the step is (0-100), when known.
	Percent int `json:"percent,omitempty"`
}

const (
	restoreStepFilesLabel = "Restoring files, databases and apps"
	restoreStepRowsLabel  = "Rebuilding the account's domains, mailboxes and settings"
	restoreStepMailLabel  = "Restoring mail"
	// restoreStepOwnLabel is a tenant's restore into their own account.
	restoreStepOwnLabel = "Restoring files, databases and mail"
)

// restoreProgressPoll is how often the panel asks the agent for progress.
var restoreProgressPoll = 2 * time.Second

var uploadRestoreProgress sync.Map // restore key → restoreProgress

func setUploadRestoreProgress(key string, p restoreProgress) { uploadRestoreProgress.Store(key, p) }

func clearUploadRestoreProgress(key string) { uploadRestoreProgress.Delete(key) }

// uploadRestoreProgressFor is the progress of the restore key, or nil.
func uploadRestoreProgressFor(key string) *restoreProgress {
	v, ok := uploadRestoreProgress.Load(key)
	if !ok {
		return nil
	}
	p := v.(restoreProgress)
	return &p
}

// progressReporter returns the report func restoreUploadedAccount calls for
// the restore key, and a func that clears its progress when the restore ends.
func progressReporter(key string) (report func(restoreProgress), done func()) {
	return func(p restoreProgress) { setUploadRestoreProgress(key, p) },
		func() { clearUploadRestoreProgress(key) }
}

// agentRestoreProgress is backup.restore_progress's result.
type agentRestoreProgress struct {
	Phase      string `json:"phase"`
	Stage      string `json:"stage"`
	Item       string `json:"item"`
	Index      int    `json:"index"`
	Count      int    `json:"count"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
}

// describe turns the agent's progress into a step detail and percent.
func (a agentRestoreProgress) describe() (string, int) {
	switch a.Phase {
	case "unpacking":
		if a.BytesTotal > 0 {
			pct := int(a.BytesDone * 100 / a.BytesTotal)
			pct = min(max(pct, 0), 100)
			return "Unpacking the backup — " + strconv.Itoa(pct) + "%", pct
		}
		return "Unpacking the backup", 0
	case "applying":
		what := restoreStageDescription(a.Stage, a.Item)
		if a.Count > 0 {
			what += fmt.Sprintf(" (%d of %d)", a.Index, a.Count)
			return what, min(max((a.Index-1)*100/a.Count, 0), 100)
		}
		return what, 0
	case "done":
		return "Finishing", 100
	}
	return "", 0
}

// restoreStageDescription names a backup stage for the admin. item comes from
// the uploaded file; it is only ever shown as text.
func restoreStageDescription(stage, item string) string {
	if len(item) > 80 {
		item = item[:80] + "…"
	}
	switch stage {
	case "home":
		return "Restoring the home directory"
	case "db":
		if item != "" {
			return "Restoring database " + item
		}
		return "Restoring databases"
	case "mail":
		return "Restoring mail"
	case "dns":
		return "Restoring DNS records"
	case "docker":
		if item != "" {
			return "Restoring Docker app " + item
		}
		return "Restoring Docker apps"
	case "cron":
		return "Restoring cron jobs"
	case "ssh":
		return "Restoring SSH keys"
	case "apps", "php", "meta", "manifest":
		return "Restoring settings"
	}
	return "Restoring " + stage
}

// withAgentRestoreProgress runs call, an agent restore of jobID, reporting
// step with what the agent is doing as its detail (report nil = no
// reporting). The watcher has stopped when it returns, so it never
// overwrites a later step.
func withAgentRestoreProgress(ctx context.Context, ag agent.AgentInterface, jobID string, step restoreProgress, report func(restoreProgress), call func()) {
	if report == nil {
		call()
		return
	}
	report(step)
	wctx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		watchAgentRestore(wctx, ag, jobID, step, report)
	}()
	call()
	stop()
	wg.Wait()
}

// watchAgentRestore reports, until ctx ends, what the agent's restore jobID is
// doing as step's detail. It stops early for an agent without
// backup.restore_progress.
func watchAgentRestore(ctx context.Context, ag agent.AgentInterface, jobID string, step restoreProgress, report func(restoreProgress)) {
	t := time.NewTicker(restoreProgressPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := ag.Call(callCtx, "backup.restore_progress", map[string]string{"job_id": jobID})
		cancel()
		if err != nil {
			var ae *agentwire.AgentError
			if errors.As(err, &ae) && ae.Code == agentwire.CodeUnknownCommand {
				return
			}
			continue // not started yet, or a transient error
		}
		var a agentRestoreProgress
		if json.Unmarshal(raw, &a) != nil {
			continue
		}
		p := step
		p.Detail, p.Percent = a.describe()
		if ctx.Err() == nil {
			report(p)
		}
	}
}
