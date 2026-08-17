package commands

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// sessions_ssh.go — GH #338 (SSH channel). Enumerate live SSH sessions (peer IP
// + the sshd process) for the admin Active Sessions view, and revoke one by
// terminating its sshd process. SECURITY: revoke ONLY ever signals a process
// whose comm is sshd/sshd-session — never an arbitrary PID.

var sshPeerRe = regexp.MustCompile(`([0-9a-fA-F:.]+):\d+\s+users:\(\("(sshd[a-z-]*)",pid=(\d+)`)

type sshSession struct {
	ID       string `json:"id"` // "ssh:<pid>"
	User     string `json:"user"`
	RemoteIP string `json:"remote_ip"`
	Since    string `json:"since"`
	Channel  string `json:"channel"` // "ssh" (interactive pts) or "sftp" (notty) — GH #338
	PID      int    `json:"pid"`
}

// sessionsSSHListHandler lists established inbound SSH connections.
func sessionsSSHListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	// `ss` established inbound connections with the owning process. GH #338:
	// do NOT filter on `sport = :22` — operators who move sshd to a non-standard
	// port (a common hardening step) would then see zero SSH sessions. Every
	// established connection whose owning process is sshd/sshd-session is an
	// inbound session (sshd never dials out; outbound ssh is the `ssh` client),
	// so the sshd-process match below scopes it correctly on any port, and SFTP
	// (same sshd transport) is captured too.
	out, err := execCommandContext(ctx, "ss", "-tnHp", "state", "established").Output()
	if err != nil {
		// No ss / no sessions — return empty, not an error.
		return map[string]any{"sessions": []sshSession{}}, nil
	}
	seen := map[int]bool{}
	sessions := []sshSession{}
	for _, line := range strings.Split(string(out), "\n") {
		m := sshPeerRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, perr := strconv.Atoi(m[3])
		if perr != nil || seen[pid] || !isSSHDProcess(pid) {
			continue
		}
		seen[pid] = true
		// Real logged-in user + channel come from the sshd process's cmdline
		// (`sshd: <user>@<pts/N|notty>`), NOT `ps -o user=` — that reports the
		// root privsep-monitor owner, so every session wrongly showed as `root`
		// and SFTP was indistinguishable from SSH (GH #338). Skip pre-auth /
		// listener sshd (no `user@…` yet).
		user, channel := sshSessionIdentity(pid)
		if user == "" {
			continue
		}
		_, since := procUserAndStart(ctx, pid)
		sessions = append(sessions, sshSession{
			ID: "ssh:" + m[3], User: user, RemoteIP: m[1], Since: since, Channel: channel, PID: pid,
		})
	}
	return map[string]any{"sessions": sessions}, nil
}

type sessionsSSHRevokeParams struct {
	PID int `json:"pid"`
}

// sessionsSSHRevokeHandler terminates one SSH session's sshd process. Refuses
// to signal any PID whose comm is not an sshd process.
func sessionsSSHRevokeHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p sessionsSSHRevokeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, csInvalidArg("bad params")
	}
	if p.PID < 2 {
		return nil, csInvalidArg("invalid pid")
	}
	if !isSSHDProcess(p.PID) {
		// Never kill a non-sshd PID (the PID may have been recycled).
		return nil, csInvalidArg("pid is not an sshd session")
	}
	if err := syscall.Kill(p.PID, syscall.SIGHUP); err != nil {
		return nil, csInternal("terminate ssh session", err)
	}
	return map[string]any{"ok": true}, nil
}

// isSSHDProcess reports whether pid's comm is an sshd process (sshd, sshd-session).
func isSSHDProcess(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(b))
	return comm == "sshd" || comm == "sshd-session" || strings.HasPrefix(comm, "sshd")
}

// sshSessionIdentity reads /proc/<pid>/cmdline for the post-auth sshd label
// `sshd: <user>@<pts/N|notty>` and returns the REAL logged-in user (not the
// root privsep-monitor that owns the process) plus the channel: an interactive
// terminal (pts/N) is "ssh", a no-tty session (notty — SFTP or a remote exec)
// is "sftp". Returns ("","") for a pre-auth/listener sshd whose label is a
// bracketed state like `[accepted]`/`[priv]` (no real user yet) so the caller
// skips it. GH #338.
func sshSessionIdentity(pid int) (user, channel string) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return "", ""
	}
	// cmdline args are NUL-separated; the sshd label is one arg with spaces.
	return parseSSHDLabel(strings.ReplaceAll(string(b), "\x00", " "))
}

// parseSSHDLabel extracts (user, channel) from an sshd process label like
// `sshd: alice@pts/0` (ssh) or `sshd-session: bob@notty` (sftp). Pure so it's
// unit-testable without a live /proc. Returns ("","") for pre-auth/listener
// labels (`[accepted]`, `[priv]`, no `user@`).
func parseSSHDLabel(cmdline string) (user, channel string) {
	s := strings.TrimSpace(cmdline)
	i := strings.Index(s, ": ") // after "sshd" / "sshd-session"
	if i < 0 {
		return "", ""
	}
	label := strings.TrimSpace(s[i+2:]) // e.g. "alice@pts/0" or "bob@notty"
	at := strings.LastIndex(label, "@")
	if at <= 0 {
		return "", "" // pre-auth: "[accepted]", "[net]", "[priv]" — no user@
	}
	user = strings.TrimSpace(label[:at])
	if user == "" || strings.HasPrefix(user, "[") {
		return "", ""
	}
	tty := strings.TrimSpace(label[at+1:])
	channel = "ssh"
	if strings.HasPrefix(tty, "notty") || strings.Contains(tty, "sftp") {
		channel = "sftp"
	}
	return user, channel
}

// procUserAndStart returns the process owner + its start time (best-effort).
func procUserAndStart(ctx context.Context, pid int) (user, since string) {
	if out, err := execCommandContext(ctx, "ps", "-o", "user=", "-p", strconv.Itoa(pid)).Output(); err == nil {
		user = strings.TrimSpace(string(out))
	}
	if out, err := execCommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output(); err == nil {
		since = strings.TrimSpace(string(out))
	}
	return user, since
}

func init() {
	Default.Register("sessions.ssh.list", sessionsSSHListHandler)
	Default.Register("sessions.ssh.revoke", sessionsSSHRevokeHandler)
}
