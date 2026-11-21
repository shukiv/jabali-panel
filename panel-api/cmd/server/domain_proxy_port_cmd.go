package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
)

// newDomainProxyPortCmd is `jabali domain proxy-port <domain> <port>` (GH
// #2544): move a reverse-proxy domain to another loopback port. Same rules as
// PUT /domains/:id/reverse-proxy-port (domainops.ChangeReverseProxyPort).
func newDomainProxyPortCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "proxy-port <domain-name|domain-id> <port>",
		Short:   "Change the local port a reverse-proxy domain forwards to",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("port must be a number, got %q", args[1])
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			d, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			// The system-uid probe needs the agent. Assign it only when the
			// pointer is set: a nil *agent.Client in the interface would pass
			// the module's nil check and panic on Call.
			var ag agent.AgentInterface
			if initAgent() == nil && sharedAgent != nil {
				ag = sharedAgent
			}
			old := d.ReverseProxyPort
			if err := domainops.ChangeReverseProxyPort(ctx, domainRepoFromDB(), ag, d, port); err != nil {
				cliAuditErr(ctx, "domain.proxy_port", "domain", d.ID, &d.UserID)
				switch {
				case errors.Is(err, domainops.ErrNotReverseProxy):
					return fmt.Errorf("%s serves its own files, not a reverse proxy", d.Name)
				case errors.Is(err, domainops.ErrReverseProxyPortInvalid):
					return err // the static policy's own reason
				case errors.Is(err, domainops.ErrReverseProxyPortSystemBound):
					return fmt.Errorf("port %d is already in use by a system service — choose another", port)
				case errors.Is(err, domainops.ErrReverseProxyPortInUse):
					return fmt.Errorf("port %d is already assigned to another domain — choose another", port)
				default:
					return fmt.Errorf("change the port of %s: %w", d.Name, err)
				}
			}
			if uint32(port) == old {
				fmt.Printf("%s already forwards to 127.0.0.1:%d.\n", d.Name, port)
				return nil
			}
			cliAuditOK(ctx, "domain.proxy_port", "domain", d.ID, &d.UserID)
			// No in-process reconciler in the CLI: the running panel's periodic
			// pass re-renders the vhost.
			fmt.Printf("%s now forwards to 127.0.0.1:%d (was %d). The site switches within about a minute.\n", d.Name, port, old)
			return nil
		},
	}
}
