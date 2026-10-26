package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"golang.org/x/sys/unix"
)

// git_remote.go — a domain's Git repository (GH #2277, ADR-0178).
//
//   git.ls_remote  the repository's branches and default branch
//
// git runs as the tenant, inside their SSH sandbox (`jabali-ssh-shell
// --exec`, as Composer runs do), through systemd-run --pipe --wait. A token
// reaches git only through a credential-store file,
// ~/.jabali-git/<random>/credentials, 0600 and the tenant's, removed when the
// call ends. Never argv or the environment: a unit's ExecStart is readable by
// any local user through systemctl show.
//
// The agent resolves the repository's host once, refuses a local or private
// address, and pins git to the address it checked (http.curloptResolve), with
// redirects off: the server, not the user, makes the connection.

const (
	// gitLsRemoteMaxSec caps the unit; the call returns well inside the
	// panel's 30 s request timeout.
	gitLsRemoteMaxSec = 20
	// gitMaxBranches bounds the branches one answer lists.
	gitMaxBranches = 1000
	// gitOutputMax bounds what the agent reads of git's stdout.
	gitOutputMax = 4 << 20
	// gitDefaultUsername is sent when the user gives no username: the name
	// GitHub App installation tokens use. Other tokens need the name their
	// host expects (a Bitbucket repository token: x-token-auth), which the
	// user enters.
	gitDefaultUsername = "x-access-token"
)

// gitCredRootName is the directory in a tenant's home that holds the
// credential files of calls in progress.
const gitCredRootName = ".jabali-git"

var (
	// gitHostRE is a DNS name or an IPv4 address; no IPv6 literal.
	gitHostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	// gitURLCharsRE is every character a repository URL may hold. No '%' or
	// '$' (systemd expands both in ExecStart), no '@' (credentials go in the
	// credential file), no '?', '#' or spaces.
	gitURLCharsRE = regexp.MustCompile(`^[A-Za-z0-9.:/_~+-]+$`)
	gitPathRE     = regexp.MustCompile(`^/[A-Za-z0-9._~+-]+(/[A-Za-z0-9._~+-]+)*/?$`)
	// gitUsernameRE covers GitHub, GitLab (deploy tokens are
	// gitlab+deploy-token-N) and Bitbucket usernames.
	gitUsernameRE = regexp.MustCompile(`^[A-Za-z0-9._+@-]{1,128}$`)
	// gitTokenRE is printable ASCII without spaces.
	gitTokenRE = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)
)

// gitRepoURL checks a repository URL: https only, a host, an optional port, a
// path, nothing else. It returns the URL's host[:port] and the URL git is
// given, rebuilt from them with the host in lower case, so the address
// gitPinHost pins is the one git looks up.
func gitRepoURL(raw string) (host, gitURL string, err error) {
	if len(raw) > 512 || !gitURLCharsRE.MatchString(raw) {
		return "", "", errors.New("repo_url must be an https:// URL of letters, digits and . : / _ ~ + -, with no username or token in it")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("repo_url must be an https:// URL with no username, token, query or fragment")
	}
	hostname := strings.ToLower(u.Hostname())
	if !gitHostRE.MatchString(hostname) {
		return "", "", errors.New("repo_url needs a host name")
	}
	host = hostname
	// "host:" with no port parses, but would leave the pin without one.
	if strings.HasSuffix(u.Host, ":") {
		return "", "", errors.New("repo_url has an invalid port")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", "", errors.New("repo_url has an invalid port")
		}
		host = hostname + ":" + strconv.Itoa(n)
	}
	if !gitPathRE.MatchString(u.Path) {
		return "", "", errors.New("repo_url needs a repository path")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return "", "", errors.New("repo_url may not contain . or .. segments")
		}
	}
	return host, "https://" + host + u.Path, nil
}

// gitHostingAccount refuses anyone but a hosting user, as composerRunAccount
// does: the SSH sandbox never jails uid 0 (GH #658).
func gitHostingAccount(osUser string) (uid, gid int, err error) {
	u, err := user.Lookup(osUser)
	if err != nil {
		return 0, 0, fmt.Errorf("look up %s: %w", osUser, err)
	}
	uid, err = strconv.Atoi(u.Uid)
	if err != nil || uid < minTenantUID || uid >= 60000 {
		return 0, 0, fmt.Errorf("%s is not a hosting user: git runs only as one", osUser)
	}
	gid, err = strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("gid of %s: %w", osUser, err)
	}
	return uid, gid, nil
}

// gitCredFile is a credential-store file for one call. Remove deletes it.
type gitCredFile struct {
	Path   string
	rootFD int
	subFD  int
	name   string
}

// writeGitCredFile writes a credential-store file for host into a new
// directory under osUser's ~/.jabali-git, owned by uid:gid, 0700/0600.
//
// The agent is root and a tenant may own their home, so nothing here follows
// a link: ~/.jabali-git must be a real directory (O_NOFOLLOW), and the call's
// directory and file are created through directory descriptors.
func writeGitCredFile(osUser string, uid, gid int, host, username, token string) (*gitCredFile, error) {
	root := filepath.Join(tenantHomeDir(osUser), gitCredRootName)
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create %s: %w", root, err)
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", root, err)
	}
	ok := false
	defer func() {
		if !ok {
			unix.Close(rootFD)
		}
	}()
	if err := unix.Fchown(rootFD, uid, gid); err != nil {
		return nil, fmt.Errorf("chown %s: %w", root, err)
	}
	if err := unix.Fchmod(rootFD, 0o700); err != nil {
		return nil, fmt.Errorf("chmod %s: %w", root, err)
	}

	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	name := hex.EncodeToString(rnd[:])
	if err := unix.Mkdirat(rootFD, name, 0o700); err != nil {
		return nil, fmt.Errorf("create %s/%s: %w", root, name, err)
	}
	subFD, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Unlinkat(rootFD, name, unix.AT_REMOVEDIR)
		return nil, fmt.Errorf("open %s/%s: %w", root, name, err)
	}
	cf := &gitCredFile{Path: filepath.Join(root, name, "credentials"), rootFD: rootFD, subFD: subFD, name: name}
	ok = true // from here cf owns rootFD
	if err := cf.write(uid, gid, host, username, token); err != nil {
		cf.Remove()
		return nil, err
	}
	return cf, nil
}

func (cf *gitCredFile) write(uid, gid int, host, username, token string) error {
	if err := unix.Fchown(cf.subFD, uid, gid); err != nil {
		return fmt.Errorf("chown the credential directory: %w", err)
	}
	fd, err := unix.Openat(cf.subFD, "credentials", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("create the credential file: %w", err)
	}
	f := os.NewFile(uintptr(fd), cf.Path)
	defer f.Close()
	if err := f.Chown(uid, gid); err != nil {
		return fmt.Errorf("chown the credential file: %w", err)
	}
	// git-credential-store's format: one URL per line, the username and token
	// percent-encoded.
	line := (&url.URL{Scheme: "https", User: url.UserPassword(username, token), Host: host}).String() + "\n"
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("write the credential file: %w", err)
	}
	return nil
}

// Remove deletes the credential file and its directory. git may have left
// a lock file beside it.
func (cf *gitCredFile) Remove() {
	_ = unix.Unlinkat(cf.subFD, "credentials", 0)
	_ = unix.Unlinkat(cf.subFD, "credentials.lock", 0)
	unix.Close(cf.subFD)
	_ = unix.Unlinkat(cf.rootFD, cf.name, unix.AT_REMOVEDIR)
	unix.Close(cf.rootFD)
}

// gitEnv is git's environment in the sandbox, as `env` arguments: no system
// or user config (a tenant's ~/.gitconfig could add a helper or rewrite the
// URL), never prompting, https only (redirects and submodules included).
func gitEnv(osUser string) []string {
	return []string{
		"HOME=" + tenantHomeDir(osUser),
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=https",
	}
}

// gitAuthArgs are the -c options that make git read credentials only from
// credFile. The empty helper clears any helper set before it. credFile ""
// (no token) leaves git with no helper.
func gitAuthArgs(credFile string) []string {
	args := []string{"-c", "credential.helper="}
	if credFile != "" {
		args = append(args, "-c", "credential.helper=store --file="+credFile)
	}
	return args
}

// gitLookupIP resolves a repository's host. A var so tests need no DNS.
var gitLookupIP = net.DefaultResolver.LookupIP

// gitBlockedNets are ranges net.IP's own tests leave out: "this network",
// CGNAT, benchmarking, reserved, deprecated IPv6 site-local and
// IPv4-compatible space, and the local-use NAT64 prefix (RFC 8215).
var gitBlockedNets = func() []*net.IPNet {
	var nets []*net.IPNet
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "198.18.0.0/15", "240.0.0.0/4", "fec0::/10", "::/96", "64:ff9b:1::/48"} {
		_, n, _ := net.ParseCIDR(cidr)
		nets = append(nets, n)
	}
	return nets
}()

// gitEmbeddingNets carry an IPv4 address in their low bits: NAT64
// (64:ff9b::/96, the IPv4 address in the last 32 bits) and 6to4 (2002::/16,
// in bits 16-48). A gateway would take the connection to that IPv4 address.
var (
	_, gitNAT64, _ = net.ParseCIDR("64:ff9b::/96")
	_, git6to4, _  = net.ParseCIDR("2002::/16")
)

// gitBlockedIP refuses what the panel's ssrfguard refuses with private hosts
// not allowed: the server connects, so a user must not reach its loopback,
// link-local (cloud metadata), private or otherwise non-public neighbours
// through it.
func gitBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsPrivate() {
		return true
	}
	for _, n := range gitBlockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	if ip16 := ip.To16(); ip.To4() == nil && ip16 != nil {
		if gitNAT64.Contains(ip) {
			return gitBlockedIP(net.IP(ip16[12:16]))
		}
		if git6to4.Contains(ip) {
			return gitBlockedIP(net.IP(ip16[2:6]))
		}
	}
	return false
}

// gitPinHost resolves host[:port] once, refuses it when any address is
// blocked, and returns the http.curloptResolve entry that makes git connect
// to the address checked here and nowhere else. Checking in the panel and
// letting git resolve again would leave a DNS rebind in between.
func gitPinHost(ctx context.Context, host string) (string, error) {
	hostname, port := host, "443"
	if h, p, err := net.SplitHostPort(host); err == nil {
		hostname = h
		if p != "" {
			port = p
		}
	}
	hostname = strings.ToLower(hostname)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := gitLookupIP(ctx, "ip", hostname)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("the Git host's name does not resolve: %s", hostname)
	}
	var pick net.IP
	for _, ip := range ips {
		if gitBlockedIP(ip) {
			return "", fmt.Errorf("the server won't connect to %s: it resolves to a local or private address", hostname)
		}
		if pick == nil || (pick.To4() == nil && ip.To4() != nil) {
			pick = ip
		}
	}
	addr := pick.String()
	if pick.To4() == nil {
		addr = "[" + addr + "]"
	}
	return hostname + ":" + port + ":" + addr, nil
}

// gitMinMajor and gitMinMinor are the oldest git the agent runs: 2.37 added
// http.curloptResolve (2.32 GIT_CONFIG_GLOBAL). An older git ignores both
// without a word, so it would neither pin the address nor skip the user's
// config.
const gitMinMajor, gitMinMinor = 2, 37

// gitVersionOutput runs `git --version`. A var so tests can name a version.
var gitVersionOutput = func(ctx context.Context, gitBin string) (string, error) {
	out, err := execCommandContext(ctx, gitBin, "--version").Output()
	return string(out), err
}

var gitVersionRE = regexp.MustCompile(`^git version (\d+)\.(\d+)`)

// gitCheckVersion refuses a git older than gitMinMajor.gitMinMinor.
func gitCheckVersion(ctx context.Context, gitBin string) error {
	out, err := gitVersionOutput(ctx, gitBin)
	m := gitVersionRE.FindStringSubmatch(strings.TrimSpace(out))
	if err != nil || m == nil {
		return errors.New("could not tell which git the server has")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < gitMinMajor || (major == gitMinMajor && minor < gitMinMinor) {
		return fmt.Errorf("git %d.%d on this server is too old for Git deploy: it needs %d.%d or newer", major, minor, gitMinMajor, gitMinMinor)
	}
	return nil
}

// gitNetArgs pin the connection to the checked address and keep git on it:
// no redirects, which could lead to another host, and no proxy. A stalled
// server fails the call instead of holding it to the unit's cap.
func gitNetArgs(resolve string) []string {
	return []string{
		"-c", "http.curloptResolve=" + resolve,
		"-c", "http.followRedirects=false",
		"-c", "http.proxy=",
		"-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=10",
	}
}

// gitLsRemoteArgv is the systemd-run argv of one ls-remote.
func gitLsRemoteArgv(osUser, gitBin, credFile, resolve, repoURL string) []string {
	args := []string{
		"--quiet",
		"--uid=" + osUser,
		"--gid=" + osUser,
		"--slice=jabali-user-" + osUser + ".slice",
		"--pipe",
		"--wait",
		"--collect",
		"--description=jabali git ls-remote",
		"--property=RuntimeMaxSec=" + strconv.Itoa(gitLsRemoteMaxSec),
		"--property=MemoryMax=512M",
		"--property=TasksMax=64",
		"--", composerSandboxShell, "--exec", envBin,
	}
	args = append(args, gitEnv(osUser)...)
	// The sandbox starts in the home directory; from / git finds no
	// repository, so no repository config (a url.insteadOf, a proxy) applies.
	args = append(args, gitBin, "-C", "/")
	args = append(args, gitAuthArgs(credFile)...)
	args = append(args, gitNetArgs(resolve)...)
	return append(args, "ls-remote", "--symref", "--", repoURL, "HEAD", "refs/heads/*")
}

// gitLsRemoteResult is what ls-remote says about a repository.
type gitLsRemoteResult struct {
	DefaultBranch string   `json:"default_branch"`
	Branches      []string `json:"branches"`
	Truncated     bool     `json:"truncated,omitempty"`
}

// parseGitLsRemote reads `git ls-remote --symref <url> HEAD refs/heads/*`.
func parseGitLsRemote(out string) gitLsRemoteResult {
	res := gitLsRemoteResult{Branches: []string{}}
	for _, line := range strings.Split(out, "\n") {
		left, ref, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		if target, isSym := strings.CutPrefix(left, "ref: "); isSym {
			if ref == "HEAD" {
				res.DefaultBranch = strings.TrimPrefix(target, "refs/heads/")
			}
			continue
		}
		if name, isBranch := strings.CutPrefix(ref, "refs/heads/"); isBranch && name != "" {
			if len(res.Branches) >= gitMaxBranches {
				res.Truncated = true
				continue
			}
			res.Branches = append(res.Branches, name)
		}
	}
	sort.Strings(res.Branches)
	return res
}

// gitRemoteError turns git's stderr into a message for the user, with
// git's own last "fatal:" line, which names the URL (no credentials in it).
// Lines the remote server sent ("remote: ...") are left out.
func gitRemoteError(stderr string) string {
	last := ""
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "fatal: ") {
			last = l
		}
	}
	reason := "git could not read the repository"
	switch s := stderr; {
	case strings.Contains(s, "could not read Username"), strings.Contains(s, "could not read Password"),
		strings.Contains(s, "Authentication failed"), strings.Contains(s, "terminal prompts disabled"):
		reason = "the Git host wants a username and token, or refused the ones given"
	case strings.Contains(s, "not found"):
		reason = "the Git host has no such repository, or the token can't read it"
	case strings.Contains(s, "Could not resolve host"):
		reason = "the Git host's name does not resolve"
	case strings.Contains(s, "SSL certificate problem"), strings.Contains(s, "server certificate verification failed"):
		reason = "the Git host's certificate is not trusted"
	case strings.Contains(s, "Failed to connect"), strings.Contains(s, "Connection timed out"),
		strings.Contains(s, "Operation timed out"), strings.Contains(s, "Connection refused"):
		reason = "the server could not connect to the Git host"
	}
	if last == "" {
		return reason
	}
	return clipBytes(reason+": "+last, 512)
}

// clipBytes cuts s to at most n bytes without splitting a character.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	max int
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return len(p), nil
}

// --- git.ls_remote ---

type gitLsRemoteParams struct {
	OSUser   string `json:"os_user"`
	RepoURL  string `json:"repo_url"`
	Username string `json:"username,omitempty"`
	// Token is never logged or echoed. "" reads a public repository.
	Token string `json:"token,omitempty"`
}

func gitLsRemoteHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p gitLsRemoteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !osUserSafeRE.MatchString(p.OSUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid os_user"}
	}
	host, gitURL, err := gitRepoURL(p.RepoURL)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	if p.Username != "" && !gitUsernameRE.MatchString(p.Username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username may hold only letters, digits and . _ + @ -, up to 128"}
	}
	if p.Token != "" && !gitTokenRE.MatchString(p.Token) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "the token must be up to 512 printable characters, without spaces"}
	}
	uid, gid, err := gitHostingAccount(p.OSUser)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "git is not installed on the server"}
	}
	if err := gitCheckVersion(ctx, gitBin); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: err.Error()}
	}
	resolve, err := gitPinHost(ctx, host)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: err.Error()}
	}

	credFile := ""
	if p.Token != "" {
		username := p.Username
		if username == "" {
			username = gitDefaultUsername
		}
		cf, err := writeGitCredFile(p.OSUser, uid, gid, host, username, p.Token)
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
		}
		defer cf.Remove()
		credFile = cf.Path
	}

	ctx, cancel := context.WithTimeout(ctx, (gitLsRemoteMaxSec+5)*time.Second)
	defer cancel()
	cmd := execCommandContext(ctx, "systemd-run", gitLsRemoteArgv(p.OSUser, gitBin, credFile, resolve, gitURL)...)
	stdout := &limitedBuffer{max: gitOutputMax}
	stderr := &tailWriter{max: 16 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		msg := gitRemoteError(string(stderr.buf))
		if ctx.Err() != nil {
			msg = "the Git host did not answer in time"
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: msg}
	}
	return parseGitLsRemote(stdout.buf.String()), nil
}

func init() {
	Default.Register("git.ls_remote", gitLsRemoteHandler)
}
