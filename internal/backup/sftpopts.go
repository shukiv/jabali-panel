// Package backup — SFTP option helpers for M30.1 destinations.
//
// restic's native SFTP backend speaks `sftp:user@host:/path` and uses
// the system ssh client. To pin a non-default key or fall through to
// password auth (sshpass) we override the default ssh invocation via
// `-o sftp.command="..."`. This file builds that flag from the typed
// SFTPOptions blob persisted by panel-api.
package backup

import (
	"fmt"
	"strings"
)

// SFTPInputs is the wire-shape view of models.SFTPOptions, lifted into
// internal/backup so the wrapper doesn't depend on panel-api.
type SFTPInputs struct {
	Host    string
	User    string
	Port    int
	Path    string
	Auth    string // "key" | "password" — empty = default ssh config
	KeyPath string
}

// ComposeSFTPURL returns the canonical restic URL form. Empty Path is
// allowed (restic uses the home directory of the user); the panel UI
// always fills it.
//
// The port is NOT encoded in the URL — restic's SFTP URL parser does
// NOT support a `:port:` segment in modern versions. Non-22 ports are
// handled exclusively via `-o sftp.command="ssh -p N ..."`.
func ComposeSFTPURL(in SFTPInputs) string {
	if in.User == "" || in.Host == "" {
		return ""
	}
	if in.Path == "" {
		return fmt.Sprintf("sftp:%s@%s:", in.User, in.Host)
	}
	return fmt.Sprintf("sftp:%s@%s:%s", in.User, in.Host, in.Path)
}

// sshTimeoutOpts bound how long ssh waits on an SFTP destination (GH #2245).
// Without them a host that is down held a backup until the kernel gave up on
// the TCP connect (about 2 minutes), and a connection that died mid-run left
// restic waiting on a dead socket. ServerAlive ends the session after 4
// unanswered probes 15s apart, so a dead connection is noticed within a
// minute. A space-separated value with no commas: restic splits sftp.command
// with go-shellwords.
var sshTimeoutOpts = []string{
	"-o", "ConnectTimeout=30",
	"-o", "ServerAliveInterval=15",
	"-o", "ServerAliveCountMax=4",
}

// SFTPCommandFlag returns the `-o sftp.command=...` option for the
// given inputs, or an empty string when there is no host to connect to.
//
// With default ssh config, the default key and port 22, the command is
// restic's own default (ssh to user@host, sftp subsystem) plus
// sshTimeoutOpts: the host-key policy and auth methods stay whatever the
// system ssh config says.
//
// Otherwise `accept-new` host-key policy keeps the first connection
// working without operator intervention; subsequent connections require
// the known_hosts entry to match. This matches the operator-friendly
// stance restic itself recommends.
func SFTPCommandFlag(in SFTPInputs) string {
	if in.Host == "" {
		return ""
	}
	needsOverride := in.Auth == "password" ||
		(in.Auth == "key" && in.KeyPath != "") ||
		(in.Port > 0 && in.Port != 22)
	if !needsOverride {
		parts := append([]string{"ssh"}, sshTimeoutOpts...)
		return "sftp.command=" + strings.Join(append(parts, sshDestination(in)...), " ")
	}
	parts := []string{}
	if in.Auth == "password" {
		// sshpass reads SSHPASS from the env (loaded from the creds
		// env file by the wrapper).
		parts = append(parts, "sshpass", "-e")
	}
	parts = append(parts, "ssh")
	if in.Auth == "key" && in.KeyPath != "" {
		parts = append(parts, "-i", shellQuote(in.KeyPath), "-o", "IdentitiesOnly=yes")
	}
	parts = append(parts, "-o", "StrictHostKeyChecking=accept-new")
	parts = append(parts, sshTimeoutOpts...)
	// BatchMode=yes disables password+keyboard-interactive prompts. Safe
	// for key auth (no prompt needed); fatal for password auth — sshpass
	// drives the interactive prompt, so BatchMode would lock it out and
	// ssh would only try pubkey methods, ending in "Permission denied
	// (publickey,password)" even with the right password.
	if in.Auth == "password" {
		// restic tokenizes sftp.command via go-shellwords which splits
		// on commas — `password,keyboard-interactive` would become two
		// args and confuse ssh. Stick to the single value.
		parts = append(parts, "-o", "PreferredAuthentications=password",
			"-o", "PubkeyAuthentication=no")
	} else {
		parts = append(parts, "-o", "BatchMode=yes")
	}
	if in.Port > 0 && in.Port != 22 {
		parts = append(parts, "-p", fmt.Sprintf("%d", in.Port))
	}
	return "sftp.command=" + strings.Join(append(parts, sshDestination(in)...), " ")
}

// sshDestination is the tail of every sftp.command: `[-l USER] -s -- HOST
// sftp`. The user goes in as -l's argument and the host after `--`, so ssh
// never reads either as one of its own options: `u@h` placed after the
// options let a user or host starting with `-` (`-oProxyCommand=...`) become
// an ssh option, and ProxyCommand runs through a shell. Both are also quoted
// like KeyPath: restic tokenizes sftp.command on whitespace and then EXECUTES
// it, so a value with spaces must stay one argv entry.
func sshDestination(in SFTPInputs) []string {
	parts := []string{}
	if in.User != "" {
		parts = append(parts, "-l", shellQuote(in.User))
	}
	return append(parts, "-s", "--", shellQuote(in.Host), "sftp")
}

// shellQuote single-quotes a path for embedding in `-o sftp.command=...`.
// We don't actually parse the value through a shell, but restic's parser
// tokenizes on spaces; spaces in key paths would break it. Quoting is
// simple enough to do correctly.
func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t'\"\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
