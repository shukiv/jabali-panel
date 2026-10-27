// Package composerrun drives the Composer runs a user starts from the panel
// (GH #2276, ADR-0176): it checks what a run asks for, starts it on the
// agent, and stores its result once it has finished.
//
// The agent runs each run as jabali-composer-<run id>.service and keeps the
// unit's result until the panel has stored it. Refresh stores a finished
// run, then asks the agent to release the unit. The API calls it when a run
// is read and a reconciler pass calls it for runs nobody reads.
package composerrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Command is one Composer command a run may start, as the agent's
// composerCommands has it: the panel checks a run before it reaches the
// agent, and the agent checks it again.
type Command struct {
	// Packages is what the run's packages may be: "" none, "names" package
	// names, "require" names with an optional :constraint.
	Packages    string
	MinPackages int
	NoDev       bool
}

// Commands are the Composer commands a run may start.
var Commands = map[string]Command{
	"install":       {NoDev: true},
	"update":        {Packages: "names", NoDev: true},
	"require":       {Packages: "require", MinPackages: 1},
	"remove":        {Packages: "names", MinPackages: 1},
	"audit":         {NoDev: true},
	"outdated":      {NoDev: true},
	"show":          {NoDev: true},
	"validate":      {},
	"dump-autoload": {NoDev: true},
	"diagnose":      {},
}

// MaxPackages bounds the packages one run names.
const MaxPackages = 20

var (
	packageRE    = regexp.MustCompile(`^[a-z0-9]([_.-]?[a-z0-9]+)*/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*$`)
	constraintRE = regexp.MustCompile(`^[A-Za-z0-9.*^~<>=!|, @_+-]{1,64}$`)
	appDirRE     = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
)

// Validate checks a run's command, packages, no_dev and app directory.
func Validate(command string, pkgs []string, noDev bool, appDir string) error {
	cmd, ok := Commands[command]
	if !ok {
		return fmt.Errorf("%q is not a Composer command the panel runs", command)
	}
	if noDev && !cmd.NoDev {
		return fmt.Errorf("%s has no --no-dev", command)
	}
	if len(pkgs) > MaxPackages {
		return fmt.Errorf("at most %d packages per run", MaxPackages)
	}
	if len(pkgs) < cmd.MinPackages {
		return fmt.Errorf("%s needs at least one package", command)
	}
	if cmd.Packages == "" && len(pkgs) > 0 {
		return fmt.Errorf("%s takes no packages", command)
	}
	for _, p := range pkgs {
		name, constraint, hasConstraint := strings.Cut(p, ":")
		if !packageRE.MatchString(name) {
			return fmt.Errorf("%q is not a package name (vendor/name)", p)
		}
		if hasConstraint && cmd.Packages != "require" {
			return fmt.Errorf("%q: only require takes a version constraint", p)
		}
		if hasConstraint && !constraintRE.MatchString(constraint) {
			return fmt.Errorf("%q: a version constraint may hold only letters, digits, spaces and . * ^ ~ < > = ! | , @ _ + -", p)
		}
	}
	if appDir != "" {
		if len(appDir) > 255 || !appDirRE.MatchString(appDir) {
			return fmt.Errorf("the app directory must be a path inside the domain's directory, of letters, digits, '.', '_', '-' and '/'")
		}
		for _, seg := range strings.Split(appDir, "/") {
			if seg == "." || seg == ".." {
				return fmt.Errorf("the app directory may not contain . or ..")
			}
		}
	}
	return nil
}

// ValidateProjectDir checks a domain's Composer project folder: relative to
// the user's home, "domains" or a folder under it. The home itself holds
// .ssh, ~/.composer and other files no Composer project should write next
// to.
func ValidateProjectDir(dir string) error {
	if dir != "domains" && !strings.HasPrefix(dir, "domains/") {
		return fmt.Errorf("the project folder must be domains/ or a folder inside it")
	}
	if len(dir) > 255 || !appDirRE.MatchString(dir) {
		return fmt.Errorf("the project folder may hold only letters, digits, '.', '_', '-' and '/'")
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("the project folder may not contain . or ..")
		}
	}
	return nil
}

// DomainWorkDir is the folder a run of domain runs in when the domain has no
// project folder: appDir inside domains/<domain>, relative to the home.
func DomainWorkDir(domain, appDir string) string {
	return path.Join("domains", domain, appDir)
}

// Wire types of the agent's composer.* commands. The fixtures in
// internal/agent/testdata/composer_*.json are decoded by both sides.
type (
	DetectRequest struct {
		OSUser  string `json:"os_user"`
		Domain  string `json:"domain"`
		Docroot string `json:"docroot"`
		// ProjectDir is the domain's project folder, relative to the home.
		// When set, the agent looks only there.
		ProjectDir *string `json:"project_dir,omitempty"`
	}
	DetectDir struct {
		Dir       string `json:"dir"`
		HasLock   bool   `json:"has_lock"`
		HasVendor bool   `json:"has_vendor"`
	}
	// DetectProject is what the domain's project folder holds.
	DetectProject struct {
		Dir string `json:"dir"`
		// Exists: the folder is there and is not a link.
		Exists          bool `json:"exists"`
		HasComposerJSON bool `json:"has_composer_json"`
		HasLock         bool `json:"has_lock"`
		HasVendor       bool `json:"has_vendor"`
	}
	DetectResponse struct {
		Dirs          []DetectDir    `json:"dirs"`
		Project       *DetectProject `json:"project,omitempty"`
		PHPVersion    string         `json:"php_version,omitempty"`
		PHPError      string         `json:"php_error,omitempty"`
		ComposerError string         `json:"composer_error,omitempty"`
	}
	StartRequest struct {
		RunID  string `json:"run_id"`
		OSUser string `json:"os_user"`
		Domain string `json:"domain"`
		AppDir string `json:"app_dir"`
		// WorkDir is the folder to run in, relative to the home; it replaces
		// AppDir. An agent from before it runs in AppDir.
		WorkDir  string   `json:"work_dir,omitempty"`
		Command  string   `json:"command"`
		Packages []string `json:"packages,omitempty"`
		NoDev    bool     `json:"no_dev,omitempty"`
	}
	StartResponse struct {
		Unit       string `json:"unit"`
		PHPVersion string `json:"php_version"`
	}
	RunIDRequest struct {
		RunID string `json:"run_id"`
	}
	StatusResponse struct {
		State             string `json:"state"`
		Result            string `json:"result,omitempty"`
		ExitCode          *int   `json:"exit_code,omitempty"`
		Log               string `json:"log"`
		LogTruncated      bool   `json:"log_truncated,omitempty"`
		PHPDefenseBlocked bool   `json:"php_defense_blocked,omitempty"`
	}
	StopResponse struct {
		Unit string `json:"unit"`
	}
)

const callTimeout = 30 * time.Second

// call runs one agent command and decodes its answer into out.
func call(ctx context.Context, ag agent.AgentInterface, command string, req, out any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
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

// Detect asks the agent where the domain's composer.json files are.
func Detect(ctx context.Context, ag agent.AgentInterface, req DetectRequest) (*DetectResponse, error) {
	var resp DetectResponse
	if err := call(ctx, ag, "composer.detect", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Start stores run as running and asks the agent to start it in
// run.WorkDir. When the domain, or another domain in the same folder,
// already has a run in progress it returns repository.ErrComposerRunActive
// and stores nothing. A run the agent does not start is stored as failed,
// with the agent's message, and the agent's error is returned.
func Start(ctx context.Context, ag agent.AgentInterface, repo repository.ComposerRunRepository, run *models.ComposerRun, osUser, domain string) error {
	if run.WorkDir == "" {
		// The run would take no folder's lock.
		return errors.New("composerrun: a run needs its folder (WorkDir)")
	}
	run.State = models.ComposerRunRunning
	if err := repo.CreateUnlessRunning(ctx, run); err != nil {
		return err
	}
	var resp StartResponse
	err := call(ctx, ag, "composer.run_start", StartRequest{
		RunID: run.ID, OSUser: osUser, Domain: domain, AppDir: run.AppDir, WorkDir: run.WorkDir,
		Command: run.Command, Packages: run.PackageList(), NoDev: run.NoDev,
	}, &resp)
	if err != nil {
		finish(run, models.ComposerRunFailed)
		if ae, ok := Refusal(err); ok {
			// The agent's own words for what the run lacks, written for the
			// user ("... does not run PHP: enable PHP on the domain first").
			run.Error = clip(ae.Message, 1024)
		} else {
			// The unit may have started before the call failed: end it, so it
			// doesn't run on with the row saying it failed. What went wrong
			// (systemd-run's stderr, say) is for the server log, not the user
			// (JAB-114).
			_ = Stop(context.WithoutCancel(ctx), ag, run.ID)
			run.Error = "the server could not start the run"
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
// stored with its result and its unit released. A run still going gets the
// log so far, which is not stored. A run not running is left alone.
func Refresh(ctx context.Context, ag agent.AgentInterface, repo repository.ComposerRunRepository, run *models.ComposerRun) error {
	if run.State != models.ComposerRunRunning {
		return nil
	}
	st, err := Status(ctx, ag, run.ID)
	if err != nil {
		return err
	}
	switch st.State {
	case "running":
		run.Log, run.LogTruncated = st.Log, st.LogTruncated
		return nil
	case "succeeded":
		finish(run, models.ComposerRunSucceeded)
	case "failed":
		finish(run, models.ComposerRunFailed)
	case "timed_out":
		finish(run, models.ComposerRunTimedOut)
	case "gone":
		// The unit went away before the panel stored its result: the server
		// restarted, or someone removed it. Its exit status is lost.
		finish(run, models.ComposerRunFailed)
		run.Error = "the run ended without the panel seeing its result (was the server restarted?)"
	default:
		return fmt.Errorf("composer.run_status: unknown state %q", st.State)
	}
	run.Result, run.ExitCode = st.Result, st.ExitCode
	run.PHPDefenseBlocked = st.PHPDefenseBlocked
	run.Log, run.LogTruncated = st.Log, st.LogTruncated
	if err := repo.Save(ctx, run); err != nil {
		return fmt.Errorf("store the run's result: %w", err)
	}
	if st.State != "gone" {
		if err := Stop(ctx, ag, run.ID); err != nil {
			// The result is stored; the unit stays loaded, holding nothing.
			slog.WarnContext(ctx, "composer: release the run's unit", "run", run.ID, "error", err)
		}
	}
	return nil
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

// ErrNotRunning: the run has already finished.
var ErrNotRunning = errors.New("composerrun: the run has already finished")

// Cancel ends a running run and stores it as cancelled, with its log. A run
// that finished meanwhile is stored with its own result instead, and
// ErrNotRunning returned.
func Cancel(ctx context.Context, ag agent.AgentInterface, repo repository.ComposerRunRepository, run *models.ComposerRun) error {
	if err := Refresh(ctx, ag, repo, run); err != nil {
		return err
	}
	if run.State != models.ComposerRunRunning {
		return ErrNotRunning
	}
	if err := Stop(ctx, ag, run.ID); err != nil {
		return err
	}
	finish(run, models.ComposerRunCancelled)
	if st, err := Status(ctx, ag, run.ID); err == nil {
		run.Log, run.LogTruncated = st.Log, st.LogTruncated
	}
	return repo.Save(ctx, run)
}

// Status asks the agent for a run's state and log.
func Status(ctx context.Context, ag agent.AgentInterface, runID string) (*StatusResponse, error) {
	var st StatusResponse
	if err := call(ctx, ag, "composer.run_status", RunIDRequest{RunID: runID}, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Stop asks the agent to stop a run's unit and unload it.
func Stop(ctx context.Context, ag agent.AgentInterface, runID string) error {
	var resp StopResponse
	return call(ctx, ag, "composer.run_stop", RunIDRequest{RunID: runID}, &resp)
}

func finish(run *models.ComposerRun, state string) {
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
