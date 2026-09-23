package commands

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// EgressUser is one user-policy snapshot consumed by the renderer.
// Username drives the slice path AND the chain/counter names; State
// selects which chain (enforced/learning) is jumped to from the vmap.
// Off-state users get NO entry — the slice falls through to default
// accept outside this table.
//
// AllowedExtra entries are pre-validated by the panel-api layer; we
// emit them verbatim into the chain. The agent does not re-validate
// CIDRs or ports here — defence-in-depth lives in the API handler.
type EgressUser struct {
	Username string
	State    string // "off" | "learning" | "enforced"
	// UID (GH #708) is the OS uid, used for a `meta skuid` egress-enforcement
	// fallback when the user's cgroup slice is missing (so an enforced tenant
	// can't fail open). 0 = unknown (fallback unavailable).
	UID          int
	AllowedExtra []EgressExtra
	// AllowPing (GH #1798) allows outbound ICMP/ICMPv6 echo-request (ping)
	// from this user's chain. Scoped to echo-request only — not the whole
	// ICMP protocol — so error/redirect messages stay blocked.
	AllowPing bool
}

// EgressExtra is one allowed-destination override for a user. Port +
// Protocol are optional. Comment is for the human auditor only and is
// emitted as an `# inline comment` on the rule.
type EgressExtra struct {
	CIDR     string
	Port     *int
	Protocol string // "tcp" or "udp"; "" defaults to "tcp"
	Comment  string
}

// EgressDefaults captures the global default allowlist (M34 Step 6 will
// surface this as server_settings columns; Step 2 hard-codes the
// canonical set in the renderer call site).
type EgressDefaults struct {
	Loopback4 []string // CIDRs
	Loopback6 []string // CIDRs
	PortsTCP  []int
	PortsUDP  []int
}

// CanonicalDefaults returns the safe-by-default allowlist. Removing any
// of these would silently break the typical LAMP workload (memory pin
// in plans/m34: loopback for MariaDB/Redis socket DSN drift, :443 for
// wp-cron/Composer/apt, :587/:465/:25 for mail() submission, and
// :993/:995/:143/:110 for apps that FETCH external mailboxes over IMAP/POP
// (e.g. ITFlow's mail parser — GH #336).
func CanonicalDefaults() EgressDefaults {
	// GH #702: TRUE loopback ONLY. RFC1918 (10/8, 172.16/12, 192.168/16) and
	// IPv6 ULA/link-local (fc00::/7, fe80::/10) were previously accepted here to
	// ALL ports, which let a compromised tenant reach the host LAN + other
	// tenants' docker networks + internal services on any port (lateral
	// movement / SSRF). MariaDB/Redis are unix sockets (not egress-controlled),
	// so loopback covers the LAMP DSN case; web/mail/dns to private hosts still
	// works via the port allowlist below. Private-net access on other ports is
	// now an explicit per-user allowlist entry, not a blanket default.
	return EgressDefaults{
		Loopback4: []string{"127.0.0.0/8"},
		Loopback6: []string{"::1/128"},
		PortsTCP:  []int{53, 80, 443, 587, 465, 25, 993, 995, 143, 110},
		PortsUDP:  []int{53},
	}
}

// RenderEgressNFT produces the deterministic /etc/nftables.d/jabali-per-user-egress.nft
// content from the in-memory snapshot. Output is sorted by username so
// the file is reproducible across reconciler ticks (idempotent file-
// hash check by callers).
//
// Cgroup path matches the M18 slice topology (verified on test VM
// 2026-04-29): jabali.slice/jabali-user.slice/jabali-user-<USERNAME>.slice
// at depth 3 of /sys/fs/cgroup. nftables `socket cgroupv2 level 3` is
// the matcher, with vmap dispatch keyed on the full cgroup path.
//
// Users whose slice does not exist on disk (existsFn returns false) are
// skipped with a comment marker; nft would otherwise reject the file
// at parse time because the kernel verifies cgroupv2 paths exist on
// the host before accepting the element.
func RenderEgressNFT(users []EgressUser, defaults EgressDefaults, existsFn func(slicePath string) bool) string {
	return renderEgress(users, defaults, existsFn, false)
}

// RenderEgressBootNFT produces a variant containing no cgroupv2 path
// references at all, for the boot unit to load before any tenant process has
// started.
//
// The boot unit used to replay the main file, which cannot work on a host that
// has tenants. nftables resolves a cgroupv2 path to an inode when the rule is
// loaded, and at boot jabali.slice/jabali-user.slice does not exist yet — no
// tenant process has run. `nft -f` applies a file as one transaction, so a
// single unresolvable path discards the ENTIRE ruleset, not just that line.
// Verified on a live host: a probe file with one good rule and one bad cgroup
// path left the table non-existent afterwards.
//
// The result was that the unit written to close the no-filter window between
// boot and the first reconciler tick instead guaranteed the window stayed
// open, on every reboot, for every enforced user — the opposite of its stated
// purpose, and silent apart from one failed unit.
//
// Passing an always-false existsFn is what makes this work rather than a
// separate code path: every user then routes through the GH #708 UID fallback
// (`meta skuid`), which is exactly the dispatch that does not need a slice.
//
// Coverage note: the SSRF floor here is emitted per known UID instead of
// scoped to the tenant parent slice, so it covers every user with an egress
// policy row rather than literally every tenant process. That is narrower than
// the steady-state floor and strictly wider than what the boot unit achieved
// before, which was nothing.
func RenderEgressBootNFT(users []EgressUser, defaults EgressDefaults) string {
	return renderEgress(users, defaults, func(string) bool { return false }, true)
}

func renderEgress(users []EgressUser, defaults EgressDefaults, existsFn func(slicePath string) bool, bootSafe bool) string {
	if existsFn == nil {
		existsFn = defaultSliceExists
	}

	sorted := make([]EgressUser, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Username < sorted[j].Username })

	var b strings.Builder
	b.WriteString("# Generated by jabali (panel-agent) — do not edit.\n")
	b.WriteString("# Source of truth: user_egress_policies. Reload via:\n")
	if bootSafe {
		b.WriteString("#   nft -f " + nftEgressBootFilePath + "\n")
		b.WriteString("# Boot-time variant: dispatches by uid, never by cgroup path, because\n")
		b.WriteString("# no tenant slice exists yet at boot and nft rejects the whole file if\n")
		b.WriteString("# it names a cgroup path that is not present. Replaced by the cgroup\n")
		b.WriteString("# ruleset on the first reconciler tick.\n\n")
	} else {
		b.WriteString("#   nft -f " + nftEgressFilePath + "\n\n")
	}

	// Atomic replace: `add` makes the table exist so the `delete` never
	// errors on first load, then `delete` removes the prior definition, all
	// in the SAME `nft -f` transaction. Without this, re-loading the file
	// (every reconcile tick) APPENDS the chain's rules instead of replacing
	// them — chain `output` accumulated a duplicate `vmap` line on every
	// reload (latent since M34), which silently reordered the GH #401 SSRF
	// floor after the per-user dispatch.
	b.WriteString("add table inet jabali_per_user\n")
	b.WriteString("delete table inet jabali_per_user\n\n")

	b.WriteString("table inet jabali_per_user {\n")

	// Default sets — referenced from every per-user chain.
	if len(defaults.Loopback4) > 0 {
		b.WriteString("  set default_loopback4 {\n")
		b.WriteString("    type ipv4_addr; flags interval;\n")
		fmt.Fprintf(&b, "    elements = { %s }\n", strings.Join(defaults.Loopback4, ", "))
		b.WriteString("  }\n")
	}
	if len(defaults.Loopback6) > 0 {
		b.WriteString("  set default_loopback6 {\n")
		b.WriteString("    type ipv6_addr; flags interval;\n")
		fmt.Fprintf(&b, "    elements = { %s }\n", strings.Join(defaults.Loopback6, ", "))
		b.WriteString("  }\n")
	}
	if len(defaults.PortsTCP) > 0 {
		b.WriteString("  set default_ports_tcp {\n")
		b.WriteString("    type inet_service;\n")
		fmt.Fprintf(&b, "    elements = { %s }\n", joinInts(defaults.PortsTCP, ", "))
		b.WriteString("  }\n")
	}
	if len(defaults.PortsUDP) > 0 {
		b.WriteString("  set default_ports_udp {\n")
		b.WriteString("    type inet_service;\n")
		fmt.Fprintf(&b, "    elements = { %s }\n", joinInts(defaults.PortsUDP, ", "))
		b.WriteString("  }\n")
	}

	// In bootSafe mode the floor is scoped by UID rather than by the tenant
	// parent slice, so collect the UIDs up front — the counter declaration
	// below has to agree with what the output chain will reference.
	var floorUIDs []int
	if bootSafe {
		for _, u := range sorted {
			if u.State != "off" && u.UID > 0 {
				floorUIDs = append(floorUIDs, u.UID)
			}
		}
	}

	// Always-on SSRF floor counter (GH #401). Declared only when the floor
	// rules themselves will be emitted, so the counter is never referenced
	// without being declared (and never declared without being referenced,
	// which would show up as a permanently-zero counter).
	if (bootSafe && len(floorUIDs) > 0) || (!bootSafe && existsFn(tenantParentSlice)) {
		b.WriteString("\n  counter ssrf_floor_drops {}\n")
	}

	// Per-user counters + chains. Skip off-state and missing-slice users.
	type rendered struct {
		username string
		state    string
	}
	var emitted []rendered
	type uidRendered struct {
		username, state string
		uid             int
	}
	var uidFallback []uidRendered

	for _, u := range sorted {
		if u.State == "off" {
			fmt.Fprintf(&b, "\n  # %s: state=off — skipped (falls through to default accept)\n", u.Username)
			continue
		}
		slicePath := SlicePathFor(u.Username)
		sliceMissing := !existsFn(slicePath)
		if sliceMissing && u.UID <= 0 {
			// Can't cgroup-match (no slice) AND can't UID-fallback (no uid).
			// Surfaced by the apply handler's users_fail_open; skip here.
			fmt.Fprintf(&b, "\n  # %s: slice %s missing + no uid — skipped\n", u.Username, slicePath)
			continue
		}
		fmt.Fprintf(&b, "\n  counter user_%s_drops {}\n", u.Username)
		writeUserChain(&b, u, defaults)
		if sliceMissing {
			// GH #708: dispatch this enforced user by UID so a missing cgroup
			// slice no longer means their traffic falls through to accept. The
			// chain is identical; only the dispatch differs (skuid vs cgroup).
			fmt.Fprintf(&b, "  # %s: slice missing — enforcing by uid %d instead of cgroup (GH #708)\n", u.Username, u.UID)
			uidFallback = append(uidFallback, uidRendered{u.Username, u.State, u.UID})
		} else {
			emitted = append(emitted, rendered{u.Username, u.State})
		}
	}

	// vmap dispatch by cgroup path (level 3). Empty if no users emitted —
	// then the chain still exists but matches nothing, falling through
	// to the policy accept.
	b.WriteString("\n  map cgroup_to_chain {\n")
	b.WriteString("    type cgroupsv2 : verdict\n")
	if len(emitted) > 0 {
		b.WriteString("    elements = {\n")
		for i, r := range emitted {
			sep := ","
			if i == len(emitted)-1 {
				sep = ""
			}
			fmt.Fprintf(&b, "      \"%s\" : jump user_%s_%s%s\n",
				SlicePathFor(r.username), r.username, r.state, sep)
		}
		b.WriteString("    }\n")
	}
	b.WriteString("  }\n")

	b.WriteString("\n  chain output {\n")
	b.WriteString("    type filter hook output priority 0; policy accept;\n")
	// GH #401 SSRF floor: no tenant process may reach link-local /
	// cloud-metadata (169.254.0.0/16 incl. 169.254.169.254, fe80::/10),
	// regardless of per-user egress enrollment. Runs BEFORE the per-user
	// vmap so it is absolute. Emitted only when the tenant parent slice
	// exists on the host (nft verifies cgroupv2 paths at load time).
	if bootSafe {
		// Same floor, dispatched by UID because no cgroup slice exists yet.
		// Still ahead of the per-user dispatch, so it stays absolute.
		for _, uid := range floorUIDs {
			fmt.Fprintf(&b, "    meta skuid %d ip daddr 169.254.0.0/16 counter name ssrf_floor_drops drop\n", uid)
			fmt.Fprintf(&b, "    meta skuid %d ip6 daddr fe80::/10 counter name ssrf_floor_drops drop\n", uid)
		}
	} else if existsFn(tenantParentSlice) {
		fmt.Fprintf(&b, "    socket cgroupv2 level 2 \"%s\" ip daddr 169.254.0.0/16 counter name ssrf_floor_drops drop\n", tenantParentSlice)
		fmt.Fprintf(&b, "    socket cgroupv2 level 2 \"%s\" ip6 daddr fe80::/10 counter name ssrf_floor_drops drop\n", tenantParentSlice)
	}
	b.WriteString("    socket cgroupv2 level 3 vmap @cgroup_to_chain\n")
	// GH #708: UID-based fallback for enforced users whose cgroup slice is
	// missing — keeps the fail-closed invariant (their traffic is still forced
	// through the per-user chain instead of the trailing policy accept).
	for _, uf := range uidFallback {
		fmt.Fprintf(&b, "    meta skuid %d jump user_%s_%s\n", uf.uid, uf.username, uf.state)
	}
	b.WriteString("  }\n")
	b.WriteString("}\n")

	return b.String()
}

// SlicePathFor returns the full cgroup path under /sys/fs/cgroup that
// nftables type=cgroupsv2 expects for one user. Mirrors the M18 slice
// hierarchy exactly.
// tenantParentSlice is the cgroupv2 path (level 2) that parents every
// per-user slice. Matching here scopes a rule to ANY tenant process,
// enrolled in a per-user egress policy or not — used for the GH #401
// always-on cloud-metadata / link-local SSRF floor.
const tenantParentSlice = "jabali.slice/jabali-user.slice"

func SlicePathFor(username string) string {
	return fmt.Sprintf("jabali.slice/jabali-user.slice/jabali-user-%s.slice", username)
}

func writeUserChain(b *strings.Builder, u EgressUser, d EgressDefaults) {
	chainName := fmt.Sprintf("user_%s_%s", u.Username, u.State)
	fmt.Fprintf(b, "\n  chain %s {\n", chainName)

	if len(d.Loopback4) > 0 {
		b.WriteString("    ip daddr @default_loopback4 accept\n")
	}
	if len(d.Loopback6) > 0 {
		b.WriteString("    ip6 daddr @default_loopback6 accept\n")
	}
	if len(d.PortsTCP) > 0 {
		b.WriteString("    tcp dport @default_ports_tcp accept\n")
	}
	if len(d.PortsUDP) > 0 {
		b.WriteString("    udp dport @default_ports_udp accept\n")
	}

	for _, ex := range u.AllowedExtra {
		writeExtra(b, ex)
	}

	// GH #1798: per-package ICMP allowance — echo-request (ping) only, v4 + v6.
	if u.AllowPing {
		b.WriteString("    icmp type echo-request accept\n")
		b.WriteString("    icmpv6 type echo-request accept\n")
	}

	switch u.State {
	case "enforced":
		// GH #638: log the blocked flow (rate-limited, same shape as learn mode)
		// BEFORE dropping, so an operator sees WHICH daddr:dport a tenant app is
		// blocked from — `journalctl -k | grep jabali-egress-drop-<user>` — instead
		// of a silent SYN-drop that surfaces only as a generic 10s connect timeout
		// in the app's DB driver. Counter still bumps every drop (M14 burst rate).
		fmt.Fprintf(b, "    limit rate 5/minute log prefix \"jabali-egress-drop-%s \" group 0\n", u.Username)
		fmt.Fprintf(b, "    counter name user_%s_drops drop\n", u.Username)
	case "learning":
		// Rate-limit the dmesg log so a runaway loop doesn't drown the
		// kernel ring buffer. Counter still bumps every drop so the
		// M14 burst source sees the true rate.
		fmt.Fprintf(b, "    limit rate 5/minute log prefix \"jabali-egress-learn-%s \" group 0\n", u.Username)
		fmt.Fprintf(b, "    counter name user_%s_drops accept\n", u.Username)
	}

	b.WriteString("  }\n")
}

func writeExtra(b *strings.Builder, ex EgressExtra) {
	proto := ex.Protocol
	if proto == "" {
		proto = "tcp"
	}
	family := "ip"
	if strings.Contains(ex.CIDR, ":") {
		family = "ip6"
	}
	var rule string
	if ex.Port != nil {
		rule = fmt.Sprintf("    %s daddr %s %s dport %d accept", family, ex.CIDR, proto, *ex.Port)
	} else {
		rule = fmt.Sprintf("    %s daddr %s accept", family, ex.CIDR)
	}
	if c := sanitiseEgressComment(ex.Comment); c != "" {
		rule += fmt.Sprintf(" comment \"%s\"", c)
	}
	b.WriteString(rule + "\n")
}

// sanitiseEgressComment makes a panel-supplied comment safe to embed in the
// nftables file this package renders. panel-api rejects newlines and control
// characters at its own boundary, but the agent writes a file that `nft -f`
// loads as root, so it does not take that on trust (the same mirror-the-
// validation rule CONVENTIONS.md sets out for M14).
//
// A newline here would inject arbitrary nftables lines; any unparseable byte
// fails the whole ruleset to load, which silently drops per-user egress
// enforcement box-wide. Quotes become apostrophes (they would close the comment
// string), newlines and control characters become spaces, and the result is
// capped at the same 200 chars the API enforces.
func sanitiseEgressComment(in string) string {
	if in == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range in {
		switch {
		case r == '"':
			b.WriteRune('\'')
		case r == '\\':
			b.WriteRune('/')
		case r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

func joinInts(in []int, sep string) string {
	parts := make([]string, len(in))
	for i, v := range in {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, sep)
}
