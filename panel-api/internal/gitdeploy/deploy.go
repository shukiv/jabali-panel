package gitdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Deployments (ADR-0178). The agent runs each deployment or rollback as
// jabali-gitdeploy-<run id>.service, as the domain's owner in their SSH
// sandbox, and keeps the unit's result until the panel has stored it.
// Refresh stores a finished run, then asks the agent to release the unit,
// which also removes the run's credential file. The API calls it when a run
// is read and a reconciler pass calls it for runs nobody reads.
//
// A deployment that succeeded has switched domains/<domain>/current to its
// release; Refresh then points the domain's document root at current (once:
// later runs find it there).

// Wire types of the agent's git.deploy_* and git.releases commands. The
// fixtures in internal/agent/testdata/git_deploy_*.json and git_releases_*
// are decoded by both sides.
type (
	DeployStartRequest struct {
		RunID string `json:"run_id"`
		// Kind is deploy or rollback.
		Kind   string `json:"kind"`
		OSUser string `json:"os_user"`
		Domain string `json:"domain"`
		// Release is the release a rollback switches to.
		Release  string `json:"release,omitempty"`
		RepoURL  string `json:"repo_url,omitempty"`
		Branch   string `json:"branch,omitempty"`
		Username string `json:"username,omitempty"`
		// Token is never logged or stored in the clear.
		Token       string   `json:"token,omitempty"`
		WebDir      string   `json:"web_dir,omitempty"`
		SharedPaths []string `json:"shared_paths,omitempty"`
		Commands    string   `json:"commands,omitempty"`
		// Composer: the hosting package allows Composer runs, so a project
		// with a composer.lock and no vendor/ gets composer install.
		Composer bool `json:"composer,omitempty"`
	}
	DeployStartResponse struct {
		Unit       string `json:"unit"`
		PHPVersion string `json:"php_version,omitempty"`
	}
	RunIDRequest struct {
		RunID string `json:"run_id"`
	}
	DeployStatusResponse struct {
		State             string `json:"state"`
		Result            string `json:"result,omitempty"`
		ExitCode          *int   `json:"exit_code,omitempty"`
		Log               string `json:"log"`
		LogTruncated      bool   `json:"log_truncated,omitempty"`
		PHPDefenseBlocked bool   `json:"php_defense_blocked,omitempty"`
		// Commit is the commit the deployment fetched, once it has.
		Commit string `json:"commit,omitempty"`
	}
	DeployStopRequest struct {
		RunID  string `json:"run_id"`
		OSUser string `json:"os_user"`
	}
	DeployStopResponse struct {
		Unit string `json:"unit"`
	}
	ReleasesRequest struct {
		OSUser string `json:"os_user"`
		Domain string `json:"domain"`
	}
	ReleasesResponse struct {
		// Releases is the domain's releases, newest first.
		Releases []string `json:"releases"`
		// Live is the release domains/<domain>/current points at; "" when
		// none.
		Live string `json:"live"`
	}
)

// liveMarker is the line a run writes once its release is live
// (panel-agent gitDeployLiveMarker).
const liveMarker = "jabali-git-deploy: live "

// ValidateLayout checks a web directory and shared paths, as the agent does.
func ValidateLayout(webDir string, shared []string) error {
	return cronvalidate.ValidateDeployLayout(webDir, shared)
}

// ValidateCommands checks a deployment's commands, as the agent does, and
// returns them normalized.
func ValidateCommands(raw string) (string, error) {
	if _, err := cronvalidate.ValidateDeployCommands(raw); err != nil {
		return "", err
	}
	return cronvalidate.NormalizeDeployCommands(raw), nil
}

// DocRoot is the document root a domain gets from its deployments.
func DocRoot(osUser, domain, webDir string) string {
	root := "/home/" + osUser + "/domains/" + domain + "/current"
	if webDir != "" {
		root += "/" + webDir
	}
	return root
}

// Scheduler re-applies a domain's vhost after its document root changed:
// the reconciler.
type Scheduler interface {
	Schedule(domainID string)
}

// deployCallTimeout covers git.deploy_start's own checks: resolving the
// repository's host and reading the domain's PHP.
const deployCallTimeout = 30 * time.Second

func call(ctx context.Context, ag agent.AgentInterface, command string, req, out any) error {
	ctx, cancel := context.WithTimeout(ctx, deployCallTimeout)
	defer cancel()
	raw, err := ag.Call(ctx, command, req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: decode the answer: %w", command, err)
	}
	return nil
}

// Start stores run as running and asks the agent to start it. When the
// domain already has a run in progress it returns
// repository.ErrGitDeploymentActive and stores nothing. A run the agent does
// not start is stored as failed, with the agent's message, and the agent's
// error is returned.
func Start(ctx context.Context, ag agent.AgentInterface, repo repository.DomainGitDeploymentRepository, run *models.DomainGitDeployment, req DeployStartRequest) error {
	run.State = models.GitDeployRunning
	if err := repo.CreateUnlessRunning(ctx, run); err != nil {
		return err
	}
	req.RunID, req.Kind, req.OSUser = run.ID, run.Kind, run.OSUser
	if run.Kind == models.GitDeployKindRollback {
		req.Release = run.ReleaseID
	}
	var resp DeployStartResponse
	if err := call(ctx, ag, "git.deploy_start", req, &resp); err != nil {
		finish(run, models.GitDeployFailed)
		if ae, ok := Refusal(err); ok {
			// The agent's own words for what the run lacks, written for the
			// user ("the hosting package does not include Composer runs").
			run.Error = clip(ae.Message, 1024)
		} else {
			// The unit may have started before the call failed: end it, and
			// remove its credential file. What went wrong is for the server
			// log, not the user.
			_ = Stop(context.WithoutCancel(ctx), ag, run)
			run.Error = "the server could not start the deployment"
		}
		if serr := repo.Save(context.WithoutCancel(ctx), run); serr != nil {
			return errors.Join(err, fmt.Errorf("store the failed run: %w", serr))
		}
		return err
	}
	run.PHPVersion = resp.PHPVersion
	return repo.Save(ctx, run)
}

// Refresh asks the agent about a running run. A run that has finished is
// stored with its result, the domain's document root pointed at current
// when the run made its release live, and the run's unit released. A run
// still going gets the log so far, which is not stored. A run not running is
// left alone.
func Refresh(ctx context.Context, ag agent.AgentInterface, repo repository.DomainGitDeploymentRepository, sched Scheduler, run *models.DomainGitDeployment) error {
	if run.State != models.GitDeployRunning {
		return nil
	}
	st, err := Status(ctx, ag, run.ID)
	if err != nil {
		return err
	}
	if st.Commit != "" {
		run.CommitSHA = st.Commit
	}
	switch st.State {
	case "running":
		run.Log, run.LogTruncated = st.Log, st.LogTruncated
		return nil
	case "succeeded":
		finish(run, models.GitDeploySucceeded)
	case "failed":
		finish(run, models.GitDeployFailed)
	case "timed_out":
		finish(run, models.GitDeployTimedOut)
	case "gone":
		// The unit went away before the panel stored its result: the server
		// restarted, or someone removed it. Its exit status is lost.
		finish(run, models.GitDeployFailed)
		run.Error = "the deployment ended without the panel seeing its result (was the server restarted?)"
	default:
		return fmt.Errorf("git.deploy_status: unknown state %q", st.State)
	}
	run.Result, run.ExitCode = st.Result, st.ExitCode
	run.PHPDefenseBlocked = st.PHPDefenseBlocked
	run.Log, run.LogTruncated = st.Log, st.LogTruncated
	return complete(ctx, ag, repo, sched, run)
}

// complete stores a finished run and releases its unit. A run whose log says
// its release went live is a success whatever else the unit reported: the
// site serves that release now, and the document root must follow it.
func complete(ctx context.Context, ag agent.AgentInterface, repo repository.DomainGitDeploymentRepository, sched Scheduler, run *models.DomainGitDeployment) error {
	if wentLive(run.Log, run.ReleaseID) {
		run.State = models.GitDeploySucceeded
	}
	if run.State == models.GitDeploySucceeded {
		// Before the run is stored: if this fails, the run stays running and
		// the next refresh tries again.
		changed, err := repo.SetDocRoot(ctx, run.DomainID, run.UserID, run.DocRoot)
		if err != nil {
			return fmt.Errorf("point the domain at the release: %w", err)
		}
		if changed && sched != nil {
			sched.Schedule(run.DomainID)
		}
	}
	if err := repo.Save(ctx, run); err != nil {
		return fmt.Errorf("store the run's result: %w", err)
	}
	// Also after a lost unit ("gone"): the run could not remove its
	// credential file itself then.
	if err := Stop(ctx, ag, run); err != nil {
		// The result is stored; the unit stays loaded, holding nothing.
		slog.WarnContext(ctx, "git deploy: release the run's unit", "run", run.ID, "error", err)
	}
	return nil
}

// wentLive reports whether a run's log says its release went live.
func wentLive(log, release string) bool {
	for _, line := range strings.Split(log, "\n") {
		if line == liveMarker+release {
			return true
		}
	}
	return false
}

// ErrNotRunning: the run has already finished.
var ErrNotRunning = errors.New("gitdeploy: the run has already finished")

// Cancel ends a running run and stores it as cancelled, with its log. A run
// that finished meanwhile is stored with its own result instead, and
// ErrNotRunning returned. A run stopped after its release went live is
// stored as succeeded: the site serves that release.
func Cancel(ctx context.Context, ag agent.AgentInterface, repo repository.DomainGitDeploymentRepository, sched Scheduler, run *models.DomainGitDeployment) error {
	if err := Refresh(ctx, ag, repo, sched, run); err != nil {
		return err
	}
	if run.State != models.GitDeployRunning {
		return ErrNotRunning
	}
	if err := Stop(ctx, ag, run); err != nil {
		return err
	}
	finish(run, models.GitDeployCancelled)
	if st, err := Status(ctx, ag, run.ID); err == nil {
		run.Log, run.LogTruncated = st.Log, st.LogTruncated
		if st.Commit != "" {
			run.CommitSHA = st.Commit
		}
	}
	return complete(ctx, ag, repo, sched, run)
}

// Status asks the agent for a run's state and log.
func Status(ctx context.Context, ag agent.AgentInterface, runID string) (*DeployStatusResponse, error) {
	var st DeployStatusResponse
	if err := call(ctx, ag, "git.deploy_status", RunIDRequest{RunID: runID}, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Stop asks the agent to stop a run's unit, unload it and remove the run's
// credential file.
func Stop(ctx context.Context, ag agent.AgentInterface, run *models.DomainGitDeployment) error {
	var resp DeployStopResponse
	return call(ctx, ag, "git.deploy_stop", DeployStopRequest{RunID: run.ID, OSUser: run.OSUser}, &resp)
}

// Releases asks the agent for a domain's releases and the live one.
func Releases(ctx context.Context, ag agent.AgentInterface, osUser, domain string) (*ReleasesResponse, error) {
	var resp ReleasesResponse
	if err := call(ctx, ag, "git.releases", ReleasesRequest{OSUser: osUser, Domain: domain}, &resp); err != nil {
		return nil, err
	}
	if resp.Releases == nil {
		resp.Releases = []string{}
	}
	return &resp, nil
}

// Refusal reports whether err is the agent refusing a run for a reason the
// user can act on: what they asked for (invalid_argument), what the domain
// lacks (failed_precondition), or a run already there (already_exists).
func Refusal(err error) (*agent.AgentError, bool) {
	var ae *agent.AgentError
	if !errors.As(err, &ae) {
		return nil, false
	}
	switch ae.Code {
	case agent.CodeInvalidArgument, agent.CodeFailedPrecondition, agent.CodeAlreadyExists:
		return ae, true
	}
	return nil, false
}

func finish(run *models.DomainGitDeployment, state string) {
	now := time.Now().UTC()
	run.State, run.FinishedAt = state, &now
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
