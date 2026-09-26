package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailhostops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newSettingsMailHostnameCmd is `jabali settings mail-hostname`: the panel
// mail hostname (JAB-390).
//
// Without flags, or with --applied, it prints the name. install.sh renders
// the Bulwark JMAP URL and the /webmail redirects from `--applied`, so a
// `jabali update` after a shared-mail-hostname switchover keeps the applied
// name instead of re-deriving mail.<hostname>. Only a value that passes
// models.ValidateMailHostname is ever printed, normalized: the output is
// interpolated into config files.
//
// --set NAME requests a switchover to NAME (mailhostops.Request, the rules
// the API applies); the reconciler applies it once NAME points at this
// server and its certificate is issued. --cancel withdraws a pending or
// failed request, and --status prints the request's progress.
func newSettingsMailHostnameCmd() *cobra.Command {
	var appliedOnly, cancelReq, status bool
	var setName string
	cmd := &cobra.Command{
		Use:   "mail-hostname",
		Short: "Show the panel mail hostname, or request, cancel or follow a change",
		Long: "Print the panel mail hostname in effect: the applied custom mail hostname, " +
			"or mail.<panel-hostname> when none is applied. With --applied, print only a " +
			"custom applied mail hostname, and nothing when the derived name is in effect.\n\n" +
			"--set NAME requests a change to NAME. The panel applies it once NAME and " +
			"mail.<panel-hostname> point at this server and a certificate for both is " +
			"issued; mail.<panel-hostname> keeps being served. Set mail.<panel-hostname> " +
			"to switch back. --status shows the request's progress; --cancel withdraws a " +
			"request that is not being applied right now.",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mode, err := mailHostnameCmdMode(cmd.Flags().Changed("set"), cancelReq, status, appliedOnly)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			out := cmd.OutOrStdout()
			audit := func(action, target, result string) { cliAudit(ctx, action, "server_settings", target, result, nil) }
			switch mode {
			case mailHostnameSet:
				return runMailHostnameSet(ctx, out, cliMailHostnameDeps(), audit, setName)
			case mailHostnameCancel:
				return runMailHostnameCancel(ctx, out, cliMailHostnameDeps(), audit)
			case mailHostnameStatus:
				sw, err := repository.NewMailHostnameSwitchoverRepository(sharedDB).Get(ctx)
				if errors.Is(err, repository.ErrNotFound) {
					sw, err = nil, nil
				}
				if err != nil {
					return fmt.Errorf("load mail hostname change: %w", err)
				}
				return printMailHostnameStatus(out, sw)
			}
			s, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx)
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}
			return printMailHostname(out, s, appliedOnly)
		},
	}
	cmd.Flags().BoolVar(&appliedOnly, "applied", false,
		"print only a custom applied mail hostname; print nothing when mail.<hostname> is in effect")
	cmd.Flags().StringVar(&setName, "set", "", "request a change of the panel mail hostname to this name")
	cmd.Flags().BoolVar(&cancelReq, "cancel", false, "withdraw a pending or failed mail hostname change")
	cmd.Flags().BoolVar(&status, "status", false, "print the progress of a requested mail hostname change")
	return cmd
}

type mailHostnameMode int

const (
	mailHostnameRead mailHostnameMode = iota
	mailHostnameSet
	mailHostnameCancel
	mailHostnameStatus
)

// mailHostnameCmdMode picks the mode from the flags; --set, --cancel,
// --status and --applied exclude each other.
func mailHostnameCmdMode(set, cancelReq, status, appliedOnly bool) (mailHostnameMode, error) {
	n := 0
	mode := mailHostnameRead
	for _, f := range []struct {
		on   bool
		mode mailHostnameMode
	}{{set, mailHostnameSet}, {cancelReq, mailHostnameCancel}, {status, mailHostnameStatus}, {appliedOnly, mailHostnameRead}} {
		if f.on {
			n++
			mode = f.mode
		}
	}
	if n > 1 {
		return 0, errors.New("--set, --cancel, --status and --applied cannot be combined")
	}
	return mode, nil
}

// cliMailHostnameDeps wires mailhostops to the database.
func cliMailHostnameDeps() mailhostops.RequestDeps {
	return mailhostops.RequestDeps{
		Settings:   repository.NewServerSettingsRepository(sharedDB),
		PanelCerts: repository.NewPanelCertificateRepository(sharedDB),
		Domains:    repository.NewDomainRepository(sharedDB),
		Aliases:    repository.NewWebDomainAliasRepository(sharedDB),
		Switchover: repository.NewMailHostnameSwitchoverRepository(sharedDB),
	}
}

// runMailHostnameSet records a switchover request and audits the outcome.
func runMailHostnameSet(ctx context.Context, out io.Writer, deps mailhostops.RequestDeps, audit func(action, target, result string), name string) error {
	got, err := mailhostops.Request(ctx, deps, name, "cli")
	target := got
	if target == "" {
		target = name
		if len(target) > 253 {
			target = target[:253]
		}
	}
	switch {
	case err == nil:
		audit("settings.mail_hostname.request", target, models.AuditResultOK)
		_, err = fmt.Fprintf(out, "requested: %s (pending)\nThe panel applies it once %s and the current mail hostname point at this server and the certificate is issued. Follow it with --status.\n", got, got)
		return err
	case errors.Is(err, mailhostops.ErrInvalidName), errors.Is(err, mailhostops.ErrNotReady),
		errors.Is(err, mailhostops.ErrNameRefused), errors.Is(err, repository.ErrSwitchoverInFlight):
		audit("settings.mail_hostname.request", target, models.AuditResultDenied)
	default:
		audit("settings.mail_hostname.request", target, models.AuditResultError)
	}
	return err
}

// runMailHostnameCancel withdraws a pending or failed request and audits the
// outcome.
func runMailHostnameCancel(ctx context.Context, out io.Writer, deps mailhostops.RequestDeps, audit func(action, target, result string)) error {
	err := mailhostops.Cancel(ctx, deps)
	switch {
	case err == nil:
		audit("settings.mail_hostname.cancel", "", models.AuditResultOK)
		_, err = fmt.Fprintln(out, "cancelled")
		return err
	case errors.Is(err, repository.ErrNotFound):
		audit("settings.mail_hostname.cancel", "", models.AuditResultDenied)
		return errors.New("there is no pending mail hostname change to cancel")
	case errors.Is(err, repository.ErrSwitchoverInFlight):
		audit("settings.mail_hostname.cancel", "", models.AuditResultDenied)
		return fmt.Errorf("the certificate for the new mail hostname is being issued; try again in a few minutes: %w", err)
	default:
		audit("settings.mail_hostname.cancel", "", models.AuditResultError)
		return err
	}
}

// printMailHostnameStatus prints a switchover request's progress, one
// "key: value" per line.
func printMailHostnameStatus(out io.Writer, sw *models.MailHostnameSwitchover) error {
	if sw == nil || sw.Desired == nil || sw.Status == models.MailHostnameSwitchoverIdle {
		_, err := fmt.Fprintln(out, "no mail hostname change requested")
		return err
	}
	lines := fmt.Sprintf("desired: %s\nstatus: %s\n", *sw.Desired, sw.Status)
	if sw.LastError != "" {
		lines += "last_error: " + sw.LastError + "\n"
	}
	if sw.NextRetryAt != nil {
		lines += "next_retry_at: " + sw.NextRetryAt.UTC().Format(time.RFC3339) + "\n"
	}
	_, err := io.WriteString(out, lines)
	return err
}

// printMailHostname writes the panel mail hostname to out, one line. With
// appliedOnly it writes only a custom applied mail hostname (nothing when the
// derived mail.<hostname> is in effect). A stored value that fails
// validation is never written; it counts as "none applied", the same
// fail-safe read models.EffectiveMailHostname does.
func printMailHostname(out io.Writer, s *models.ServerSettings, appliedOnly bool) error {
	if appliedOnly {
		if applied, ok := models.AppliedMailHostname(s.MailHostname); ok {
			_, err := fmt.Fprintln(out, applied)
			return err
		}
		return nil
	}
	name := models.EffectiveMailHostname(s.MailHostname, s.Hostname)
	if name == "" {
		return errors.New("no panel hostname is set and no mail hostname is applied")
	}
	_, err := fmt.Fprintln(out, name)
	return err
}
