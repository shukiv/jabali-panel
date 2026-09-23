package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	osuser "os/user"
	"path/filepath"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nftEgressFilePath is where the reconciler renders the per-user egress
// table. nftables loads /etc/nftables.d/*.nft via the host's main
// /etc/nftables.conf include (set up by install_per_user_egress in
// install.sh). Reload uses `nft -f <file>` only — never `systemctl
// reload nftables` which would flush other tables we do not own
// (CrowdSec blocklists, UFW chain).
const nftEgressFilePath = "/etc/nftables.d/jabali-per-user-egress.nft"

// nftEgressBootFilePath holds the uid-dispatched variant that
// jabali-per-user-egress-load.service loads at boot. Separate file rather than
// a flag on the main one because the boot unit runs long before panel-api and
// has to be able to load something valid with zero knowledge of host state.
const nftEgressBootFilePath = "/etc/nftables.d/jabali-per-user-egress-boot.nft"
const nftEgressTableName = "jabali_per_user"
const cgroupRoot = "/sys/fs/cgroup"

// userEgressApplyParams is the agent-call payload carrying the full
// snapshot of every user's policy. The reconciler sends the entire
// list every tick (cheap; at 1000 users the file is < 200 KB).
type userEgressApplyParams struct {
	Users    []userEgressApplyUser    `json:"users"`
	Defaults *userEgressApplyDefaults `json:"defaults,omitempty"`
}

type userEgressApplyUser struct {
	Username     string                 `json:"username"`
	State        string                 `json:"state"`
	AllowedExtra []userEgressApplyExtra `json:"allowed_extra"`
	// AllowPing (GH #1798) permits ICMP/ICMPv6 echo-request (ping) out for this
	// user's enforced/learning chain. Absent/false = ping stays blocked.
	AllowPing bool `json:"allow_ping,omitempty"`
}

type userEgressApplyExtra struct {
	CIDR     string `json:"cidr"`
	Port     *int   `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

type userEgressApplyDefaults struct {
	Loopback4 []string `json:"loopback4,omitempty"`
	Loopback6 []string `json:"loopback6,omitempty"`
	PortsTCP  []int    `json:"ports_tcp,omitempty"`
	PortsUDP  []int    `json:"ports_udp,omitempty"`
}

type userEgressApplyResponse struct {
	Applied      bool `json:"applied"`
	UsersEmitted int  `json:"users_emitted"`
	UsersSkipped int  `json:"users_skipped"`
	// GH #708: enforced users whose slice was missing -> egress NOT enforced.
	UsersFailOpen []string `json:"users_fail_open,omitempty"`
	TableVersion  string   `json:"table_version"`
	NoChange      bool     `json:"no_change,omitempty"`
	BytesWritten  int      `json:"bytes_written,omitempty"`
}

func userEgressApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userEgressApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	for _, u := range p.Users {
		if !userSliceUsernameRegex.MatchString(u.Username) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("invalid username %q", u.Username),
			}
		}
		switch u.State {
		case "off", "learning", "enforced":
		default:
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("invalid state %q for %s", u.State, u.Username),
			}
		}
	}

	// GH #681: validate operator-supplied defaults BEFORE rendering them into
	// nftables sets — a malformed CIDR or out-of-range port would break the
	// ruleset (fail-open egress) or inject unexpected set elements.
	if p.Defaults != nil {
		for _, c := range append(append([]string{}, p.Defaults.Loopback4...), p.Defaults.Loopback6...) {
			if _, _, err := net.ParseCIDR(strings.TrimSpace(c)); err != nil {
				return nil, csInvalidArg(fmt.Sprintf("invalid default CIDR %q", c))
			}
		}
		for _, port := range append(append([]int{}, p.Defaults.PortsTCP...), p.Defaults.PortsUDP...) {
			if port < 1 || port > 65535 {
				return nil, csInvalidArg(fmt.Sprintf("invalid default port %d", port))
			}
		}
	}

	// Translate to renderer types.
	defs := CanonicalDefaults()
	if p.Defaults != nil {
		if len(p.Defaults.Loopback4) > 0 {
			defs.Loopback4 = p.Defaults.Loopback4
		}
		if len(p.Defaults.Loopback6) > 0 {
			defs.Loopback6 = p.Defaults.Loopback6
		}
		if len(p.Defaults.PortsTCP) > 0 {
			defs.PortsTCP = p.Defaults.PortsTCP
		}
		if len(p.Defaults.PortsUDP) > 0 {
			defs.PortsUDP = p.Defaults.PortsUDP
		}
	}
	users := make([]EgressUser, 0, len(p.Users))
	for _, u := range p.Users {
		extras := make([]EgressExtra, 0, len(u.AllowedExtra))
		for _, e := range u.AllowedExtra {
			extras = append(extras, EgressExtra{
				CIDR: e.CIDR, Port: e.Port,
				Protocol: e.Protocol, Comment: e.Comment,
			})
		}
		// GH #708: resolve the uid for the missing-slice UID-enforcement fallback.
		uid := 0
		if ou, lerr := osuser.Lookup(u.Username); lerr == nil {
			if n, aerr := strconv.Atoi(ou.Uid); aerr == nil {
				uid = n
			}
		}
		users = append(users, EgressUser{
			Username: u.Username, State: u.State, UID: uid, AllowedExtra: extras,
			AllowPing: u.AllowPing,
		})
	}

	// Count emitted/skipped before render — same predicate as renderer.
	usersEmitted := 0
	usersSkipped := 0
	// GH #708: an ENFORCED/learning user whose slice is missing gets skipped by
	// the renderer and then falls through to the output chain's `policy accept`
	// — i.e. their egress is NOT enforced (fail-open). We cannot emit an nft
	// cgroup match for a non-existent slice, so we cannot fail-closed here; but
	// we MUST NOT let it be silent. Collect them, report them in the response,
	// and warn so the panel/operator re-creates the slice (or the reconciler
	// retries) instead of the tenant silently escaping egress control.
	var failOpen []string
	for _, u := range users {
		if u.State == "off" {
			usersSkipped++
			continue
		}
		if !defaultSliceExists(SlicePathFor(u.Username)) {
			// GH #708: missing slice — enforced by UID fallback if the uid
			// resolved; only truly fail-open (skipped) when we have no uid either.
			if u.UID <= 0 {
				usersSkipped++
				failOpen = append(failOpen, u.Username)
			} else {
				usersEmitted++
			}
			continue
		}
		usersEmitted++
	}
	if len(failOpen) > 0 {
		slog.WarnContext(ctx, "egress fail-open: enforced users skipped, slice missing on host",
			"users", failOpen, "detail", "no cgroup slice AND no resolvable uid — NOT egress-controlled until the slice exists")
	}

	content := RenderEgressNFT(users, defs, defaultSliceExists)
	sum := sha256.Sum256([]byte(content))
	version := hex.EncodeToString(sum[:])[:16]

	// Boot-time copy: same policy, dispatched by uid so it holds no cgroupv2
	// paths and can load before any tenant process exists. Written but
	// deliberately NOT applied — the cgroup ruleset is the better one and is
	// what should be live right now.
	//
	// This sits ABOVE the no-change return on purpose. Keying it off whether
	// the MAIN file changed would mean a converged host never gets one: the
	// first tick after an upgrade finds the main file already current, returns
	// early, and the boot file stays missing forever — which is precisely the
	// state this is meant to fix.
	//
	// A failure here is logged, not fatal. The live ruleset is what protects
	// the host today; the boot file only matters at the next reboot.
	if bootContent := RenderEgressBootNFT(users, defs); bootContent != "" {
		if existingBoot, _ := os.ReadFile(nftEgressBootFilePath); string(existingBoot) != bootContent {
			if err := os.MkdirAll(filepath.Dir(nftEgressBootFilePath), 0755); err != nil {
				slog.WarnContext(ctx, "egress: could not create boot ruleset dir",
					"path", nftEgressBootFilePath, "error", err)
			} else if err := writeFileAtomically(nftEgressBootFilePath, []byte(bootContent), 0644); err != nil {
				slog.WarnContext(ctx, "egress: could not write boot ruleset",
					"path", nftEgressBootFilePath, "error", err,
					"detail", "per-user egress will not be enforced between boot and the first reconciler tick")
			}
		}
	}

	// Idempotent write.
	existing, _ := os.ReadFile(nftEgressFilePath)
	resp := &userEgressApplyResponse{
		UsersEmitted:  usersEmitted,
		UsersSkipped:  usersSkipped,
		UsersFailOpen: failOpen,
		TableVersion:  version,
		BytesWritten:  len(content),
	}
	if string(existing) == content {
		resp.Applied = true
		resp.NoChange = true
		return resp, nil
	}

	if err := os.MkdirAll(filepath.Dir(nftEgressFilePath), 0755); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir: %v", err),
		}
	}
	if err := writeFileAtomically(nftEgressFilePath, []byte(content), 0644); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write nft file: %v", err),
		}
	}

	testMutex.Lock()
	runCmdFn := runCmd
	testMutex.Unlock()
	if _, stderr, err := runCmdFn(ctx, "nft", "-f", nftEgressFilePath); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("nft -f failed: %v (%s)", err, strings.TrimSpace(string(stderr))),
		}
	}
	resp.Applied = true
	return resp, nil
}

// userEgressReadCountersParams is empty — the agent always returns
// every counter. Reconciler diffs against its in-memory previous tick.
type userEgressReadCountersParams struct {
	Reset bool `json:"reset"`
}

type userEgressCounter struct {
	Username string `json:"username"`
	Packets  uint64 `json:"packets"`
	Bytes    uint64 `json:"bytes"`
}

type userEgressReadCountersResponse struct {
	Counters []userEgressCounter `json:"counters"`
}

func userEgressReadCountersHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userEgressReadCountersParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("parse params: %v", err),
			}
		}
	}

	testMutex.Lock()
	runCmdFn := runCmd
	testMutex.Unlock()

	args := []string{"-j", "list", "counters", "table", "inet", nftEgressTableName}
	if p.Reset {
		args = []string{"-j", "reset", "counters", "table", "inet", nftEgressTableName}
	}
	stdout, stderr, err := runCmdFn(ctx, "nft", args...)
	if err != nil {
		// Table may not exist yet (first reconciler tick before apply).
		// Treat as "no counters" rather than an error so the reconciler
		// can proceed.
		if strings.Contains(string(stderr), "No such file") ||
			strings.Contains(string(stderr), "does not exist") {
			return &userEgressReadCountersResponse{Counters: []userEgressCounter{}}, nil
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("nft list/reset: %v (%s)", err, strings.TrimSpace(string(stderr))),
		}
	}

	counters, perr := parseNFTCounters(stdout)
	if perr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("parse counters: %v", perr),
		}
	}
	return &userEgressReadCountersResponse{Counters: counters}, nil
}

// parseNFTCounters walks the nftables JSON output and extracts every
// counter named user_<USERNAME>_drops, returning one row per match.
// Counters not following this naming scheme are ignored — keeps us
// resilient to unrelated counters added by other tables in the future.
func parseNFTCounters(raw []byte) ([]userEgressCounter, error) {
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	type counterObj struct {
		Family  string `json:"family"`
		Table   string `json:"table"`
		Name    string `json:"name"`
		Packets uint64 `json:"packets"`
		Bytes   uint64 `json:"bytes"`
	}
	out := []userEgressCounter{}
	for _, item := range doc.Nftables {
		raw, ok := item["counter"]
		if !ok {
			continue
		}
		var c counterObj
		if err := json.Unmarshal(raw, &c); err != nil {
			continue
		}
		if c.Table != nftEgressTableName {
			continue
		}
		username, ok := extractUsernameFromCounter(c.Name)
		if !ok {
			continue
		}
		out = append(out, userEgressCounter{
			Username: username,
			Packets:  c.Packets,
			Bytes:    c.Bytes,
		})
	}
	return out, nil
}

// extractUsernameFromCounter returns the username embedded in a name of
// the form "user_<USERNAME>_drops". Returns ok=false on any other shape.
func extractUsernameFromCounter(name string) (string, bool) {
	const prefix = "user_"
	const suffix = "_drops"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	mid := name[len(prefix) : len(name)-len(suffix)]
	if mid == "" {
		return "", false
	}
	return mid, true
}

// defaultSliceExists checks whether the M18 per-user slice has been
// activated on this host. nftables rejects vmap elements whose
// cgroupv2 path doesn't exist, so we filter at render time.
func defaultSliceExists(slicePath string) bool {
	full := filepath.Join(cgroupRoot, slicePath)
	st, err := os.Stat(full)
	if err != nil {
		return false
	}
	return st.IsDir()
}

// portStrings is a tiny helper for tests + manual inspection — render
// a CSV of ports for human-readable diffs.
func portStrings(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ",")
}

func init() {
	Default.Register("user.egress.apply", userEgressApplyHandler)
	Default.Register("user.egress.read_counters", userEgressReadCountersHandler)
}
