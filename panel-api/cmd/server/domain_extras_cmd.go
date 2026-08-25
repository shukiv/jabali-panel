// domain_extras_cmd.go — M6.5 CLI surfaces for catch-all + disclaimer.
// Mirrors panel-api/internal/api/domain_{catchall,disclaimer}.go but
// goes direct DB + agent so operators can drive the panel from a script
// without a Kratos session.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainmailpolicy"
)

// mailPolicyPush is the domainmailpolicy.PushFunc backed by the CLI's agent
// client. It returns the agent error so Set/Clear can turn a failed push into a
// warning while keeping the DB the desired-state truth (the reconciler
// re-asserts).
func mailPolicyPush(ctx context.Context, cmd string, params map[string]any) error {
	_, err := callAgentMailbox(ctx, cmd, params)
	return err
}

// domainExtraSubcommands returns the cobra commands wired into newDomainCmd.
func domainExtraSubcommands() []*cobra.Command {
	return []*cobra.Command{
		newDomainCatchallCmd(),
		newDomainDisclaimerCmd(),
	}
}

// ---- catchall -------------------------------------------------------------

func newDomainCatchallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catchall",
		Short: "Manage per-domain catch-all routing",
	}
	cmd.AddCommand(
		newDomainCatchallSetCmd(),
		newDomainCatchallClearCmd(),
		newDomainCatchallShowCmd(),
	)
	return cmd
}

func newDomainCatchallSetCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:     "set <domain-name-or-id>",
		Short:   "Route mail to unknown@<domain> to --target",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(target) == "" {
				return fmt.Errorf("--target is required (use 'catchall clear' to remove)")
			}
			canon, warning, err := domainmailpolicy.SetCatchall(ctx,
				domainmailpolicy.Deps{Domains: domainRepoFromDB()}, dom, target, mailPolicyPush)
			if err != nil {
				return fmt.Errorf("set catch-all: %w", err)
			}
			fmt.Printf("Catch-all for %s -> %s\n", dom.Name, canon)
			if warning != "" {
				fmt.Printf("Note: %s\n", warning)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "Destination email address (required)")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}

func newDomainCatchallClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "clear <domain-name-or-id>",
		Short:   "Remove the catch-all rule for a domain",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			warning, err := domainmailpolicy.ClearCatchall(ctx,
				domainmailpolicy.Deps{Domains: domainRepoFromDB()}, dom, mailPolicyPush)
			if err != nil {
				return fmt.Errorf("clear catch-all: %w", err)
			}
			fmt.Printf("Catch-all cleared for %s\n", dom.Name)
			if warning != "" {
				fmt.Printf("Note: %s\n", warning)
			}
			return nil
		},
	}
}

func newDomainCatchallShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <domain-name-or-id>",
		Short:   "Print the current catch-all target",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			target := "(none)"
			if dom.CatchallTarget != nil {
				target = *dom.CatchallTarget
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"domain": dom.Name,
					"target": dom.CatchallTarget,
				})
			}
			fmt.Printf("Domain:    %s\n", dom.Name)
			fmt.Printf("Catch-all: %s\n", target)
			return nil
		},
	}
}

// ---- disclaimer -----------------------------------------------------------

func newDomainDisclaimerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disclaimer",
		Short: "Manage per-domain outbound disclaimer",
	}
	cmd.AddCommand(
		newDomainDisclaimerSetCmd(),
		newDomainDisclaimerClearCmd(),
		newDomainDisclaimerShowCmd(),
	)
	return cmd
}

func newDomainDisclaimerSetCmd() *cobra.Command {
	var (
		text string
		file string
	)
	cmd := &cobra.Command{
		Use:     "set <domain-name-or-id>",
		Short:   "Set + enable the outbound disclaimer for a domain",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if text == "" && file == "" {
				return fmt.Errorf("one of --text or --file is required")
			}
			if file != "" {
				b, err := os.ReadFile(file)
				if err != nil {
					return fmt.Errorf("read disclaimer file: %w", err)
				}
				text = string(b)
			}
			text = strings.TrimSpace(text)
			if text == "" {
				return fmt.Errorf("disclaimer text must not be empty")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			norm, warning, err := domainmailpolicy.SetDisclaimer(ctx,
				domainmailpolicy.Deps{Domains: domainRepoFromDB()}, dom, true, text, mailPolicyPush)
			if err != nil {
				return fmt.Errorf("set disclaimer: %w", err)
			}
			fmt.Printf("Disclaimer enabled for %s (%d bytes)\n", dom.Name, len(norm))
			if warning != "" {
				fmt.Printf("Note: %s\n", warning)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&text, "text", "", "Disclaimer text (UTF-8, plain or HTML)")
	cmd.Flags().StringVar(&file, "file", "", "Read disclaimer from this file (overrides --text)")
	return cmd
}

func newDomainDisclaimerClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "clear <domain-name-or-id>",
		Short:   "Disable + remove the outbound disclaimer for a domain",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			warning, err := domainmailpolicy.ClearDisclaimer(ctx,
				domainmailpolicy.Deps{Domains: domainRepoFromDB()}, dom, mailPolicyPush)
			if err != nil {
				return fmt.Errorf("clear disclaimer: %w", err)
			}
			fmt.Printf("Disclaimer cleared for %s\n", dom.Name)
			if warning != "" {
				fmt.Printf("Note: %s\n", warning)
			}
			return nil
		},
	}
}

func newDomainDisclaimerShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <domain-name-or-id>",
		Short:   "Print the current disclaimer for a domain",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			text := ""
			if dom.DisclaimerText != nil {
				text = *dom.DisclaimerText
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"domain":  dom.Name,
					"enabled": dom.DisclaimerEnabled,
					"text":    text,
				})
			}
			fmt.Printf("Domain:    %s\n", dom.Name)
			fmt.Printf("Enabled:   %v\n", dom.DisclaimerEnabled)
			if text == "" {
				fmt.Println("Text:      (none)")
			} else {
				fmt.Printf("Text (%d bytes):\n%s\n", len(text), text)
			}
			_ = errors.New
			return nil
		},
	}
}
