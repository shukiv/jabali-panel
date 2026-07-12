// domain_php_settings_cmd.go — JAB-129: CLI parity for per-domain php.ini
// directives (memory_limit, upload_max_filesize, post_max_size, max_input_vars,
// max_execution_time, max_input_time). Mirrors GET/PATCH /domains/:id/php-settings
// (domain_php_settings.go) but goes direct-DB; the running reconciler re-renders
// the FPM pool config within a tick, same as the web path. PHP *version* has its
// own command (`domain php-version`).

package main

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

var cliPHPSizeRe = regexp.MustCompile(`^\d{1,8}[KMGkmg]?$`)

func cliValidatePHPSize(name, v string) error {
	if len(v) > 8 || !cliPHPSizeRe.MatchString(v) {
		return fmt.Errorf("--%s %q invalid (digits + optional K/M/G, max 8 chars)", name, v)
	}
	return nil
}

func cliValidatePHPInt(name string, v int) error {
	if v < 1 || v > 86400 {
		return fmt.Errorf("--%s %d out of range (1..86400)", name, v)
	}
	return nil
}

// domainPHPSettingsSubcommands returns the `domain php-settings` group.
func domainPHPSettingsSubcommands() []*cobra.Command {
	return []*cobra.Command{newDomainPHPSettingsCmd()}
}

func newDomainPHPSettingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "php-settings",
		Short: "Get/set a domain's php.ini directives (JAB-129)",
	}
	cmd.AddCommand(newDomainPHPSettingsGetCmd(), newDomainPHPSettingsSetCmd())
	return cmd
}

func newDomainPHPSettingsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <domain-name-or-id>",
		Short:   "Show a domain's php.ini directives",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			out := map[string]any{
				"domain":              dom.Name,
				"memory_limit":        derefStr(dom.PHPMemoryLimit),
				"upload_max_filesize": derefStr(dom.PHPUploadMaxFilesize),
				"post_max_size":       derefStr(dom.PHPPostMaxSize),
				"max_input_vars":      derefInt(dom.PHPMaxInputVars),
				"max_execution_time":  derefInt(dom.PHPMaxExecutionTime),
				"max_input_time":      derefInt(dom.PHPMaxInputTime),
			}
			if jsonOutput {
				return printJSON(out)
			}
			fmt.Printf("%s php.ini directives:\n", dom.Name)
			for _, k := range []string{"memory_limit", "upload_max_filesize", "post_max_size", "max_input_vars", "max_execution_time", "max_input_time"} {
				fmt.Printf("  %-20s %v\n", k, out[k])
			}
			return nil
		},
	}
}

func newDomainPHPSettingsSetCmd() *cobra.Command {
	var (
		memoryLimit  string
		uploadMax    string
		postMax      string
		maxInputVars int
		maxExecTime  int
		maxInputTime int
	)
	cmd := &cobra.Command{
		Use:     "set <domain-name-or-id> [flags]",
		Short:   "Set php.ini directives (only the flags you pass change; reconciler converges)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			var s repository.DomainPHPSettings
			any := false
			f := cmd.Flags()
			if f.Changed("memory-limit") {
				if err := cliValidatePHPSize("memory-limit", memoryLimit); err != nil {
					return err
				}
				s.MemoryLimit = &memoryLimit
				any = true
			}
			if f.Changed("upload-max-filesize") {
				if err := cliValidatePHPSize("upload-max-filesize", uploadMax); err != nil {
					return err
				}
				s.UploadMaxFilesize = &uploadMax
				any = true
			}
			if f.Changed("post-max-size") {
				if err := cliValidatePHPSize("post-max-size", postMax); err != nil {
					return err
				}
				s.PostMaxSize = &postMax
				any = true
			}
			if f.Changed("max-input-vars") {
				if err := cliValidatePHPInt("max-input-vars", maxInputVars); err != nil {
					return err
				}
				s.MaxInputVars = &maxInputVars
				any = true
			}
			if f.Changed("max-execution-time") {
				if err := cliValidatePHPInt("max-execution-time", maxExecTime); err != nil {
					return err
				}
				s.MaxExecutionTime = &maxExecTime
				any = true
			}
			if f.Changed("max-input-time") {
				if err := cliValidatePHPInt("max-input-time", maxInputTime); err != nil {
					return err
				}
				s.MaxInputTime = &maxInputTime
				any = true
			}
			if !any {
				return fmt.Errorf("pass at least one directive flag (see --help)")
			}

			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			if err := domainRepoFromDB().UpdatePHPSettings(ctx, dom.ID, s); err != nil {
				return fmt.Errorf("update php-settings: %w", err)
			}
			fmt.Printf("updated php.ini directives for %s (reconciler will re-render the FPM pool)\n", dom.Name)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&memoryLimit, "memory-limit", "", "memory_limit (e.g. 256M)")
	f.StringVar(&uploadMax, "upload-max-filesize", "", "upload_max_filesize (e.g. 64M)")
	f.StringVar(&postMax, "post-max-size", "", "post_max_size (e.g. 64M)")
	f.IntVar(&maxInputVars, "max-input-vars", 0, "max_input_vars (1..86400)")
	f.IntVar(&maxExecTime, "max-execution-time", 0, "max_execution_time seconds (1..86400)")
	f.IntVar(&maxInputTime, "max-input-time", 0, "max_input_time seconds (1..86400)")
	return cmd
}

func derefInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
