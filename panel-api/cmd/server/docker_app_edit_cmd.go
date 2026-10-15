// `jabali docker-app edit` (Gitea #546, final gap). Domain + port edits for an
// installed Docker app — the one lifecycle control the CLI lacked.
//
// It calls api.DockerAppEditDomainPorts, the SAME gin-free editDomainPorts the
// admin PATCH handler now delegates to. So the CLI inherits, unchanged: the
// stopped-container requirement, port resolution, docker_app-managed domain row
// management, and — critically — the tenant-hardening compose re-render
// (renderInstallCompose + tenant_validate/tenant_caps). The sandbox is NEVER
// stripped because the security-critical path is shared, not reimplemented.
package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newDockerAppEditCmd() *cobra.Command {
	var domain string
	var clearDomain bool
	var portSpecs []string
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a Docker app's domain and/or ports (container must be stopped)",
		Long: "Change the published ports and/or attached domain of an installed app. " +
			"Requires the container be STOPPED (it triggers a full compose re-render + " +
			"redispatch, preserving the tenant sandbox). Resource/update-mode changes " +
			"stay on `docker-app patch`.\n\n" +
			"--port repeatable, comma-separated key=value: name=<catalog-port>," +
			"host=<port|0=auto>,bind=<loopback|public:<managed_ip_id>>,proxy=<true|false>," +
			"enabled=<true|false>. Only name is required.",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("domain") && !clearDomain && len(portSpecs) == 0 {
				return fmt.Errorf("nothing to change: pass --domain, --clear-domain, and/or --port")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 4*time.Minute)
			defer cancel()

			app, err := dockerAppRepoFromDB().FindByID(ctx, args[0])
			if err != nil || app == nil {
				return fmt.Errorf("no docker app with id %q", args[0])
			}
			if app.UserID == nil || *app.UserID == "" {
				return fmt.Errorf("app %s has no owner — cannot attribute a domain row", app.ID)
			}

			var domainPtr *string
			if cmd.Flags().Changed("domain") {
				d := domain
				domainPtr = &d
			} else if clearDomain {
				empty := ""
				domainPtr = &empty
			}

			ports, perr := parsePortSpecs(portSpecs)
			if perr != nil {
				return perr
			}

			cat, cerr := loadDockerCatalogForCLI()
			if cerr != nil {
				return fmt.Errorf("load docker catalog: %w", cerr)
			}
			cfg := cliDockerEditConfig(cat)
			// Operator acts on the app's behalf: attribute any new domain row to
			// the app OWNER, with admin authority so the ownership check passes.
			updated, eerr := api.DockerAppEditDomainPorts(ctx, cfg, app.ID, domainPtr, ports, *app.UserID, true)
			if eerr != nil {
				return fmt.Errorf("edit failed: %w", eerr)
			}
			cliAuditOK(ctx, "docker_app.edit", "docker_app", app.ID, app.UserID)
			if jsonOutput {
				return printJSON(updated)
			}
			fmt.Printf("edited %s (%s): domain/port change re-rendered with sandbox preserved\n", updated.Name, updated.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "attach this domain (empty string detaches)")
	cmd.Flags().BoolVar(&clearDomain, "clear-domain", false, "detach the managed domain")
	cmd.Flags().StringArrayVar(&portSpecs, "port", nil, "port override (repeatable): name=..,host=..,bind=..,proxy=..,enabled=..")
	return cmd
}

// parsePortSpecs parses repeatable --port "k=v,k=v" specs into the exported
// edit type. Only name is required; the rest mirror the GUI port fields.
func parsePortSpecs(specs []string) ([]api.DockerAppPortEdit, error) {
	out := make([]api.DockerAppPortEdit, 0, len(specs))
	for _, spec := range specs {
		var pe api.DockerAppPortEdit
		for _, kv := range strings.Split(spec, ",") {
			kv = strings.TrimSpace(kv)
			if kv == "" {
				continue
			}
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, fmt.Errorf("bad --port segment %q (want key=value)", kv)
			}
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "name":
				pe.Name = v
			case "host":
				n, err := strconv.Atoi(v)
				if err != nil {
					return nil, fmt.Errorf("--port host must be a number: %q", v)
				}
				pe.HostPort = n
			case "bind":
				pe.BindInterface = v
			case "proxy":
				b, err := strconv.ParseBool(v)
				if err != nil {
					return nil, fmt.Errorf("--port proxy must be true|false: %q", v)
				}
				pe.ReverseProxy = &b
			case "enabled":
				b, err := strconv.ParseBool(v)
				if err != nil {
					return nil, fmt.Errorf("--port enabled must be true|false: %q", v)
				}
				pe.Enabled = &b
			default:
				return nil, fmt.Errorf("unknown --port key %q (valid: name,host,bind,proxy,enabled)", k)
			}
		}
		if pe.Name == "" {
			return nil, fmt.Errorf("--port requires name=<catalog-port>")
		}
		out = append(out, pe)
	}
	return out, nil
}

// cliDockerEditConfig wires the edit the way the admin API does, including
// the repositories behind the domain checks (alias, panel hostname and mail
// hostname, ownership state). Without them those checks pass every name.
func cliDockerEditConfig(cat *dockerapp.Catalog) api.DockerAppHandlerConfig {
	return api.DockerAppHandlerConfig{
		Repo:             dockerAppRepoFromDB(),
		Catalog:          cat,
		Domains:          repository.NewDomainRepository(sharedDB),
		DomainOwnership:  repository.NewDomainOwnershipRepository(sharedDB),
		ServerSettings:   repository.NewServerSettingsRepository(sharedDB),
		WebDomainAliases: repository.NewWebDomainAliasRepository(sharedDB),
		Agent:            sharedAgent,
		Users:            userRepo(),
	}
}
