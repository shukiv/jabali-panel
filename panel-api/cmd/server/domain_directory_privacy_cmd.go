// domain_directory_privacy_cmd.go — JAB-130: CLI for Directory Privacy
// (auth_basic protected directories + their credentials). Mirrors the
// /domains/:id/directory-privacy REST surface (domain_directory_privacy.go)
// direct-DB; the running reconciler writes the htpasswd file + nginx location
// and reloads within a tick, same as the web path.

package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

var (
	cliPrivacyPathRE = regexp.MustCompile(`^/[A-Za-z0-9_./-]*$`)
	cliPrivacyUserRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// cliValidatePrivacyPath mirrors validatePrivacyPath in the API package.
func cliValidatePrivacyPath(in string) (string, error) {
	p := strings.TrimSpace(in)
	if p == "" {
		return "", fmt.Errorf("--path required")
	}
	if len(p) > 255 {
		return "", fmt.Errorf("--path too long (max 255)")
	}
	if p == "/" {
		return "/", nil
	}
	if !cliPrivacyPathRE.MatchString(p) {
		return "", fmt.Errorf("--path must start with / and contain only [A-Za-z0-9_./-]")
	}
	if strings.Contains(p, "//") {
		return "", fmt.Errorf("--path must not contain //")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("--path must not contain ..")
		}
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
	}
	return p, nil
}

func cliValidatePrivacyRealm(in string) (string, error) {
	r := strings.TrimSpace(in)
	if r == "" {
		r = "Restricted"
	}
	if len(r) > 255 {
		return "", fmt.Errorf("--realm too long (max 255)")
	}
	for _, c := range r {
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return "", fmt.Errorf(`--realm must be printable ASCII without " or \`)
		}
	}
	return r, nil
}

// resolvePrivacyRule loads a rule by id and confirms it belongs to the domain.
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
			repo := repository.NewDomainDirectoryPrivacyRepository(sharedDB)
			rules, err := repo.ListRulesByDomain(ctx, dom.ID)
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
			cleanPath, err := cliValidatePrivacyPath(path)
			if err != nil {
				return err
			}
			cleanRealm, err := cliValidatePrivacyRealm(realm)
			if err != nil {
				return err
			}
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			row := &models.DomainDirectoryPrivacyRule{ID: ids.NewULID(), DomainID: dom.ID, Path: cleanPath, Realm: cleanRealm}
			if err := repository.NewDomainDirectoryPrivacyRepository(sharedDB).CreateRule(ctx, row); err != nil {
				return fmt.Errorf("create rule: %w", err)
			}
			fmt.Printf("protected %s%s (rule %s); add a credential with `dir-privacy cred-add`\n", dom.Name, cleanPath, row.ID)
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
			repo := repository.NewDomainDirectoryPrivacyRepository(sharedDB)
			if _, err := resolvePrivacyRule(ctx, repo, dom.ID, args[1]); err != nil {
				return err
			}
			if err := repo.DeleteRule(ctx, args[1]); err != nil {
				return fmt.Errorf("delete rule: %w", err)
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
			repo := repository.NewDomainDirectoryPrivacyRepository(sharedDB)
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
			if !cliPrivacyUserRE.MatchString(username) {
				return fmt.Errorf("--user must match [A-Za-z0-9._-] (1-64 chars)")
			}
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
			if len(password) < 8 || len(password) > 128 {
				return fmt.Errorf("password must be 8-128 characters")
			}
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			repo := repository.NewDomainDirectoryPrivacyRepository(sharedDB)
			if _, err := resolvePrivacyRule(ctx, repo, dom.ID, args[1]); err != nil {
				return err
			}
			hash, err := auth.HashPassword(password, 0)
			if err != nil {
				return fmt.Errorf("hash password: %w", err)
			}
			row := &models.DomainDirectoryPrivacyCredential{ID: ids.NewULID(), RuleID: args[1], Username: username, PasswordHash: hash}
			if err := repo.CreateCredential(ctx, row); err != nil {
				return fmt.Errorf("create credential: %w", err)
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
			repo := repository.NewDomainDirectoryPrivacyRepository(sharedDB)
			rule, err := resolvePrivacyRule(ctx, repo, dom.ID, args[1])
			if err != nil {
				return err
			}
			// JAB-316: verify the credential belongs to THIS rule before deleting
			// it. Without this containment check the CLI deletes any credential ID
			// under the guise of an unrelated rule the operator names — a
			// cross-rule credential deletion. The HTTP adapter already enforces
			// cred.RuleID == rule.ID; fail closed (never delete) on a mismatch or
			// a missing credential.
			cred, err := repo.FindCredentialByID(ctx, args[2])
			if err != nil {
				return fmt.Errorf("credential %s not found", args[2])
			}
			if cred.RuleID != rule.ID {
				return fmt.Errorf("credential %s does not belong to rule %s", args[2], rule.ID)
			}
			if err := repo.DeleteCredential(ctx, args[2]); err != nil {
				return fmt.Errorf("delete credential: %w", err)
			}
			fmt.Printf("removed credential %s\n", args[2])
			return nil
		},
	}
}
