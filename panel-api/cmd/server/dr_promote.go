package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	backup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #331 Step 4 — `jabali dr promote`. Turns a DR standby into the live primary:
// a final account-inclusive restore, then the role flip that (a) stops the drsync
// loop, (b) lets StandbyReadOnly pass writes again, and (c) wakes the reconciler
// to build every domain's serving config from the replicated DB. This is the ONE
// deliberately-irreversible DR action, so it is confirm-gated and guarded against
// split-brain: if the old primary still answers, promotion is refused without
// --force (two live primaries for the same domains is the failure this whole
// design exists to avoid — the operator points traffic, and must first ensure the
// old primary is down).

// peerAliveProbe reports whether the paired primary still answers on the network.
// Injectable so promotionGate's decision can be unit-tested without a live peer.
var peerAliveProbe = tcpPeerAlive

// peerProbePorts are the TCP ports a live primary would answer on. A successful
// connect to any of them means the box is up (443 = panel/nginx, 22 = sshd).
var peerProbePorts = []string{"443", "22"}

func newDRPromoteCmd() *cobra.Command {
	var yes, force, skipRestore bool
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote this DR standby to the live primary (GH #331)",
		Long: "Promote this standby: run a final account-inclusive restore from the DR " +
			"destination, then flip the role to primary. The drsync loop stops, tenant " +
			"writes are accepted again, and the reconciler builds every domain's serving " +
			"config from the replicated database on its next tick.\n\n" +
			"SPLIT-BRAIN GUARD: if the old primary still answers on the network, promotion " +
			"is refused unless --force. Point DNS/MX at this box only AFTER the old primary " +
			"is confirmed down. This action is irreversible.",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()

			repo := serverSettingsRepoFromDB()
			s, err := repo.Get(ctx)
			if err != nil || s == nil {
				return fmt.Errorf("read server settings: %w", err)
			}
			if !s.IsStandby() {
				return fmt.Errorf("this box is not a DR standby (role=%s); nothing to promote", orDefault(s.ServerRole, models.ServerRolePrimary))
			}

			// Split-brain guard. Only probe when not forcing — --force is the
			// operator asserting the old primary is already down.
			peerAlive := false
			if !force {
				peerAlive = peerAliveProbe(ctx, s.DRPeerLabel)
			}
			allowed, reason := promotionGate(s.IsStandby(), peerAlive, force)
			if !allowed {
				return fmt.Errorf("%s", reason)
			}

			// Confirmation. --yes is the scriptable bypass.
			if !yes {
				fmt.Printf("About to promote this DR standby to PRIMARY (peer=%s).\n", orDefault(s.DRPeerLabel, "unlabelled"))
				if peerAlive {
					fmt.Println("WARNING: the old primary still appears to answer; --force was given. Ensure it is truly isolated.")
				} else {
					fmt.Println("Could not confirm the old primary is alive. Promoting TWO live primaries causes split-brain —")
					fmt.Println("make sure the old primary is powered off or network-isolated before continuing.")
				}
				ok, cerr := confirmYes(os.Stdin, "Type 'yes' to promote (anything else aborts): ")
				if cerr != nil {
					return cerr
				}
				if !ok {
					fmt.Println("Aborted.")
					return nil
				}
			}

			// FENCE the panel service around the restore + role flip (GH #331
			// two-node drill): the panel hosts the drsync loop, and a tick's
			// system.restore that is IN FLIGHT when the role flips completes
			// afterwards and re-stamps role=standby via its dr_pairing block —
			// the promoted box silently re-demoted itself and resumed syncing
			// over the promoted state. Stopping jabali-panel kills the loop
			// (the agent, which serves the restore dispatch, stays up); it is
			// restarted after the flip. Same fence `jabali system restore`
			// uses.
			fmt.Println("Stopping jabali-panel.service for the promotion window…")
			_ = exec.CommandContext(ctx, "systemctl", "stop", "jabali-panel.service").Run()
			defer func() {
				fmt.Println("Starting jabali-panel.service…")
				_ = exec.CommandContext(context.Background(), "systemctl", "start", "jabali-panel.service").Run()
			}()

			// Final account-inclusive restore so home dirs, mail, and per-user DBs
			// land (drsync only applied panel_db+config+tls during replication).
			if !skipRestore {
				if err := promoteRestoreAccounts(ctx, s); err != nil {
					return fmt.Errorf("final restore failed (box NOT promoted; fix and re-run, or --skip-restore to promote without it): %w", err)
				}
			}

			// GH #1169 gap 1 — IP re-point (cutover-critical). Done AFTER the
			// restore (StagePanelDB replaces the panel DB, so any earlier write
			// would be wiped) and folded into the role-flip Upsert below, while
			// the panel is still fenced. managed_ips + public_ipv4 replicate the
			// OLD primary's address; without this every rebuilt vhost binds an IP
			// this box doesn't own and nothing serves.
			if own, derr := detectOwnIPv4(ctx); derr != nil {
				fmt.Printf("WARNING: could not detect this box's IPv4 (%v).\n", derr)
				fmt.Println("  The replicated managed IP / public_ipv4 still point at the OLD primary —")
				fmt.Println("  re-point them by hand (`jabali ip …`) before traffic will serve.")
			} else if !yes {
				cur, _ := ipRepoFromDB().FindDefaultByFamily(ctx, "ipv4")
				curIP := "none"
				if cur != nil {
					curIP = cur.Address
				}
				fmt.Printf("Re-point the default IPv4 from %s (old primary) to %s (this box)? "+
					"public_ipv4 is currently %q.\n", orDefault(curIP, "none"), own, s.PublicIPv4)
				ok, cerr := confirmYes(os.Stdin, "Type 'yes' to re-point (anything else keeps the replicated IPs): ")
				if cerr != nil {
					return cerr
				}
				if ok {
					if msg, rerr := repointDefaultIP(ctx, ipRepoFromDB(), s, own); rerr != nil {
						return fmt.Errorf("IP re-point failed (box NOT promoted; fix and re-run): %w", rerr)
					} else {
						fmt.Println("  " + msg)
					}
				} else {
					fmt.Println("  Skipped IP re-point — the replicated IPs stay; re-point manually before serving.")
				}
			} else {
				// Scriptable (--yes): re-point without prompting.
				if msg, rerr := repointDefaultIP(ctx, ipRepoFromDB(), s, own); rerr != nil {
					return fmt.Errorf("IP re-point failed (box NOT promoted; fix and re-run): %w", rerr)
				} else {
					fmt.Println(msg)
				}
			}

			// Flip the role. Clearing the DR pairing makes drsync inert (it re-reads
			// role each tick) and StandbyReadOnly pass again; the reconciler wakes on
			// its next tick and converges serving config. Last-sync history is kept.
			s.ServerRole = models.ServerRolePrimary
			s.DRDestinationID = nil
			s.DRPairedAt = nil
			if err := repo.Upsert(ctx, s); err != nil {
				return fmt.Errorf("flip role to primary: %w", err)
			}

			// GH #1169 gap 5 — converge PHP. The replicated pools demand PHP
			// versions this empty box may lack and are marked active (which the
			// reconciler skips); install what's missing and flip pools to pending
			// so the reconciler renders them. Best-effort: a failure here logs a
			// warning but does not un-promote (the role flip already committed).
			fmt.Println("Converging PHP pools for this host…")
			if err := promoteConvergePHP(ctx, repository.NewPHPPoolRepository(sharedDB), sharedAgent); err != nil {
				fmt.Printf("  WARNING: PHP pool convergence incomplete (%v); run `jabali php pool reapply-all` after fixing.\n", err)
			}

			fmt.Println("PROMOTED: this box is now the PRIMARY.")
			fmt.Println("Next:")
			fmt.Println("  • The reconciler will build nginx vhosts, DNS zones, and certificates within a minute.")
			fmt.Println("  • Point DNS A/AAAA (and NS if self-hosted) + MX at this box's IP to take live traffic.")
			fmt.Println("  • Verify with `jabali dr status` (role=primary) and `systemctl status jabali-panel`.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirmation (scriptable)")
	cmd.Flags().BoolVar(&force, "force", false, "promote even if the old primary still answers (operator asserts it is down)")
	cmd.Flags().BoolVar(&skipRestore, "skip-restore", false, "skip the final account-inclusive restore (role flip only)")
	return cmd
}

// promotionGate is the pure split-brain decision. Separated from I/O so the
// safety logic is unit-tested. A standby whose old primary still answers is
// refused unless the operator forces it.
func promotionGate(isStandby, peerAlive, force bool) (allowed bool, reason string) {
	if !isStandby {
		return false, "this box is not a DR standby; nothing to promote"
	}
	if peerAlive && !force {
		return false, "the paired primary still answers on the network — promoting now risks split-brain. " +
			"Confirm the old primary is powered off or isolated, then re-run with --force."
	}
	return true, ""
}

// promoteRestoreAccounts dispatches a final system.restore with apply + accounts
// from the DR destination, resolving the newest manifest. Mirrors drsync's
// restore but includes account data (home + mail + per-user DBs).
func promoteRestoreAccounts(ctx context.Context, s *models.ServerSettings) error {
	if s.DRDestinationID == nil || *s.DRDestinationID == "" {
		return fmt.Errorf("standby has no DR destination; cannot run the final restore (use --skip-restore to promote anyway)")
	}
	dest, derr := backupDestinationRepoFromDB().Get(ctx, *s.DRDestinationID)
	if derr != nil || dest == nil {
		return fmt.Errorf("DR destination %s not found", *s.DRDestinationID)
	}
	fmt.Println("Running final account-inclusive restore from the DR destination (this can take a while)…")
	return backupwrapperhelpers.WithDestPasswordFile(ctx, dest, sharedAgent, ssoKeyForCLI(),
		func(passwordFile string) error {
			params := map[string]any{
				"job_id":               ids.NewULID(),
				"manifest_snapshot_id": "latest",
				"apply":                true,
				"include_accounts":     true,
				// os_users MUST be materialized before the account stages
				// land (GH #331 two-node drill): a promoted standby is a box
				// where the tenant Linux users never existed — nothing else
				// creates them (account creation is imperative, not
				// reconciled), so without this the home/DB restore rsyncs
				// into nothing and the reconciler's domain-create loops on
				// "unknown user". The explicit list keeps the whitelist
				// semantics: every auto stage is named.
				"apply_stages": []string{
					backup.StagePanelDB, backup.StagePanelConfig,
					backup.StageTLS, backup.StageOSUsers,
				},
				"repo_url":         dest.URL,
				"destination_kind": dest.Kind,
				"sftp":             backupwrapperhelpers.SFTPWireParams(dest),
			}
			if dest.CredentialsRef != nil {
				params["credentials_ref"] = *dest.CredentialsRef
			}
			if passwordFile != "" {
				params["password_file"] = passwordFile
			}
			raw, err := sharedAgent.Call(ctx, "system.restore", params)
			if err != nil {
				return err
			}
			// Surface what the restore actually did — the drill's first
			// promote "succeeded" while every account stage silently
			// failed, because these warnings were swallowed.
			var out struct {
				Applied       []string `json:"applied"`
				ApplyWarnings []string `json:"apply_warnings"`
			}
			if jerr := json.Unmarshal(raw, &out); jerr == nil {
				for _, a := range out.Applied {
					fmt.Println("  applied:", a)
				}
				zeroAccounts := false
				for _, w := range out.ApplyWarnings {
					fmt.Println("  WARNING:", w)
					if strings.Contains(w, "no account_backup manifest snapshots found") {
						zeroAccounts = true
					}
				}
				// GH #1169 gap 3 — hard-warn on an empty account restore. The
				// drill's first promote "succeeded" while restoring ZERO tenant
				// data, because `dr feed` ships only system_backup and no
				// account_backup manifests ever reach the DR repo. That is a
				// silent data-loss cutover: the box comes up, the reconciler
				// builds vhosts, and every tenant's home/mail/DB is empty. Make
				// it impossible to miss.
				if zeroAccounts {
					fmt.Println("")
					fmt.Println("  ============================================================")
					fmt.Println("  ⚠  NO TENANT DATA WAS RESTORED — the DR repo has no")
					fmt.Println("     account_backup manifests. Every tenant's home directory,")
					fmt.Println("     mail, and per-user databases will be EMPTY after promote.")
					fmt.Println("")
					fmt.Println("     Cause: the DR feed shipped only system backups. Ensure")
					fmt.Println("     account-backup coverage to the DR destination (see")
					fmt.Println("     `jabali dr feed`), let a cycle run, then re-restore before")
					fmt.Println("     pointing live traffic here.")
					fmt.Println("  ============================================================")
					fmt.Println("")
				}
			}
			return nil
		})
}

// tcpPeerAlive reports whether the peer answers a TCP connect on any well-known
// port — the truest "is the box up" signal without decrypting anything. Only a
// dial failure/timeout on every port (or an unusable label) counts as "not
// alive". A raw connect is deliberately used over HTTP so there is no TLS trust
// to weaken; identity doesn't matter here, only reachability.
func tcpPeerAlive(ctx context.Context, peerLabel string) bool {
	host := strings.TrimSpace(peerLabel)
	// A freeform/empty label we can't turn into a host:port → cannot confirm alive.
	if host == "" || strings.ContainsAny(host, " /:") {
		return false
	}
	var d net.Dialer
	for _, port := range peerProbePorts {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		conn, err := d.DialContext(cctx, "tcp", net.JoinHostPort(host, port))
		cancel()
		if err == nil {
			_ = conn.Close()
			return true // the box answered → up
		}
	}
	return false
}

// confirmYes reads a line and returns true only when it is exactly "yes"
// (case-insensitive, trimmed).
func confirmYes(in *os.File, prompt string) (bool, error) {
	fmt.Print(prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}
