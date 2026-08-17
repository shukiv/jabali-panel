// system_software.go — agent handler for system.software.
//
// Returns versions of installed software stack relevant to operators
// of a Jabali host. Probes a hardcoded set of binaries via short shell-
// outs and parses the first version-shaped token from each.
//
// Why a separate command (not folded into system.info): system.info is
// on the server-status fan-out polling loop (5s default). Adding ~10
// exec.Command shell-outs would make every poll wait on the slowest
// binary. system.software gets its own slot in the envelope with a
// 5-minute in-memory cache — versions don't change between updates,
// so re-probing every 5s is pure waste.
//
// Probes are best-effort: a missing binary collapses to a "not
// installed" row rather than failing the whole command. exec timeout
// of 3s per binary protects the dispatcher from a hung probe.

package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SystemSoftwareItem is one row in the response.
type SystemSoftwareItem struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SystemSoftwareResponse wraps the items list.
type SystemSoftwareResponse struct {
	Items []SystemSoftwareItem `json:"items"`
}

// softwareProbe describes one binary + how to invoke it. parser
// extracts the version token from the combined stdout+stderr.
type softwareProbe struct {
	name string
	bin  string
	args []string
}

// All probes write their version banner to either stdout or stderr (or
// both); we capture both via CombinedOutput. Order here drives row
// order in the UI table.
var softwareProbes = []softwareProbe{
	{name: "nginx", bin: "nginx", args: []string{"-v"}},
	{name: "MariaDB", bin: "mariadb", args: []string{"-V"}},
	{name: "PHP", bin: "php", args: []string{"-v"}},
	{name: "Redis", bin: "redis-server", args: []string{"-v"}},
	{name: "Stalwart", bin: "stalwart", args: []string{"--version"}},
	{name: "Kratos", bin: "kratos", args: []string{"version"}},
	{name: "PowerDNS", bin: "pdns_server", args: []string{"--version"}},
	{name: "PowerDNS Recursor", bin: "pdns_recursor", args: []string{"--version"}},
	{name: "OpenSSH", bin: "sshd", args: []string{"-V"}},
	{name: "Bulwark (node)", bin: "node", args: []string{"--version"}},
}

// versionTokenRe matches the first dotted-numeric token in a version
// banner — handles "nginx version: nginx/1.24.0" → "1.24.0",
// "PHP 8.4.16 (cli)" → "8.4.16", "v22.11.0" → "22.11.0", etc.
var versionTokenRe = regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?(?:[-+][\w.]+)?)`)

// softwareCache holds the last successful response. The whole list is
// cached as a unit because most callers (server-status fan-out) want
// every row in one shot. TTL is 5 minutes — stale enough to avoid
// hammering 10 binaries per poll, fresh enough that a post-`apt
// upgrade` re-probe is the next poll cycle at worst.
const softwareCacheTTL = 5 * time.Minute

var (
	softwareCacheMu      sync.Mutex
	softwareCacheValue   SystemSoftwareResponse
	softwareCacheExpires time.Time
)

func systemSoftwareHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	softwareCacheMu.Lock()
	if time.Now().Before(softwareCacheExpires) {
		v := softwareCacheValue
		softwareCacheMu.Unlock()
		return v, nil
	}
	softwareCacheMu.Unlock()

	items := make([]SystemSoftwareItem, 0, len(softwareProbes))
	for _, p := range softwareProbes {
		items = append(items, SystemSoftwareItem{
			Name:    p.name,
			Version: probeSoftware(ctx, p),
		})
	}

	resp := SystemSoftwareResponse{Items: items}

	softwareCacheMu.Lock()
	softwareCacheValue = resp
	softwareCacheExpires = time.Now().Add(softwareCacheTTL)
	softwareCacheMu.Unlock()

	return resp, nil
}

// probeSoftware runs one binary's version probe with a 3s timeout and
// extracts the first version-shaped token. Empty string means "not
// installed or no recognizable version" — UI renders this as a muted
// "—".
func probeSoftware(ctx context.Context, p softwareProbe) string {
	if _, err := exec.LookPath(p.bin); err != nil {
		return ""
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, _ := execCommandContext(probeCtx, p.bin, p.args...).CombinedOutput()
	return parseVersion(string(out))
}

// parseVersion returns the first dotted-numeric token in s, trimmed.
// Exported behavior is "best effort" — exact format is not load-bearing.
func parseVersion(s string) string {
	m := versionTokenRe.FindString(s)
	return strings.TrimSpace(m)
}

// resetSoftwareCacheForTest is a test-only hook to clear the cache.
func resetSoftwareCacheForTest() {
	softwareCacheMu.Lock()
	softwareCacheExpires = time.Time{}
	softwareCacheValue = SystemSoftwareResponse{}
	softwareCacheMu.Unlock()
}

func init() {
	Default.Register("system.software", systemSoftwareHandler)
}
