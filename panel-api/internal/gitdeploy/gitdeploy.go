// Package gitdeploy holds what the panel knows about deploying a domain from
// a Git repository (GH #2277, ADR-0178): it checks a repository URL, a
// username, a token and a branch, and asks the agent for a repository's
// branches.
//
// The agent's git.ls_remote checks the URL, username and token again
// (panel-agent/internal/commands/git_remote.go); the two must agree.
package gitdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssrfguard"
)

// DefaultUsername is what the agent sends with a token when the user gives
// no username: the name GitHub App installation tokens use. Other tokens need
// the name their host expects (a Bitbucket repository token: x-token-auth).
const DefaultUsername = "x-access-token"

var (
	hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	// urlCharsRE: no '%' or '$' (systemd expands both in a unit's
	// ExecStart), no '@' (credentials never go in the URL), no '?', '#' or
	// spaces.
	urlCharsRE = regexp.MustCompile(`^[A-Za-z0-9.:/_~+-]+$`)
	pathRE     = regexp.MustCompile(`^/[A-Za-z0-9._~+-]+(/[A-Za-z0-9._~+-]+)*/?$`)
	usernameRE = regexp.MustCompile(`^[A-Za-z0-9._+@-]{1,128}$`)
	tokenRE    = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)
	branchRE   = regexp.MustCompile(`^[A-Za-z0-9._/+-]{1,255}$`)
)

// RepoURL is a checked repository URL.
type RepoURL struct {
	// Raw is the URL as the user gave it.
	Raw string
	// Host is host[:port], lower case: a stored token is bound to it.
	Host string
	// Hostname is Host without the port.
	Hostname string
}

// ParseRepoURL checks a repository URL: https only, a host name, an optional
// port and a repository path, with no username, token, query or fragment.
func ParseRepoURL(raw string) (*RepoURL, error) {
	if len(raw) > 512 || !urlCharsRE.MatchString(raw) {
		return nil, errors.New("the repository URL must be an https:// URL of letters, digits and . : / _ ~ + -, with no username or token in it")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("the repository URL must be an https:// URL with no username, token, query or fragment")
	}
	if !hostRE.MatchString(u.Hostname()) {
		return nil, errors.New("the repository URL needs a host name")
	}
	// "host:" with no port parses, but names no port.
	if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("the repository URL has an invalid port")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, errors.New("the repository URL has an invalid port")
		}
	}
	if !pathRE.MatchString(u.Path) {
		return nil, errors.New("the repository URL needs the repository's path, such as /owner/app.git")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return nil, errors.New("the repository URL may not contain . or ..")
		}
	}
	return &RepoURL{Raw: raw, Host: strings.ToLower(u.Host), Hostname: strings.ToLower(u.Hostname())}, nil
}

// ValidateUsername checks a username; "" means DefaultUsername.
func ValidateUsername(s string) error {
	if s != "" && !usernameRE.MatchString(s) {
		return errors.New("the username may hold only letters, digits and . _ + @ -, up to 128")
	}
	return nil
}

// ValidateToken checks a token: printable ASCII without spaces, up to 512.
func ValidateToken(s string) error {
	if !tokenRE.MatchString(s) {
		return errors.New("the token must be up to 512 printable characters, without spaces")
	}
	return nil
}

// ValidateBranch checks a branch name by git's rules for a ref
// (git-check-ref-format), narrowed to the characters branch names use.
func ValidateBranch(s string) error {
	bad := !branchRE.MatchString(s) ||
		strings.HasPrefix(s, "-") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") ||
		strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") ||
		strings.Contains(s, "..") || strings.Contains(s, "//") || strings.Contains(s, "/.")
	if bad {
		return fmt.Errorf("%q is not a branch name", s)
	}
	return nil
}

// CheckHost refuses a repository host that resolves to a loopback,
// link-local, private, CGNAT or other non-public address
// (internal/ssrfguard): the server, not the user, connects to it.
func CheckHost(ctx context.Context, hostname string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := ssrfguard.ValidateHost(ctx, hostname, false); err != nil {
		return fmt.Errorf("the server won't connect to %s: it does not resolve to a public address", hostname)
	}
	return nil
}

// Wire types of the agent's git.* commands. The fixtures in
// internal/agent/testdata/git_*.json are decoded by both sides.
type (
	LsRemoteRequest struct {
		OSUser   string `json:"os_user"`
		RepoURL  string `json:"repo_url"`
		Username string `json:"username,omitempty"`
		// Token is never logged or stored in the clear.
		Token string `json:"token,omitempty"`
	}
	LsRemoteResponse struct {
		DefaultBranch string   `json:"default_branch"`
		Branches      []string `json:"branches"`
		Truncated     bool     `json:"truncated,omitempty"`
	}
)

// callTimeout covers the agent's 20 s cap on ls-remote, inside the panel's
// 30 s request timeout.
const callTimeout = 25 * time.Second

// LsRemote asks the agent for a repository's branches, read as osUser.
func LsRemote(ctx context.Context, ag agent.AgentInterface, req LsRemoteRequest) (*LsRemoteResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	raw, err := ag.Call(ctx, "git.ls_remote", req)
	if err != nil {
		return nil, err
	}
	var resp LsRemoteResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("git.ls_remote: decode the answer: %w", err)
	}
	if resp.Branches == nil {
		resp.Branches = []string{}
	}
	return &resp, nil
}

// HasBranch reports whether branch is one of the repository's.
func (r *LsRemoteResponse) HasBranch(branch string) bool {
	for _, b := range r.Branches {
		if b == branch {
			return true
		}
	}
	return false
}
