// domain_directory_privacy_cmd.go — JAB-130: CLI for Directory Privacy
// (auth_basic protected directories + their credentials). Mirrors the
// /domains/:id/directory-privacy REST surface (domain_directory_privacy.go).
//
// The rule/credential mutation lifecycle — validation, bcrypt-at-write,
// cross-rule containment, converge-schedule — lives in internal/dirprivops, the
// same module the HTTP handler calls, so the two adapters cannot drift. This
// file owns CLI concerns only: flag parsing, --password-stdin handling, the
// read/list commands, and mapping the ops' typed errors onto operator messages.
//
// The CLI passes no reconcile Schedule (Deps.Schedule is nil): the running
// reconciler writes the htpasswd file + nginx location and reloads within a
// tick, same as before. A CLI-built reconciler would render the vhost without
// the auth_basic blocks (see internal/dirprivops.Deps.Schedule), so immediate
// convergence stays the daemon's job.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dirprivops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func dirPrivacyRepo() repository.DomainDirectoryPrivacyRepository {
	return repository.NewDomainDirectoryPrivacyRepository(sharedDB)
}

// cliDirPrivacyDeps builds the shared-module deps for the CLI. Schedule is nil
// by design (see the file header + internal/dirprivops.Deps.Schedule).
func cliDirPrivacyDeps() dirprivops.Deps {
	return dirprivops.Deps{Privacy: dirPrivacyRepo()}
}

// mapDirPrivacyErr turns an ops error into an operator-facing message: a
// validation failure names the offending flag, the containment sentinels get a
// friendly message, and anything else is wrapped with the attempted action.
func mapDirPrivacyErr(action string, err error, ruleID, credID string) error {
	var ve *dirprivops.ValidationError
	switch {
	case errors.As(err, &ve):
		return fmt.Errorf("--%s: %s", ve.Field, ve.Msg)
	case errors.Is(err, dirprivops.ErrRuleNotFound):
		return fmt.Errorf("rule %q not found on this domain", ruleID)
	case errors.Is(err, dirprivops.ErrCredentialNotFound):
		return fmt.Errorf("credential %q does not belong to rule %q", credID, ruleID)
	default:
		return fmt.Errorf("%s: %w", action, err)
	}
}

// resolvePrivacyRule loads a rule by id and confirms it belongs to the domain.
// Used by the read commands (cred-list); mutations resolve inside dirprivops.
func resolvePrivacyRule(ctx context.Context, repo repository.DomainDirectoryPrivacyRepository, domainID, ruleID string) (*models.DomainDirectoryPrivacyRule, error) {
	rules, err := repo.ListRulesByDomain(ctx, domainID)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	for i := range rules {
		if rules[i].ID == ruleID {
			return &rules[i], nil
		}
	}
	return nil, fmt.Errorf("rule %q not found on this domain", ruleID)
}

func domainDirectoryPrivacySubcommands() []*cobra.Command {
	return []*cobra.Command{newDomainDirPrivacyCmd()}
}

func newDomainDirPrivacyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dir-privacy",
		Short: "Manage password-protected directories (auth_basic) (JAB-130)",
	}
	cmd.AddCommand(
		newDirPrivacyListCmd(),
		newDirPrivacyAddCmd(),
		newDirPrivacyRemoveCmd(),
		newDirPrivacyCredListCmd(),
		newDirPrivacyCredAddCmd(),
		newDirPrivacyCredRemoveCmd(),
	)
	return cmd
}

func newDirPrivacyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <domain>",
		Short:   "List protected directories for a domain",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			rules, err := dirPrivacyRepo().ListRulesByDomain(ctx, dom.ID)
			if err != nil {
				return fmt.Errorf("list rules: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"domain": dom.Name, "rules": rules})
			}
			if len(rules) == 0 {
				fmt.Printf("%s: no protected directories\n", dom.Name)
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "RULE ID\tPATH\tREALM")
			for _, r := range rules {
				fmt.Fprintf(w, "%s\t%s\t%s\n", r.ID, r.Path, r.Realm)
			}
			return w.Flush()
		},
	}
}

func newDirPrivacyAddCmd() *cobra.Command {
	var path, realm string
	cmd := &cobra.Command{
		Use:     "add <domain> --path /dir [--realm ...]",
		Short:   "Protect a directory",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			row, err := dirprivops.CreateRule(ctx, cliDirPrivacyDeps(), dom.ID, path, realm)
			if err != nil {
				return mapDirPrivacyErr("create rule", err, "", "")
			}
			fmt.Printf("protected %s%s (rule %s); add a credential with `dir-privacy cred-add`\n", dom.Name, row.Path, row.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "directory path under the docroot (e.g. /admin)")
	cmd.Flags().StringVar(&realm, "realm", "", "auth realm shown in the browser prompt (default Restricted)")
	return cmd
}

func newDirPrivacyRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <domain> <rule-id>",
		Short:   "Remove a protected directory (and its credentials)",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			if err := dirprivops.DeleteRule(ctx, cliDirPrivacyDeps(), dom.ID, args[1]); err != nil {
				return mapDirPrivacyErr("delete rule", err, args[1], "")
			}
			fmt.Printf("removed rule %s from %s\n", args[1], dom.Name)
			return nil
		},
	}
}

func newDirPrivacyCredListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "cred-list <domain> <rule-id>",
		Short:   "List credentials for a protected directory",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			repo := dirPrivacyRepo()
			if _, err := resolvePrivacyRule(ctx, repo, dom.ID, args[1]); err != nil {
				return err
			}
			creds, err := repo.ListCredentialsByRule(ctx, args[1])
			if err != nil {
				return fmt.Errorf("list credentials: %w", err)
			}
			if jsonOutput {
				out := make([]map[string]string, 0, len(creds))
				for _, cr := range creds {
					out = append(out, map[string]string{"id": cr.ID, "username": cr.Username})
				}
				return printJSON(map[string]any{"rule_id": args[1], "credentials": out})
			}
			if len(creds) == 0 {
				fmt.Println("no credentials (directory is protected but unreachable until you add one)")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "CRED ID\tUSERNAME")
			for _, cr := range creds {
				fmt.Fprintf(w, "%s\t%s\n", cr.ID, cr.Username)
			}
			return w.Flush()
		},
	}
}

func newDirPrivacyCredAddCmd() *cobra.Command {
	var username, password string
	var viaStdin bool
	cmd := &cobra.Command{
		Use:     "cred-add <domain> <rule-id> --user <name> (--password <p> | --password-stdin)",
		Short:   "Add a credential to a protected directory",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			if password != "" && viaStdin {
				return fmt.Errorf("--password and --password-stdin are mutually exclusive")
			}
			if viaStdin {
				p, err := readPasswordStdin()
				if err != nil {
					return err
				}
				password = p
			}
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			row, err := dirprivops.CreateCredential(ctx, cliDirPrivacyDeps(), dom.ID, args[1], username, password)
			if err != nil {
				// Present username/password validation as the flag that carries it.
				var ve *dirprivops.ValidationError
				if errors.As(err, &ve) && ve.Field == "username" {
					return fmt.Errorf("--user: %s", ve.Msg)
				}
				return mapDirPrivacyErr("create credential", err, args[1], "")
			}
			fmt.Printf("added credential %q (id %s) to rule %s\n", username, row.ID, args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&username, "user", "", "basic-auth username")
	cmd.Flags().StringVar(&password, "password", "", "basic-auth password (8-128 chars; prefer --password-stdin)")
	cmd.Flags().BoolVar(&viaStdin, "password-stdin", false, "read the password from stdin (no argv leak)")
	return cmd
}

func newDirPrivacyCredRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "cred-remove <domain> <rule-id> <cred-id>",
		Short:   "Remove a credential from a protected directory",
		Args:    cobra.ExactArgs(3),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			// dirprivops.DeleteCredential enforces cross-rule containment
			// (cred.RuleID == rule.ID) before deleting — the JAB-316 guard,
			// failing closed on a mismatch or a missing credential.
			if err := dirprivops.DeleteCredential(ctx, cliDirPrivacyDeps(), dom.ID, args[1], args[2]); err != nil {
				return mapDirPrivacyErr("delete credential", err, args[1], args[2])
			}
			fmt.Printf("removed credential %s\n", args[2])
			return nil
		},
	}
}
