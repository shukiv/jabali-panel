package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"golang.org/x/sys/unix"
)

// git_deploy.go — Git deployments (GH #2277, ADR-0178), the agent's side.
//
//   git.deploy_start   start a deployment, or a rollback, as a detached unit
//   git.deploy_status  the run's state, exit code, log and the commit it fetched
//   git.deploy_stop    cancel a run, or release a finished one, and remove its
//                      credential file
//   git.releases       the domain's releases, and the one live
//
// A run is jabali-gitdeploy-<run id>.service, shaped like a Composer run
// (composer_runs.go): the tenant's uid in their slice, Type=oneshot with
// RemainAfterExit so its result outlives it, in the SSH sandbox. It runs
// `jabali-agent git-deploy <settings>` (git_deploy_run.go). Root checks
// everything a run needs first, and resolves and pins the Git host; the run
// itself does nothing as root.

// gitDeployKeep is how many releases a deployment keeps, the live one aside.
const gitDeployKeep = 5

// gitDeployAgentBin is the agent binary the run executes, as installed
// (install.sh AGENT_BIN_PATH). /usr is visible in the sandbox.
var gitDeployAgentBin = "/usr/local/bin/jabali-agent"

// gitDeployRunProps are a run's limits, on top of the user's slice.
// TimeoutStartSec caps the whole run; TimeoutStopSec gives a cancelled run
// time to remove its unfinished release.
var gitDeployRunProps = []string{
	"--property=TimeoutStartSec=1800",
	"--property=TimeoutStopSec=20",
	"--property=MemoryMax=2G",
	"--property=TasksMax=256",
	"--property=CPUWeight=50",
	"--property=IOWeight=50",
}

func gitDeployUnit(runID string) string { return "jabali-gitdeploy-" + runID + ".service" }

// gitDeployCredName is a run's directory in ~/.jabali-git.
func gitDeployCredName(runID string) string { return "deploy-" + runID }

// --- git.deploy_start ---

type gitDeployStartParams struct {
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
	// Token is never logged or echoed. "" fetches a public repository.
	Token       string   `json:"token,omitempty"`
	WebDir      string   `json:"web_dir,omitempty"`
	SharedPaths []string `json:"shared_paths,omitempty"`
	Commands    string   `json:"commands,omitempty"`
	// Composer: the owner's hosting package allows Composer runs.
	Composer bool `json:"composer,omitempty"`
}

type gitDeployStartResponse struct {
	Unit       string `json:"unit"`
	PHPVersion string `json:"php_version,omitempty"`
}

// gitDeployArgv is the systemd-run argv of a run.
func gitDeployArgv(p gitDeployStartParams, settings string) []string {
	args := []string{
		"--unit=" + gitDeployUnit(p.RunID),
		"--no-block",
		"--uid=" + p.OSUser,
		"--gid=" + p.OSUser,
		"--slice=jabali-user-" + p.OSUser + ".slice",
		"--service-type=oneshot",
		"--remain-after-exit",
		"--description=jabali git " + p.Kind + " " + p.Domain,
	}
	args = append(args, gitDeployRunProps...)
	return append(args, "--", composerSandboxShell, "--exec", gitDeployAgentBin, "git-deploy", settings)
}

func gitDeployInvalid(msg string) error {
	return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
}

func gitDeployPrecondition(msg string) error {
	return &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: msg}
}

func gitDeployStartHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p gitDeployStartParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, gitDeployInvalid("parse params: " + err.Error())
	}
	if !ulidRE.MatchString(p.RunID) {
		return nil, gitDeployInvalid("run_id must be a 26-char ULID")
	}
	if !osUserSafeRE.MatchString(p.OSUser) || !domainRegex.MatchString(p.Domain) {
		return nil, gitDeployInvalid("invalid os_user or domain")
	}
	if err := cronvalidate.ValidateDeployLayout(p.WebDir, p.SharedPaths); err != nil {
		return nil, gitDeployInvalid(err.Error())
	}
	uid, gid, err := gitHostingAccount(p.OSUser)
	if err != nil {
		return nil, gitDeployInvalid(err.Error())
	}
	if !composerIsDir(p.OSUser, filepath.Join("domains", p.Domain)) {
		return nil, gitDeployPrecondition("the domain's directory does not exist or is a link")
	}
	m := gitDeployManifest{
		RunID: p.RunID, Home: tenantHomeDir(p.OSUser), DomainDir: composerDomainDir(p.OSUser, p.Domain),
		WebDir: p.WebDir, SharedPaths: p.SharedPaths,
	}
	resp := gitDeployStartResponse{Unit: gitDeployUnit(p.RunID)}

	host, username := "", ""
	switch p.Kind {
	case "rollback":
		if !ulidRE.MatchString(p.Release) {
			return nil, gitDeployInvalid("release must be a 26-char ULID")
		}
		if !composerIsDir(p.OSUser, filepath.Join("domains", p.Domain, "releases", p.Release)) {
			return nil, gitDeployPrecondition("release " + p.Release + " is no longer on the server")
		}
		m.Mode, m.Release = gitDeployModeActivate, p.Release
	case "deploy":
		var gitURL string
		host, gitURL, err = gitRepoURL(p.RepoURL)
		if err != nil {
			return nil, gitDeployInvalid(err.Error())
		}
		if !validGitBranch(p.Branch) {
			return nil, gitDeployInvalid(fmt.Sprintf("%q is not a branch name", p.Branch))
		}
		if p.Username != "" && !gitUsernameRE.MatchString(p.Username) {
			return nil, gitDeployInvalid("username may hold only letters, digits and . _ + @ -, up to 128")
		}
		if p.Token != "" && !gitTokenRE.MatchString(p.Token) {
			return nil, gitDeployInvalid("the token must be up to 512 printable characters, without spaces")
		}
		if _, err := cronvalidate.ValidateDeployCommands(p.Commands); err != nil {
			return nil, gitDeployInvalid("deploy commands: " + err.Error())
		}
		gitBin, err := exec.LookPath("git")
		if err != nil {
			return nil, gitDeployPrecondition("git is not installed on the server")
		}
		if err := gitCheckVersion(ctx, gitBin); err != nil {
			return nil, gitDeployPrecondition(err.Error())
		}
		resolve, err := gitPinHost(ctx, host)
		if err != nil {
			return nil, gitDeployPrecondition(err.Error())
		}
		m.Mode, m.Release = gitDeployModeDeploy, p.RunID
		m.RepoURL, m.Branch, m.GitBin, m.Resolve = gitURL, p.Branch, gitBin, resolve
		m.Commands, m.Keep = p.Commands, gitDeployKeep
		username = p.Username
		if username == "" {
			username = gitDefaultUsername
		}
	default:
		return nil, gitDeployInvalid(fmt.Sprintf("kind %q is not deploy or rollback", p.Kind))
	}

	if php, ver, err := composerDomainPHP(p.Domain, p.OSUser); err != nil {
		m.PHPError = err.Error()
	} else {
		m.PHPBin, resp.PHPVersion = php, ver
	}
	if m.Mode == gitDeployModeDeploy {
		if p.Composer {
			if err := gitDeployComposerSetup(&m, p.OSUser); err != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
			}
		} else {
			m.ComposerUnavailable = "the hosting package does not include Composer runs"
		}
	}

	if st := composerUnitShow(ctx, resp.Unit); st["LoadState"] == "loaded" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeAlreadyExists, Message: resp.Unit + " already exists"}
	}
	credName := gitDeployCredName(p.RunID)
	if m.Mode == gitDeployModeDeploy && p.Token != "" {
		cf, err := writeGitCredFileIn(p.OSUser, uid, gid, credName, host, username, p.Token)
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
		}
		// The run reads it, and removes it once it has fetched the branch.
		m.CredFile = cf.Path
		cf.Close()
	}
	settings, err := encodeGitDeployManifest(m)
	if err == nil {
		// What the run will check, checked here first.
		_, err = decodeGitDeployManifest(settings)
	}
	if err != nil {
		_ = removeGitCredDir(p.OSUser, credName)
		return nil, gitDeployInvalid(err.Error())
	}
	out, err := execCommandContext(ctx, "systemd-run", gitDeployArgv(p, settings)...).CombinedOutput()
	if err != nil {
		_ = removeGitCredDir(p.OSUser, credName)
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))}
	}
	return resp, nil
}

// gitDeployComposerSetup fills in how a deployment runs Composer: the
// phar, its home and the Composer PHP Defense rules, as a Composer run has
// them. Composer missing from the server is the run's to report, when the
// project needs it.
func gitDeployComposerSetup(m *gitDeployManifest, osUser string) error {
	phar, err := composerPharFor(osUser)
	if err != nil {
		m.ComposerUnavailable = err.Error()
		return nil
	}
	if err := ensureComposerHome(osUser); err != nil {
		return err
	}
	scanDir, err := ensureComposerPHPDefense()
	if err != nil {
		return err
	}
	m.Composer = &gitDeployComposer{Phar: phar, ScanDir: scanDir, Env: composerEnv(osUser)}
	return nil
}

// --- git.deploy_status ---

type gitDeployStatusResponse struct {
	// State is running, succeeded, failed, timed_out or gone, as a Composer
	// run's.
	State             string `json:"state"`
	Result            string `json:"result,omitempty"`
	ExitCode          *int   `json:"exit_code,omitempty"`
	Log               string `json:"log"`
	LogTruncated      bool   `json:"log_truncated,omitempty"`
	PHPDefenseBlocked bool   `json:"php_defense_blocked,omitempty"`
	// Commit is the commit the deployment fetched, once it has.
	Commit string `json:"commit,omitempty"`
}

// gitDeployCommitScanLines bounds how far into a run's log the commit line
// is looked for: it comes right after the clone.
const gitDeployCommitScanLines = 500

// gitDeployCommit is the commit from the first commit line in a run's log.
// Later lines don't count: the repository's code runs after it, and could
// print one.
func gitDeployCommit(ctx context.Context, unit string) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := execCommandContext(ctx, "journalctl", "_SYSTEMD_UNIT="+unit, "-o", "cat", "--no-pager")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return ""
	}
	if err := cmd.Start(); err != nil {
		return ""
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 0; n < gitDeployCommitScanLines && sc.Scan(); n++ {
		if c, ok := strings.CutPrefix(sc.Text(), gitDeployCommitMarker); ok {
			if gitCommitRE.MatchString(c) {
				return c
			}
			return ""
		}
	}
	return ""
}

func gitDeployStatusHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p composerRunIDParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, gitDeployInvalid("parse params: " + err.Error())
	}
	if !ulidRE.MatchString(p.RunID) {
		return nil, gitDeployInvalid("run_id must be a 26-char ULID")
	}
	unit := gitDeployUnit(p.RunID)
	resp := gitDeployStatusResponse{}
	resp.State, resp.Result, resp.ExitCode = composerRunState(composerUnitShow(ctx, unit))
	resp.Log, resp.LogTruncated = composerRunLog(ctx, unit)
	resp.Commit = gitDeployCommit(ctx, unit)
	if composerRunBlockedRE.MatchString(resp.Log) {
		resp.PHPDefenseBlocked = true
		if resp.State == "succeeded" {
			resp.State = "failed"
		}
	}
	return resp, nil
}

// --- git.deploy_stop ---

type gitDeployStopParams struct {
	RunID  string `json:"run_id"`
	OSUser string `json:"os_user"`
}

type gitDeployStopResponse struct {
	Unit string `json:"unit"`
}

// gitDeployStopHandler stops a run's unit, ending a deployment in progress,
// clears a failed one, and removes the run's credential file, however the
// run ended. A unit already gone is not an error.
func gitDeployStopHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p gitDeployStopParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, gitDeployInvalid("parse params: " + err.Error())
	}
	if !ulidRE.MatchString(p.RunID) || !osUserSafeRE.MatchString(p.OSUser) {
		return nil, gitDeployInvalid("run_id must be a 26-char ULID, with a valid os_user")
	}
	unit := gitDeployUnit(p.RunID)
	if composerUnitShow(ctx, unit)["LoadState"] == "loaded" {
		if out, err := execCommandContext(ctx, "systemctl", "stop", unit).CombinedOutput(); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemctl stop %s: %v: %s", unit, err, strings.TrimSpace(string(out)))}
		}
		_ = execCommandContext(ctx, "systemctl", "reset-failed", unit).Run()
	}
	if err := removeGitCredDir(p.OSUser, gitDeployCredName(p.RunID)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	return gitDeployStopResponse{Unit: unit}, nil
}

// --- git.releases ---

type gitReleasesParams struct {
	OSUser string `json:"os_user"`
	Domain string `json:"domain"`
}

type gitReleasesResponse struct {
	// Releases are the release directories, newest first.
	Releases []string `json:"releases"`
	// Live is the release current points at; "" when current is missing or
	// points elsewhere.
	Live string `json:"live"`
}

// gitDeployReadReleases reads domains/<d>/releases and domains/<d>/current
// without following a link (RESOLVE_NO_SYMLINKS) or leaving the home
// (RESOLVE_BENEATH): the agent is root and the tenant owns these.
func gitDeployReadReleases(osUser, domain string) (gitReleasesResponse, error) {
	resp := gitReleasesResponse{Releases: []string{}}
	home, err := os.Open(tenantHomeDir(osUser))
	if err != nil {
		return resp, err
	}
	defer home.Close()
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	}
	dirFD, err := unix.Openat2(int(home.Fd()), filepath.Join("domains", domain), how)
	if err != nil {
		return resp, fmt.Errorf("open the domain's directory: %w", err)
	}
	defer unix.Close(dirFD)

	buf := make([]byte, 256)
	if n, err := unix.Readlinkat(dirFD, "current", buf); err == nil {
		if name, ok := strings.CutPrefix(string(buf[:n]), "releases/"); ok && ulidRE.MatchString(name) {
			resp.Live = name
		}
	}
	relFD, err := unix.Openat2(dirFD, "releases", how)
	if errors.Is(err, unix.ENOENT) {
		return resp, nil
	}
	if err != nil {
		return resp, fmt.Errorf("open releases: %w", err)
	}
	f := os.NewFile(uintptr(relFD), "releases")
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return resp, fmt.Errorf("list releases: %w", err)
	}
	for _, e := range entries {
		// A link is not a release: DirEntry.IsDir does not follow it.
		if e.IsDir() && ulidRE.MatchString(e.Name()) {
			resp.Releases = append(resp.Releases, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(resp.Releases)))
	return resp, nil
}

func gitReleasesHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p gitReleasesParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, gitDeployInvalid("parse params: " + err.Error())
	}
	if !osUserSafeRE.MatchString(p.OSUser) || !domainRegex.MatchString(p.Domain) {
		return nil, gitDeployInvalid("invalid os_user or domain")
	}
	resp, err := gitDeployReadReleases(p.OSUser, p.Domain)
	if err != nil {
		return nil, gitDeployPrecondition(err.Error())
	}
	return resp, nil
}

func init() {
	Default.Register("git.deploy_start", gitDeployStartHandler)
	Default.Register("git.deploy_status", gitDeployStatusHandler)
	Default.Register("git.deploy_stop", gitDeployStopHandler)
	Default.Register("git.releases", gitReleasesHandler)
}
