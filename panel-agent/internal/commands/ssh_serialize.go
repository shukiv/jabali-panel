package commands

import "sync"

// sshOpMu serializes every sshd-mutating write→`sshd -t`→reload sequence across
// all agent handlers (JAB-267), the SSH twin of nginxOpMu.
//
// The agent runs a goroutine per UDS command, so two SSH-config handlers could
// otherwise interleave. `sshd -t` validates the COMPLETE config — the main file
// plus EVERY drop-in in /etc/ssh/sshd_config.d — so a concurrent writer to a
// DIFFERENT drop-in can invalidate a test another handler already passed:
//
//	A: write jabali-xfer.conf   A: sshd -t (passes)
//	                            B: write jabali-sshd.conf (broken/partial)
//	A: reload                   → sshd loads B's broken config
//
// A broken sshd config is a host lockout, so this is stricter than the nginx
// case. Every handler that mutates any sshd_config.d drop-in MUST hold this
// mutex across its ENTIRE write→test→reload critical section — currently
// ftpaccount.sshd_sync (ftp_account_sshd.go) and system.set_ssh_config
// (system_set_ssh_config.go). It is a plain (non-reentrant) sync.Mutex: a
// handler acquires it exactly once around its whole section and never calls
// another mutex-taking helper while holding it.
var sshOpMu sync.Mutex
