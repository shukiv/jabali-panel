// per_user_egress_ops_cmd.go — JAB-131: CLI parity for the per-user PHP-FPM
// egress firewall (M34) request queue + policy. The GUI/API can list, approve,
// and deny egress requests and set a user's policy; the CLI previously only had
// `flip-mature`. These commands go direct-DB (approve/deny reuse the shared
// egressops.DecideRequest so the fold-into-policy cascade stays byte-identical
// to the web path); the running reconciler re-renders nftables within a tick.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/egressops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// maxCLIEgressExtras mirrors maxAllowedExtras in internal/api/user_egress.go.
const maxCLIEgressExtras = 50

func egressReqRepo() repository.UserEgressRequestRepository {
	return repository.NewUserEgressRequestRepository(sharedDB)
}

func egressPolicyRepo() repository.UserEgressPolicyRepository {
	return repository.NewUserEgressPolicyRepository(sharedDB)
}

func newPerUserEgressRequestsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "requests",
		Short:   "List pending egress requests (the admin queue)",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			rows, err := egressReqRepo().ListPending(ctx)
			if err != nil {
				return fmt.Errorf("list pending: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"data": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Println("no pending egress requests")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "REQUEST ID\tUSER ID\tCIDR\tPORT\tPROTO\tREASON\tCREATED")
			for _, r := range rows {
				port := "-"
				if r.Port != nil {
					port = strconv.FormatUint(uint64(*r.Port), 10)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					r.ID, r.UserID, r.CIDR, port, r.Protocol, truncateReason(r.Reason), r.CreatedAt.Format(time.RFC3339))
			}
			return w.Flush()
		},
	}
}

func truncateReason(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 40 {
		return s[:37] + "..."
	}
	return s
}

func newPerUserEgressDecideCmd(approve bool) *cobra.Command {
	use, verb, status := "deny <request-id>", "Deny", models.UserEgressRequestStatusDenied
	if approve {
		use, verb, status = "approve <request-id>", "Approve", models.UserEgressRequestStatusApproved
	}
	return &cobra.Command{
		Use:     use,
		Short:   verb + " a pending egress request",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			requestID := args[0]
			// reviewedBy is empty: the CLI has no interactive session. The
			// audit row below records the operator provenance.
			req, err := egressops.DecideRequest(ctx,
				egressops.Deps{Requests: egressReqRepo(), Policies: egressPolicyRepo()},
				requestID, status, "")
			if err != nil {
				switch {
				case errors.Is(err, repository.ErrNotFound):
					return fmt.Errorf("request %q not found", requestID)
				case errors.Is(err, egressops.ErrAlreadyDecided):
					return fmt.Errorf("request %q is already %s (not pending)", requestID, req.Status)
				default:
					return fmt.Errorf("decide: %w", err)
				}
			}
			cliAuditOK(ctx, "egress.request."+status, "user", req.UserID, &req.UserID)
			if approve {
				fmt.Printf("approved request %s (folded %s into %s's policy; reconciler will re-render nftables)\n",
					requestID, req.CIDR, req.UserID)
			} else {
				fmt.Printf("denied request %s\n", requestID)
			}
			return nil
		},
	}
}

func newPerUserEgressGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <email-or-id>",
		Short:   "Show a user's egress policy",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			u, err := resolveUser(ctx, args[0])
			if err != nil || u == nil {
				return fmt.Errorf("user %q not found", args[0])
			}
			row, err := egressPolicyRepo().Get(ctx, u.ID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					fmt.Printf("%s has no egress policy row (default applies)\n", args[0])
					return nil
				}
				return fmt.Errorf("get policy: %w", err)
			}
			extras, _ := row.DecodeAllowedExtra()
			if jsonOutput {
				return printJSON(map[string]any{"user_id": row.UserID, "state": row.State, "allowed_extra": extras})
			}
			fmt.Printf("user:  %s (%s)\nstate: %s\nallowed_extra (%d):\n", args[0], row.UserID, row.State, len(extras))
			for _, e := range extras {
				fmt.Printf("  %s\n", formatDestination(e))
			}
			return nil
		},
	}
}

func newPerUserEgressSummaryCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "summary",
		Short:   "Show egress policy state counts + pending queue depth",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			counts, err := egressPolicyRepo().StateCounts(ctx)
			if err != nil {
				return fmt.Errorf("state counts: %w", err)
			}
			pending, err := egressReqRepo().ListPending(ctx)
			if err != nil {
				return fmt.Errorf("list pending: %w", err)
			}
			out := map[string]any{
				"states":           counts,
				"pending_requests": len(pending),
			}
			if jsonOutput {
				return printJSON(out)
			}
			fmt.Printf("egress policy states:\n")
			for _, s := range []string{models.UserEgressStateOff, models.UserEgressStateLearning, models.UserEgressStateEnforced} {
				fmt.Printf("  %-9s %d\n", s, counts[s])
			}
			fmt.Printf("pending requests: %d\n", len(pending))
			return nil
		},
	}
}

func newPerUserEgressSetPolicyCmd() *cobra.Command {
	var (
		state  string
		allows []string
	)
	cmd := &cobra.Command{
		Use:   "set-policy <email-or-id>",
		Short: "Set a user's egress state + allowed destinations (replaces the list)",
		Long: "Set the per-user egress policy. --state is one of off|learning|enforced.\n" +
			"Each --allow is CIDR[,PORT[,PROTO]] (repeatable), e.g.\n" +
			"  --allow 10.0.0.0/8 --allow 1.2.3.4/32,443 --allow 1.2.3.4/32,53,udp\n" +
			"The allow list REPLACES the user's allowed_extra (matches PUT /users/:id/egress).",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			if !validCLIEgressState(state) {
				return fmt.Errorf("--state must be one of off|learning|enforced (got %q)", state)
			}
			if len(allows) > maxCLIEgressExtras {
				return fmt.Errorf("too many --allow entries (%d, max %d)", len(allows), maxCLIEgressExtras)
			}
			extras := make([]models.EgressDestination, 0, len(allows))
			for i, a := range allows {
				dest, err := parseCLIAllow(a)
				if err != nil {
					return fmt.Errorf("--allow %q (index %d): %w", a, i, err)
				}
				extras = append(extras, dest)
			}

			u, err := resolveUser(ctx, args[0])
			if err != nil || u == nil {
				return fmt.Errorf("user %q not found", args[0])
			}

			jsonExtras, _ := json.Marshal(extras)
			policy := &models.UserEgressPolicy{
				UserID:       u.ID,
				State:        state,
				AllowedExtra: jsonExtras,
			}
			if err := egressPolicyRepo().Upsert(ctx, policy); err != nil {
				return fmt.Errorf("upsert policy: %w", err)
			}
			cliAuditOK(ctx, "egress.policy.set", "user", u.ID, &u.ID)
			fmt.Printf("set %s egress policy: state=%s, %d allowed destination(s) (reconciler will re-render nftables)\n",
				args[0], state, len(extras))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&state, "state", "", "egress state: off|learning|enforced (required)")
	f.StringArrayVar(&allows, "allow", nil, "allowed destination CIDR[,PORT[,PROTO]] (repeatable; replaces the list)")
	_ = cmd.MarkFlagRequired("state")
	return cmd
}

func validCLIEgressState(s string) bool {
	return s == models.UserEgressStateOff ||
		s == models.UserEgressStateLearning ||
		s == models.UserEgressStateEnforced
}

// parseCLIAllow parses a CIDR[,PORT[,PROTO]] token into a validated
// EgressDestination. Mirrors validateExtra in internal/api/user_egress.go.
func parseCLIAllow(s string) (models.EgressDestination, error) {
	var dest models.EgressDestination
	parts := strings.Split(s, ",")
	cidr := strings.TrimSpace(parts[0])
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		return dest, fmt.Errorf("cidr: %v", err)
	}
	dest.CIDR = cidr
	if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
		p, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || p < 1 || p > 65535 {
			return dest, fmt.Errorf("port must be 1..65535")
		}
		dest.Port = &p
	}
	proto := models.UserEgressProtocolTCP
	if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
		proto = strings.ToLower(strings.TrimSpace(parts[2]))
	}
	if proto != models.UserEgressProtocolTCP && proto != models.UserEgressProtocolUDP {
		return dest, fmt.Errorf("protocol must be tcp or udp")
	}
	dest.Protocol = proto
	if len(parts) > 3 {
		return dest, fmt.Errorf("too many fields (want CIDR[,PORT[,PROTO]])")
	}
	return dest, nil
}

func formatDestination(e models.EgressDestination) string {
	proto := e.Protocol
	if proto == "" {
		proto = models.UserEgressProtocolTCP
	}
	port := "any"
	if e.Port != nil {
		port = strconv.Itoa(*e.Port)
	}
	out := fmt.Sprintf("%s port=%s proto=%s", e.CIDR, port, proto)
	if e.Comment != "" {
		out += "  # " + e.Comment
	}
	return out
}
