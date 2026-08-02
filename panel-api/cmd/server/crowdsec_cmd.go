// CrowdSec + AppSec operational CLI (Gitea #551). Each verb dispatches the same
// agent command the Security REST handlers use (security_crowdsec.go), so
// validation/reload/apply stay agent-authoritative. Settings-backed knobs
// (geoblock, captcha) persist to server_settings then push to the agent, exactly
// as the handlers do. Mutations are CLI-audited (#537).
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newCrowdsecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crowdsec",
		Short: "CrowdSec + AppSec operations (decisions, allowlists, hub, alerts, geoblock, captcha)",
	}
	cmd.AddCommand(
		csPassthroughCmd("status", "Show CrowdSec engine status", "security.crowdsec.status"),
		csPassthroughCmd("metrics", "Show CrowdSec metrics", "security.crowdsec.metrics"),
		csPassthroughCmd("bouncers", "List CrowdSec bouncers", "security.crowdsec.bouncers.list"),
		newCsDecisionsCmd(),
		newCsAllowlistsCmd(),
		newCsBlocklistsCmd(),
		newCsHubCmd(),
		newCsAlertsCmd(),
		newCsGeoblockCmd(),
		newCsCaptchaCmd(),
		newCsProfilesCmd(),
	)
	return cmd
}

// csCall dispatches an agent verb and prints the raw JSON result.
func csCall(ctx context.Context, verb string, params map[string]any) error {
	raw, err := sharedAgent.Call(ctx, verb, params)
	if err != nil {
		return err
	}
	os.Stdout.Write(raw)
	os.Stdout.Write([]byte{'\n'})
	return nil
}

func csPassthroughCmd(use, short, verb string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Short:   short,
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			return csCall(ctx, verb, nil)
		},
	}
}

// ---- decisions ---------------------------------------------------------------

func newCsDecisionsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "decisions", Short: "List / add / delete CrowdSec decisions"}
	cmd.AddCommand(newCsDecisionsListCmd(), newCsDecisionsAddCmd(), newCsDecisionsDeleteCmd())
	return cmd
}

var validCSScopes = map[string]bool{"ip": true, "range": true, "country": true, "as": true}

func newCsDecisionsListCmd() *cobra.Command {
	var scope string
	var limit int
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List active decisions",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			params := map[string]any{}
			if scope != "" {
				if !validCSScopes[scope] {
					return fmt.Errorf("--scope must be ip|range|country|as")
				}
				params["scope"] = scope
			}
			if limit > 0 {
				if limit > 1000 {
					return fmt.Errorf("--limit must be 1..1000")
				}
				params["limit"] = limit
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			return csCall(ctx, "security.crowdsec.decisions.list", params)
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "filter: ip|range|country|as")
	cmd.Flags().IntVar(&limit, "limit", 0, "max rows (1..1000)")
	return cmd
}

func newCsDecisionsAddCmd() *cobra.Command {
	var scope, value, duration, reason string
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Add a decision (ban)",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if value == "" {
				return fmt.Errorf("--value is required (ip/range/country/as)")
			}
			if scope == "" {
				scope = "ip"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			err := csCall(ctx, "security.crowdsec.decisions.add", map[string]any{
				"scope": scope, "value": value, "duration": duration, "reason": reason,
			})
			if err == nil {
				cliAuditOK(ctx, "crowdsec.decision_add", "crowdsec_decision", value, nil)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "ip", "ip|range|country|as")
	cmd.Flags().StringVar(&value, "value", "", "target value (required)")
	cmd.Flags().StringVar(&duration, "duration", "4h", "ban duration (e.g. 4h, 24h)")
	cmd.Flags().StringVar(&reason, "reason", "manual (cli)", "reason label")
	return cmd
}

func newCsDecisionsDeleteCmd() *cobra.Command {
	var ip string
	cmd := &cobra.Command{
		Use:     "delete [<id>]",
		Short:   "Delete a decision by id, or every decision for --ip",
		Args:    cobra.MaximumNArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			var params map[string]any
			var subj string
			switch {
			case ip != "":
				params = map[string]any{"ip": ip}
				subj = ip
			case len(args) == 1:
				id, err := strconv.Atoi(args[0])
				if err != nil || id < 1 {
					return fmt.Errorf("decision id must be a positive integer")
				}
				params = map[string]any{"id": id}
				subj = args[0]
			default:
				return fmt.Errorf("provide a decision <id> or --ip <ip>")
			}
			err := csCall(ctx, "security.crowdsec.decisions.delete", params)
			if err == nil {
				cliAuditOK(ctx, "crowdsec.decision_delete", "crowdsec_decision", subj, nil)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&ip, "ip", "", "delete all decisions targeting this IP/CIDR")
	return cmd
}

// ---- allowlists --------------------------------------------------------------

func newCsAllowlistsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "allowlists", Short: "List / add / remove allowlist entries"}
	cmd.AddCommand(
		csPassthroughCmd("list", "List allowlist entries", "security.crowdsec.allowlists.list"),
		func() *cobra.Command {
			var value, reason string
			var me bool
			c := &cobra.Command{
				Use: "add", Short: "Add an allowlist entry (use --me to allowlist your current SSH IP)", Args: cobra.NoArgs, PreRunE: requireDBAndAgent,
				RunE: func(cmd *cobra.Command, _ []string) error {
					// GH #330: --me allowlists the IP you're SSH'd in from — the
					// recovery path for a roaming admin locked out by CrowdSec
					// before they can reach the panel login (so the M43 login
					// auto-allowlist never fires). No need to know/type your IP.
					if me {
						ip := sshClientIP()
						if ip == "" {
							return fmt.Errorf("--me: could not determine your SSH client IP ($SSH_CLIENT/$SSH_CONNECTION unset — pass --value instead)")
						}
						value = ip
						if reason == "manual (cli)" {
							reason = "roaming admin (cli --me)"
						}
					}
					if value == "" {
						return fmt.Errorf("--value (or --me) is required")
					}
					ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
					defer cancel()
					if err := csCall(ctx, "security.crowdsec.allowlists.add", map[string]any{"value": value, "reason": reason}); err != nil {
						return err
					}
					cliAuditOK(ctx, "crowdsec.allowlist_add", "crowdsec_allowlist", value, nil)
					// Immediately clear any active ban on this IP so the admin is
					// unblocked NOW, not just protected on the next eval.
					if err := csCall(ctx, "security.crowdsec.decisions.delete", map[string]any{"ip": value}); err == nil {
						cliAuditOK(ctx, "crowdsec.decision_delete", "crowdsec_decision", value, nil)
						fmt.Printf("allowlisted %s and cleared any active ban on it\n", value)
					} else {
						fmt.Printf("allowlisted %s (no active ban to clear, or clear failed)\n", value)
					}
					return nil
				},
			}
			c.Flags().StringVar(&value, "value", "", "IP/CIDR to allowlist (required unless --me)")
			c.Flags().StringVar(&reason, "reason", "manual (cli)", "reason label")
			c.Flags().BoolVar(&me, "me", false, "allowlist the IP you are SSH'd in from (roaming-admin recovery)")
			return c
		}(),
		func() *cobra.Command {
			var value string
			c := &cobra.Command{
				Use: "remove", Short: "Remove an allowlist entry", Args: cobra.NoArgs, PreRunE: requireDBAndAgent,
				RunE: func(cmd *cobra.Command, _ []string) error {
					if value == "" {
						return fmt.Errorf("--value is required")
					}
					ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
					defer cancel()
					err := csCall(ctx, "security.crowdsec.allowlists.remove", map[string]any{"value": value})
					if err == nil {
						cliAuditOK(ctx, "crowdsec.allowlist_remove", "crowdsec_allowlist", value, nil)
					}
					return err
				},
			}
			c.Flags().StringVar(&value, "value", "", "allowlist entry to remove (required)")
			return c
		}(),
	)
	return cmd
}

// ---- blocklists --------------------------------------------------------------

func newCsBlocklistsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "blocklists", Short: "List / refresh blocklists"}
	cmd.AddCommand(
		csPassthroughCmd("list", "List subscribed blocklists", "security.crowdsec.blocklists.list"),
		func() *cobra.Command {
			c := csPassthroughCmd("refresh", "Refresh blocklists now", "security.crowdsec.blocklists.refresh")
			return c
		}(),
	)
	return cmd
}

// ---- hub ---------------------------------------------------------------------

func newCsHubCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "hub", Short: "List / install / remove hub items"}
	var listType string
	listCmd := &cobra.Command{
		Use: "list", Short: "List hub items (collections/parsers/scenarios/appsec-rules)", Args: cobra.NoArgs, PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			params := map[string]any{}
			if listType != "" {
				params["type"] = listType
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			return csCall(ctx, "security.crowdsec.hub.list", params)
		},
	}
	listCmd.Flags().StringVar(&listType, "type", "", "filter by type")

	var iType, iName string
	var iForce bool
	installCmd := &cobra.Command{
		Use: "install", Short: "Install a hub item", Args: cobra.NoArgs, PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if iType == "" || iName == "" {
				return fmt.Errorf("--type and --name are required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			err := csCall(ctx, "security.crowdsec.hub.install", map[string]any{"type": iType, "name": iName, "force": iForce})
			if err == nil {
				cliAuditOK(ctx, "crowdsec.hub_install", "crowdsec_hub", iType+"/"+iName, nil)
			}
			return err
		},
	}
	installCmd.Flags().StringVar(&iType, "type", "", "collections|parsers|scenarios|appsec-rules (required)")
	installCmd.Flags().StringVar(&iName, "name", "", "hub item name (required)")
	installCmd.Flags().BoolVar(&iForce, "force", false, "force reinstall")

	var rType, rName string
	removeCmd := &cobra.Command{
		Use: "remove", Short: "Remove a hub item", Args: cobra.NoArgs, PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rType == "" || rName == "" {
				return fmt.Errorf("--type and --name are required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			err := csCall(ctx, "security.crowdsec.hub.remove", map[string]any{"type": rType, "name": rName})
			if err == nil {
				cliAuditOK(ctx, "crowdsec.hub_remove", "crowdsec_hub", rType+"/"+rName, nil)
			}
			return err
		},
	}
	removeCmd.Flags().StringVar(&rType, "type", "", "item type (required)")
	removeCmd.Flags().StringVar(&rName, "name", "", "item name (required)")

	cmd.AddCommand(listCmd, installCmd, removeCmd)
	return cmd
}

// ---- alerts ------------------------------------------------------------------

func newCsAlertsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "alerts", Short: "List / inspect alerts"}
	cmd.AddCommand(
		csPassthroughCmd("list", "List recent alerts", "security.crowdsec.alerts.list"),
		&cobra.Command{
			Use: "inspect <id>", Short: "Inspect one alert", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				defer cancel()
				return csCall(ctx, "security.crowdsec.alerts.inspect", map[string]any{"id": args[0]})
			},
		},
	)
	return cmd
}

// ---- geoblock (AppSec) -------------------------------------------------------

var cliCountryRE = regexp.MustCompile(`^[A-Z]{2}$`)

func newCsGeoblockCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "geoblock", Short: "AppSec geoblock get / set"}
	cmd.AddCommand(
		&cobra.Command{
			Use: "get", Short: "Show geoblock mode + countries", Args: cobra.NoArgs, PreRunE: requireDB,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
				defer cancel()
				s, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx)
				if err != nil {
					return err
				}
				countries := []string{}
				if s.AppSecGeoblockCountries != "" {
					countries = strings.Split(s.AppSecGeoblockCountries, ",")
				}
				if jsonOutput {
					return printJSON(map[string]any{"mode": s.AppSecGeoblockMode, "countries": countries})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "mode=%s countries=%s\n", s.AppSecGeoblockMode, strings.Join(countries, ","))
				return nil
			},
		},
		newCsGeoblockSetCmd(),
	)
	return cmd
}

func newCsGeoblockSetCmd() *cobra.Command {
	var mode string
	var countries []string
	cmd := &cobra.Command{
		Use:     "set",
		Short:   "Set geoblock mode (off|allow|deny) and countries",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mode != "off" && mode != "allow" && mode != "deny" {
				return fmt.Errorf("--mode must be off|allow|deny")
			}
			cleaned := []string{}
			seen := map[string]bool{}
			for _, c := range countries {
				code := strings.ToUpper(strings.TrimSpace(c))
				if code == "" {
					continue
				}
				if !cliCountryRE.MatchString(code) {
					return fmt.Errorf("invalid country code %q (expected 2-letter ISO)", c)
				}
				if !seen[code] {
					seen[code] = true
					cleaned = append(cleaned, code)
				}
			}
			if (mode == "allow" || mode == "deny") && len(cleaned) == 0 {
				return fmt.Errorf("mode %s requires at least one --country", mode)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			// Agent first so a YAML-rewrite failure doesn't drift the DB.
			if _, err := sharedAgent.Call(ctx, "security.crowdsec.appsec.geoblock.set", map[string]any{
				"mode": mode, "countries": cleaned,
			}); err != nil {
				return err
			}
			repo := repository.NewServerSettingsRepository(sharedDB)
			s, err := repo.Get(ctx)
			if err != nil {
				return err
			}
			s.AppSecGeoblockMode = mode
			s.AppSecGeoblockCountries = strings.Join(cleaned, ",")
			if err := repo.Upsert(ctx, s); err != nil {
				return fmt.Errorf("persist geoblock settings: %w", err)
			}
			cliAuditOK(ctx, "crowdsec.geoblock_set", "server_settings", "1", nil)
			fmt.Fprintf(cmd.OutOrStdout(), "geoblock mode=%s countries=%s\n", mode, strings.Join(cleaned, ","))
			return nil
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "", "off|allow|deny (required)")
	cmd.Flags().StringArrayVar(&countries, "country", nil, "2-letter ISO code (repeatable)")
	return cmd
}

// ---- captcha -----------------------------------------------------------------

func newCsCaptchaCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "captcha", Short: "Captcha remediation get / set"}
	cmd.AddCommand(
		&cobra.Command{
			Use: "get", Short: "Show captcha config (secret never shown)", Args: cobra.NoArgs, PreRunE: requireDB,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
				defer cancel()
				s, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx)
				if err != nil {
					return err
				}
				if jsonOutput {
					return printJSON(map[string]any{"enabled": s.CrowdSecCaptchaEnabled, "provider": s.CrowdSecCaptchaProvider, "site_key": s.CrowdSecCaptchaSiteKey})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "enabled=%v provider=%s site_key=%s\n", s.CrowdSecCaptchaEnabled, s.CrowdSecCaptchaProvider, s.CrowdSecCaptchaSiteKey)
				return nil
			},
		},
		newCsCaptchaSetCmd(),
	)
	return cmd
}

func newCsCaptchaSetCmd() *cobra.Command {
	var (
		enabled   bool
		provider  string
		siteKey   string
		secretKey string
	)
	cmd := &cobra.Command{
		Use:     "set",
		Short:   "Configure captcha remediation (merge: empty --secret-key keeps existing)",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			repo := repository.NewServerSettingsRepository(sharedDB)
			s, err := repo.Get(ctx)
			if err != nil {
				return err
			}
			s.CrowdSecCaptchaEnabled = enabled
			if cmd.Flags().Changed("provider") {
				s.CrowdSecCaptchaProvider = strings.TrimSpace(provider)
			}
			if cmd.Flags().Changed("site-key") {
				s.CrowdSecCaptchaSiteKey = strings.TrimSpace(siteKey)
			}
			if v := strings.TrimSpace(secretKey); v != "" {
				s.CrowdSecCaptchaSecretKey = v
			}
			if enabled && s.CrowdSecCaptchaSecretKey == "" {
				return fmt.Errorf("--secret-key is required when enabling captcha")
			}
			if err := repo.Upsert(ctx, s); err != nil {
				return fmt.Errorf("persist captcha settings: %w", err)
			}
			if _, err := sharedAgent.Call(ctx, "security.crowdsec.captcha.apply", map[string]any{
				"enabled": s.CrowdSecCaptchaEnabled, "provider": s.CrowdSecCaptchaProvider,
				"site_key": s.CrowdSecCaptchaSiteKey, "secret_key": s.CrowdSecCaptchaSecretKey,
			}); err != nil {
				return err
			}
			cliAuditOK(ctx, "crowdsec.captcha_set", "server_settings", "1", nil)
			fmt.Fprintf(cmd.OutOrStdout(), "captcha enabled=%v provider=%s\n", s.CrowdSecCaptchaEnabled, s.CrowdSecCaptchaProvider)
			return nil
		},
	}
	cmd.Flags().BoolVar(&enabled, "enabled", false, "enable captcha remediation")
	cmd.Flags().StringVar(&provider, "provider", "", "recaptcha|hcaptcha|turnstile")
	cmd.Flags().StringVar(&siteKey, "site-key", "", "captcha site key")
	cmd.Flags().StringVar(&secretKey, "secret-key", "", "captcha secret key (write-only; empty keeps existing)")
	return cmd
}

// ---- profiles (per-scenario remediation override) ----------------------------

func newCsProfilesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "profiles", Short: "Per-scenario remediation overrides get / set"}
	cmd.AddCommand(
		csPassthroughCmd("get", "Show current profile overrides", "security.crowdsec.profiles.get"),
		newCsProfilesSetCmd(),
	)
	return cmd
}

func newCsProfilesSetCmd() *cobra.Command {
	var overrides []string
	cmd := &cobra.Command{
		Use:     "set",
		Short:   "Set per-scenario overrides as scenario:action (action=captcha|off)",
		Args:    cobra.NoArgs,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(overrides) == 0 {
				return fmt.Errorf("at least one --override scenario:action is required")
			}
			list := make([]map[string]string, 0, len(overrides))
			for _, o := range overrides {
				scen, action, ok := strings.Cut(o, ":")
				if !ok || scen == "" {
					return fmt.Errorf("--override %q must be scenario:action", o)
				}
				if action != "captcha" && action != "off" {
					return fmt.Errorf("override action must be captcha|off (got %q)", action)
				}
				list = append(list, map[string]string{"scenario": scen, "action": action})
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			err := csCall(ctx, "security.crowdsec.profiles.set", map[string]any{"overrides": list})
			if err == nil {
				cliAuditOK(ctx, "crowdsec.profiles_set", "crowdsec_profile", "overrides", nil)
			}
			return err
		},
	}
	cmd.Flags().StringArrayVar(&overrides, "override", nil, "scenario:action (repeatable; action=captcha|off)")
	return cmd
}

// sshClientIP returns the source IP of the current SSH session from
// $SSH_CLIENT (first field) or $SSH_CONNECTION (first field), or "" if unset.
func sshClientIP() string {
	for _, env := range []string{"SSH_CLIENT", "SSH_CONNECTION"} {
		if v := os.Getenv(env); v != "" {
			if f := strings.Fields(v); len(f) > 0 && net.ParseIP(f[0]) != nil {
				return f[0]
			}
		}
	}
	return ""
}
