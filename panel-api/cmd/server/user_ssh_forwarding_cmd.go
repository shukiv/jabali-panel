package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newUserSSHForwardingCmd opts a hosting user into (or out of) SSH TCP forwarding
// for VS Code Remote-SSH (GH #1229), by managing jabali-ssh-forward membership.
//
// Default OFF: an SSH-enabled user is in the JAB-352 forwarding lockdown. Opting
// in EXCLUDES them from the lockdown and allows local/dynamic loopback forwarding
// only; the sensitive loopback services stay firewall-blocked per-uid, so this
// never re-opens the tunneling vector. Takes effect on the user's next SSH
// connection (sshd re-evaluates Match Group per connection).
func newUserSSHForwardingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh-forwarding <email|username|user-id> <on|off>",
		Short: "Opt a hosting user into/out of SSH TCP forwarding for VS Code Remote-SSH (GH #1229)",
		Long: `Opt a hosting user into or out of SSH TCP forwarding.

Default is OFF — SSH-enabled users cannot forward (JAB-352 lockdown), which blocks
VS Code Remote-SSH. Turning it ON adds the user to the jabali-ssh-forward group:
they get local/dynamic loopback forwarding (enough for VS Code Remote-SSH to reach
its own VS Code Server), while remote/socket/agent/X11/tunnel forwarding stays off
and the sensitive loopback services remain firewall-blocked per-uid.

Takes effect on the user's next SSH connection.`,
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			target, err := resolveUser(ctx, args[0])
			if err != nil {
				return err
			}
			if target.Username == nil || *target.Username == "" {
				return fmt.Errorf("%q has no Linux username — SSH forwarding applies only to hosting users", args[0])
			}
			var enable bool
			switch strings.ToLower(args[1]) {
			case "on", "enable", "true", "yes":
				enable = true
			case "off", "disable", "false", "no":
				enable = false
			default:
				return fmt.Errorf("second argument must be on|off, got %q", args[1])
			}

			// Persist the durable flag FIRST — the SSH reconciler is the source
			// of truth and converges jabali-ssh-forward membership from
			// users.ssh_forwarding_enabled (GH #1229). Without this write the
			// reconciler would strip a CLI-only opt-in within one re-dispatch
			// interval (the flag defaults OFF), silently regressing forwarding.
			if err := userRepo().SetSSHForwardingEnabled(ctx, target.ID, enable); err != nil {
				cliAuditErr(ctx, "ssh.forwarding", "user", target.ID, &target.ID)
				return fmt.Errorf("persist ssh_forwarding_enabled: %w", err)
			}

			// Apply now so it takes effect on the user's next connection without
			// waiting for the reconcile tick. Best-effort: the reconciler
			// self-heals group membership from the flag regardless.
			verb := "ssh.user.leave_forward_group"
			if enable {
				verb = "ssh.user.join_forward_group"
			}
			if _, err := sharedAgent.Call(ctx, verb, map[string]any{"username": *target.Username}); err != nil {
				cliAuditErr(ctx, "ssh.forwarding", "user", target.ID, &target.ID)
				return fmt.Errorf("%s: %w", verb, err)
			}
			cliAuditOK(ctx, "ssh.forwarding", "user", target.ID, &target.ID)

			if enable {
				fmt.Printf("SSH TCP forwarding ENABLED for %q — VS Code Remote-SSH will work on their next connection.\n"+
					"Only loopback forwarding is allowed; sensitive services stay firewall-blocked.\n", *target.Username)
			} else {
				fmt.Printf("SSH TCP forwarding DISABLED for %q — back in the JAB-352 lockdown on their next connection.\n", *target.Username)
			}
			return nil
		},
	}
}
