package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newSettingsMailHostnameCmd is `jabali settings mail-hostname [--applied]`:
// a read-only view of the panel mail hostname (JAB-390).
//
// install.sh renders the Bulwark JMAP URL and the /webmail redirects from
// `--applied`, so a `jabali update` after a shared-mail-hostname switchover
// keeps the applied name instead of re-deriving mail.<hostname>. Only a
// value that passes models.ValidateMailHostname is ever printed, normalized:
// the output is interpolated into config files.
func newSettingsMailHostnameCmd() *cobra.Command {
	var appliedOnly bool
	cmd := &cobra.Command{
		Use:   "mail-hostname",
		Short: "Print the panel mail hostname in effect (read-only)",
		Long: "Print the panel mail hostname in effect: the applied custom mail hostname, " +
			"or mail.<panel-hostname> when none is applied. With --applied, print only a " +
			"custom applied mail hostname, and nothing when the derived name is in effect.",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			s, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx)
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}
			return printMailHostname(cmd.OutOrStdout(), s, appliedOnly)
		},
	}
	cmd.Flags().BoolVar(&appliedOnly, "applied", false,
		"print only a custom applied mail hostname; print nothing when mail.<hostname> is in effect")
	return cmd
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
