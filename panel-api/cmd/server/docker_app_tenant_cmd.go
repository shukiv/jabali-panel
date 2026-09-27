// Tenant-scoped Docker app install from the CLI (Gitea #547). `docker-app
// install --user <id|username> --domain <name>` reproduces the tenant install
// path's policy gates (docker_apps_user.go): tenant-installable validation,
// hosting-package allowlist + MaxDockerApps/disk quota, domain ownership,
// per-package CPU/mem/PID clamps, loopback+reverse-proxy domain attach, and
// TenantHardening (cap_drop ALL + owner cgroup slice). Produces the same
// app+domain rows the GUI does, so the install is visible/manageable there.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/oklog/ulid/v2"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// cliTenantInstallable mirrors api.tenantInstallable: flag set AND no
// default-public port (loopback-only rule).
func cliTenantInstallable(e dockerapp.Entry) bool {
	if !e.TenantInstallable {
		return false
	}
	for _, p := range e.Ports {
		if p.DefaultBind == "public" {
			return false
		}
	}
	return true
}

// cliTenantAllowSet mirrors api.tenantAllowSet: the package's docker_app_slugs
// allowlist when set, else the server-wide docker_tenant_apps curation. all=true
// means every eligible app.
func cliTenantAllowSet(ctx context.Context, pkg *models.HostingPackage) (map[string]bool, bool) {
	if pkg != nil && strings.TrimSpace(pkg.DockerAppSlugs) != "" {
		return csvSet(pkg.DockerAppSlugs), false
	}
	ss, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx)
	if err != nil || ss == nil || strings.TrimSpace(ss.DockerTenantApps) == "" {
		return nil, true
	}
	return csvSet(ss.DockerTenantApps), false
}

func csvSet(csv string) map[string]bool {
	set := map[string]bool{}
	for _, s := range strings.Split(csv, ",") {
		if v := strings.TrimSpace(s); v != "" {
			set[v] = true
		}
	}
	return set
}

func tenantInstanceSlugCLI(slug, userID, name string) string {
	sum := sha256.Sum256([]byte(userID))
	short := hex.EncodeToString(sum[:])[:10]
	return strings.ToLower(slug + "-" + short + "-" + name)
}

// --- per-package clamp helpers (mirror docker_apps_user.go) -------------------

func parseDockerSizeCLI(s string) (int64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "gb"), strings.HasSuffix(s, "g"):
		mult = 1024 * 1024 * 1024
		s = strings.TrimRight(s, "gb")
	case strings.HasSuffix(s, "mb"), strings.HasSuffix(s, "m"):
		mult = 1024 * 1024
		s = strings.TrimRight(s, "mb")
	case strings.HasSuffix(s, "kb"), strings.HasSuffix(s, "k"):
		mult = 1024
		s = strings.TrimRight(s, "kb")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimRight(s, "b")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return int64(n * float64(mult)), true
}

func clampMemCLI(s string, capMB uint32) string {
	if capMB == 0 {
		return s
	}
	b, ok := parseDockerSizeCLI(s)
	if !ok {
		return s
	}
	if b > int64(capMB)*1024*1024 {
		return strconv.Itoa(int(capMB)) + "m"
	}
	return s
}

func clampCPUCLI(s string, capPct uint32) string {
	if capPct == 0 {
		return s
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return s
	}
	if capCores := float64(capPct) / 100.0; f > capCores {
		return strconv.FormatFloat(capCores, 'f', -1, 64)
	}
	return s
}

func clampPIDsCLI(p int, capTasks uint32) int {
	if capTasks == 0 {
		return p
	}
	if p <= 0 || p > int(capTasks) {
		return int(capTasks)
	}
	return p
}

// resolveTenantUser resolves --user by ULID then by username.
func resolveTenantUser(ctx context.Context, ref string) (*models.User, error) {
	repo := repository.NewUserRepository(sharedDB)
	if u, err := repo.FindByID(ctx, ref); err == nil && u != nil {
		return u, nil
	}
	u, err := repo.FindByUsername(ctx, ref)
	if err != nil || u == nil {
		return nil, fmt.Errorf("no user with id or username %q", ref)
	}
	return u, nil
}

// runTenantDockerInstall installs a tenant-owned Docker app under the policy
// gates from docker_apps_user.go.
func runTenantDockerInstall(ctx context.Context, slug, name, userRef, domain, updateMode string, cpuFlag, memFlag string, pidsFlag int, envPairs []string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid --name %q (must match ^[a-z0-9-]{1,32}$)", name)
	}
	if strings.TrimSpace(domain) == "" {
		return fmt.Errorf("--domain is required for a tenant install (the app attaches to a domain the user owns)")
	}
	if updateMode == "" {
		updateMode = models.DockerAppUpdateModeManual
	}

	cat, err := loadDockerCatalogForCLI()
	if err != nil {
		return err
	}
	entry, ok := cat.Get(slug)
	if !ok {
		return fmt.Errorf("slug %q not in catalog", slug)
	}
	if !cliTenantInstallable(entry) {
		return fmt.Errorf("%q is not tenant-installable (flag off or declares a public port)", slug)
	}

	user, err := resolveTenantUser(ctx, userRef)
	if err != nil {
		return err
	}
	if user.Username == nil || *user.Username == "" {
		return fmt.Errorf("user %s has no Linux account; tenant Docker requires one", user.ID)
	}
	if user.PackageID == nil {
		return fmt.Errorf("tenant Docker apps require a hosting package that includes them")
	}
	pkg, err := repository.NewPackageRepository(sharedDB).FindByID(ctx, *user.PackageID)
	if err != nil || pkg == nil {
		return fmt.Errorf("resolve hosting package: %w", err)
	}
	if pkg.MaxDockerApps == 0 {
		return fmt.Errorf("the user's hosting package does not include Docker apps")
	}

	allowSet, allowAll := cliTenantAllowSet(ctx, pkg)
	if !(allowAll || allowSet[slug]) {
		return fmt.Errorf("%q is not installable on the user's hosting package allowlist", slug)
	}

	repo := dockerAppRepoFromDB()
	count, err := repo.CountByUserID(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("quota check: %w", err)
	}
	if count >= int64(pkg.MaxDockerApps) {
		return fmt.Errorf("user has reached the Docker app limit (%d)", pkg.MaxDockerApps)
	}
	if pkg.DiskQuotaMB > 0 {
		used, derr := repo.SumDataBytesByUserID(ctx, user.ID)
		if derr != nil {
			return fmt.Errorf("disk check: %w", derr)
		}
		if used >= int64(pkg.DiskQuotaMB)*1024*1024 {
			return fmt.Errorf("user's Docker app disk usage has reached the package quota")
		}
	}

	// Domain ownership: must be unowned or owned by this user (criterion #3).
	domRepo := domainRepoFromDB()
	if dom, _ := domRepo.FindByName(ctx, domain); dom != nil && dom.UserID != user.ID {
		return fmt.Errorf("domain %q belongs to another user", domain)
	}

	username := *user.Username
	instanceSlug := tenantInstanceSlugCLI(slug, user.ID, name)

	mem := strings.TrimSpace(memFlag)
	if mem == "" {
		mem = entry.Resources.Memory
	}
	cpu := strings.TrimSpace(cpuFlag)
	if cpu == "" {
		cpu = entry.Resources.CPU
	}
	pids := entry.Resources.PIDs
	if pidsFlag > 0 {
		pids = pidsFlag
	}
	mem = clampMemCLI(mem, pkg.MemoryLimitMB)
	cpu = clampCPUCLI(cpu, pkg.CPUQuotaPercent)
	pids = clampPIDsCLI(pids, pkg.MaxTasks)

	app := &models.DockerApp{
		ID:             ulid.Make().String(),
		UserID:         &user.ID,
		Slug:           slug,
		InstanceSlug:   instanceSlug,
		Name:           name,
		CatalogVersion: entry.Version,
		Status:         models.DockerAppStatusPending,
		UpdateMode:     updateMode,
	}
	if cpu != "" {
		app.CPULimit = &cpu
	}
	if mem != "" {
		app.MemoryLimit = &mem
	}
	if pids > 0 {
		app.PIDsLimit = &pids
	}
	if err := repo.Create(ctx, app); err != nil {
		return fmt.Errorf("persist docker_apps row: %w", err)
	}

	// Catalog-default loopback ports.
	runtime := make(map[string]dockerapp.RuntimePort, len(entry.Ports))
	var loopbackPort *models.DockerAppPublishedPort
	for _, p := range entry.Ports {
		if !p.DefaultEnabled {
			continue
		}
		bind := p.DefaultBind
		if bind == "" {
			bind = "loopback"
		}
		host, herr := repo.FindFreeHostPort(ctx, bind, p.Protocol)
		if herr != nil {
			_ = repo.Delete(ctx, app.ID)
			return fmt.Errorf("allocate host port for %q: %w", p.Name, herr)
		}
		row := &models.DockerAppPublishedPort{
			ID: ulid.Make().String(), AppID: app.ID, PortName: p.Name,
			ContainerPort: p.ContainerPort, BindInterface: bind, HostPort: host,
			Protocol: p.Protocol, ReverseProxy: p.DefaultReverseProxy, Enabled: true,
		}
		if perr := repo.CreatePort(ctx, row); perr != nil {
			_ = repo.Delete(ctx, app.ID)
			return fmt.Errorf("persist port row %q: %w", p.Name, perr)
		}
		runtime[p.Name] = dockerapp.RuntimePort{HostPort: host, ContainerPort: p.ContainerPort, BindInterface: "127.0.0.1", Protocol: p.Protocol}
		if row.ReverseProxy && row.Protocol == "tcp" && loopbackPort == nil {
			loopbackPort = row
		}
	}

	// Attach / create the domain under the user's ownership.
	if loopbackPort != nil {
		truePtr := true
		rules := models.NginxRules{{Type: "proxy_pass", Path: "/", Target: "http://127.0.0.1:" + strconv.Itoa(loopbackPort.HostPort), Websocket: &truePtr}}
		if existing, _ := domRepo.FindByName(ctx, domain); existing != nil {
			if aerr := domRepo.AttachDockerApp(ctx, existing.ID, app.ID, rules); aerr != nil {
				_ = repo.Delete(ctx, app.ID)
				return fmt.Errorf("attach domain: %w", aerr)
			}
		} else {
			dom := &models.Domain{
				ID: ulid.Make().String(), UserID: user.ID, Name: domain,
				IsEnabled: true, SSLEnabled: true, NginxRules: rules,
				ManagedBy: models.DomainManagedByDockerApp, DockerAppID: &app.ID,
			}
			if derr := domRepo.Create(ctx, dom); derr != nil {
				_ = repo.Delete(ctx, app.ID)
				return fmt.Errorf("create domain: %w", derr)
			}
		}
	}

	// Env overrides (validated like the env-edit path).
	envOverride := map[string]string{}
	for _, kv := range envPairs {
		k, v, okk := strings.Cut(kv, "=")
		if !okk || k == "" {
			_ = repo.Delete(ctx, app.ID)
			return fmt.Errorf("--env %q must be KEY=VALUE", kv)
		}
		if msg := validateEnvKVCLI(k, v); msg != "" {
			_ = repo.Delete(ctx, app.ID)
			return fmt.Errorf("invalid env: %s", msg)
		}
		envOverride[k] = v
	}
	envMap, err := dockerapp.MaterialiseEnv(entry, envOverride)
	if err != nil {
		_ = repo.Delete(ctx, app.ID)
		return fmt.Errorf("materialise env: %w", err)
	}

	composeYML, tenantServices, err := dockerapp.RenderTenant(entry, dockerapp.RenderParams{
		Slug: instanceSlug, Name: name, Domain: domain, ImageChannel: entry.ImageChannel,
		DataRoot: "/var/lib/jabali/docker-apps/" + instanceSlug,
		CPULimit: cpu, MemoryLimit: mem, PIDsLimit: pids, Ports: runtime, Env: envMap,
		TenantHardening: &dockerapp.TenantHardening{
			CgroupParent: "jabali-user-" + username + ".slice",
			Caps:         dockerapp.TenantCapAllowlist(entry.TenantCaps),
			PIDsLimit:    pids,
		},
	})
	if err != nil {
		_ = repo.Delete(ctx, app.ID)
		return fmt.Errorf("render compose: %w", err)
	}

	volumeNames := make([]string, 0, len(entry.Volumes))
	for _, v := range entry.Volumes {
		volumeNames = append(volumeNames, v.Name)
	}
	_ = repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusInstalling, nil)
	if _, err := sharedAgent.Call(ctx, "docker_app.install", map[string]any{
		"slug":                        instanceSlug,
		"compose_yml":                 composeYML,
		"env_file":                    buildEnvFileForCLI(envMap),
		"volumes":                     volumeNames,
		"volume_owner":                entry.VolumeOwner,
		"wait_healthy":                true,
		"healthcheck_timeout_seconds": 300,
		"tenant_validate":             true,
		"tenant_caps":                 dockerapp.TenantCapAllowlist(entry.TenantCaps),
		"tenant_cgroup":               "jabali-user-" + username + ".slice",
		"tenant_services":             tenantServices,
	}); err != nil {
		msg := firstLine(err.Error())
		_ = repo.UpdateStatus(context.Background(), app.ID, models.DockerAppStatusFailed, &msg)
		return fmt.Errorf("agent install: %s", msg)
	}
	_ = repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusRunning, nil)
	cliAuditOK(ctx, "docker_app.install", "docker_app", app.ID, &user.ID)
	fmt.Printf("ok: installed tenant app %s (%s) for %s on %s status=running\n", name, app.ID, username, domain)
	return nil
}
