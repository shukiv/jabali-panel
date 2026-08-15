package app

import (
	"flag"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/apps"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/reconciler"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

var updateGolden = flag.Bool("update", false, "rewrite the undocumented-routes golden")

const (
	openapiPath = "../api/openapi.yaml"
	goldenPath  = "openapi_undocumented.golden"
	apiPrefix   = "/api/v1"
)

// fullDeps builds a maximal Deps so NewWithDeps mounts EVERY dep-gated route.
// Registration never dereferences a dep (handlers are closures), so a stub
// *gorm.DB is safe — the repos just need to be non-nil to pass the != nil gates.
func fullDeps() Deps {
	db := &gorm.DB{}
	return Deps{
		Users: repository.NewUserRepository(db),
		Packages: repository.NewPackageRepository(db),
		Domains: repository.NewDomainRepository(db),
		Databases: repository.NewDatabaseRepository(db),
		DatabaseUsers: repository.NewDatabaseUserRepository(db),
		DatabaseUserGrants: repository.NewDatabaseUserGrantRepository(db),
		DBAdmin: repository.NewDBAdminRepository(db),
		Mailboxes: repository.NewMailboxRepository(db),
		MailGroups: repository.NewMailGroupRepository(db),
		MailboxSSOTokens: repository.NewMailboxSSOTokenRepository(db),
		PhpMyAdminSSOTokens: repository.NewPhpMyAdminSSOTokenRepository(db),
		AdminerSSOTokens: repository.NewAdminerSSOTokenRepository(db),
		LogAccessStreams: repository.NewLogAccessStreamRepository(db),
		TerminalSessions: repository.NewTerminalSessionRepository(db),
		ServerSettings: repository.NewServerSettingsRepository(db),
		PageTemplates: repository.NewPageTemplateRepository(db),
		AccountSkeleton: repository.NewAccountSkeletonRepository(db),
		NotificationEventSettings: repository.NewNotificationEventSettingRepository(db),
		DNSZones: repository.NewDNSZoneRepository(db),
		DNSRecords: repository.NewDNSRecordRepository(db),
		SSLCerts: repository.NewSSLCertificateRepository(db),
		MailRBLStates: repository.NewMailRBLStateRepository(db),
		DMARCAggregate: repository.NewDMARCAggregateRepository(db),
		TLSRPTAggregate: repository.NewTLSRPTAggregateRepository(db),
		ARFReports: repository.NewARFReportRepository(db),
		MailOutboundPolicies: repository.NewMailOutboundPolicyRepository(db),
		BWDaily: repository.NewBWDailyRepository(db),
		DomainIPACLs: repository.NewDomainIPACLRepository(db),
		MailCerts: repository.NewMailCertificateRepository(db),
		DomainDirectoryPrivacy: repository.NewDomainDirectoryPrivacyRepository(db),
		MigrationJobs: repository.NewMigrationJobRepository(db),
		MigrationSizeCache: repository.NewMigrationAccountSizeCacheRepository(db),
		AutomationTokens: repository.NewAutomationTokenRepository(db),
		UserAPITokens: repository.NewUserAPITokenRepository(db),
		PHPPools: repository.NewPHPPoolRepository(db),
		PHPPoolIniOverrides: repository.NewPHPPoolIniOverrideRepository(db),
		PHPPerformanceModes: repository.NewPHPPerformanceModeRepository(db),
		WordPressInstalls: repository.NewWordPressInstallRepository(db),
		ManagedIPs: repository.NewManagedIPRepository(db),
		CronJobs: repository.NewCronJobRepository(db),
		DockerApps: repository.NewDockerAppRepository(db),
		PythonApps: repository.NewPythonAppRepository(db),
		SSHKeys: repository.NewSSHKeyRepository(db),
		FtpAccounts: repository.NewFtpAccountRepository(db),
		LimitOverrides: repository.NewUserLimitOverrideRepository(db),
		Autoresponders: repository.NewEmailAutoresponderRepository(db),
		Forwarders: repository.NewEmailForwarderRepository(db),
		MailboxShares: repository.NewMailboxShareRepository(db),
		DNSSECKeys: repository.NewDNSSECKeyRepository(db),
		PanelCerts: repository.NewPanelCertificateRepository(db),
		UpdateState: repository.NewUpdateStateRepository(db),
		UpdateHistory: repository.NewUpdateHistoryRepository(db),
		UpdateAutoupdate: repository.NewUpdateAutoupdateConfigRepository(db),
		BackupJobs: repository.NewBackupJobRepository(db),
		BackupDestinations: repository.NewBackupDestinationRepository(db),
		BackupSchedules: repository.NewBackupScheduleRepository(db),
		MalwareQuarantine: repository.NewMalwareQuarantineRepository(db),
		MalwareEvents: repository.NewMalwareEventRepository(db),
		YARARules: repository.NewYARACustomRuleRepository(db),
		MalwareSettings: repository.NewMalwareSettingsRepository(db),
		MailScanState: repository.NewMailScanStateRepository(db),
		MailScanFailures: repository.NewMailScanFailureRepository(db),
		MalwareUserScans: repository.NewMalwareUserScanRepository(db),
		UserEgressPolicies: repository.NewUserEgressPolicyRepository(db),
		UserEgressRequests: repository.NewUserEgressRequestRepository(db),
		UserEgressDropSamples: repository.NewUserEgressDropSampleRepository(db),
		NotificationChannels: repository.NewNotificationChannelRepository(db),
		UserNotificationRoutes: repository.NewUserNotificationRouteRepository(db),
		NotificationHistory: repository.NewNotificationHistoryRepository(db),
		WebhookEndpoints: repository.NewWebhookEndpointRepository(db),
		WebPushSubs: repository.NewWebPushSubscriptionRepository(db),
		Snuffleupagus: repository.NewSnuffleupagusRepository(db),
		DB:         db,
		Agent:      agent.NewMockClient(),
		Reconciler: &reconciler.Reconciler{},
		Apps:       apps.New(),
		QuotaMount: "/home",
		Log:        slog.Default(),
		Redis:      redis.NewClient(&redis.Options{}),
	}
}

// httpMethods are the OpenAPI-documentable verbs.
var httpMethods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}

// normalizeRoute turns a gin route (/api/v1/domains/:id) into an OpenAPI path
// key relative to the server base (/domains/{id}), or "" if it's not part of
// the documentable REST surface (meta/health/static/ory).
func normalizeRoute(method, ginPath string) (verb, path string) {
	m := strings.ToLower(method)
	if !httpMethods[m] {
		return "", ""
	}
	if !strings.HasPrefix(ginPath, apiPrefix+"/") {
		return "", "" // /healthz, /metrics, static, etc. — not under /api/v1
	}
	p := strings.TrimPrefix(ginPath, apiPrefix)
	// Exclude the spec/meta + auth-proxy surfaces that aren't part of the contract.
	for _, ex := range []string{"/_meta", "/.ory"} {
		if strings.HasPrefix(p, ex) {
			return "", ""
		}
	}
	// gin :param / *wild → OpenAPI {param}.
	var b strings.Builder
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			continue
		}
		b.WriteByte('/')
		if seg[0] == ':' || seg[0] == '*' {
			b.WriteByte('{')
			b.WriteString(seg[1:])
			b.WriteByte('}')
		} else {
			b.WriteString(seg)
		}
	}
	np := b.String()
	if np == "" {
		np = "/"
	}
	return m, np
}

// documentedRoutes parses openapi.yaml into the set of "verb path" it documents.
func documentedRoutes(t *testing.T) map[string]bool {
	raw, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	out := map[string]bool{}
	for path, ops := range spec.Paths {
		for verb := range ops {
			if httpMethods[strings.ToLower(verb)] {
				out[strings.ToLower(verb)+" "+path] = true
			}
		}
	}
	return out
}

// TestOpenAPICoverage is a ratchet: it lists the registered REST routes that are
// NOT yet in openapi.yaml and pins that set as a golden file. A NEW undocumented
// route (not in the golden) fails the test — document it or, deliberately, add it
// to the golden with `-update`. Documenting a route (removing it from the gap)
// also fails until the golden is regenerated, so the debt only shrinks. This is
// the JAB-160 drift guard; the golden is the visible, checked-in coverage gap.
func TestOpenAPICoverage(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Kratos.PublicURL = "http://127.0.0.1:4433"
	cfg.Auth.Kratos.AdminURL = "http://127.0.0.1:4434"
	r := NewWithDeps(cfg, fullDeps())

	documented := documentedRoutes(t)
	var undocumented []string
	seen := map[string]bool{}
	for _, rt := range r.Routes() {
		verb, path := normalizeRoute(rt.Method, rt.Path)
		if verb == "" {
			continue
		}
		key := verb + " " + path
		if documented[key] || seen[key] {
			continue
		}
		seen[key] = true
		undocumented = append(undocumented, strings.ToUpper(verb)+" "+path)
	}
	sort.Strings(undocumented)
	got := strings.Join(undocumented, "\n") + "\n"

	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %d undocumented routes to %s", len(undocumented), goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run `go test -run TestOpenAPICoverage -update`): %v", err)
	}
	if got != string(want) {
		t.Errorf("OpenAPI route coverage changed vs %s.\n"+
			"A registered route is undocumented (or a documented one changed).\n"+
			"Document it in internal/api/openapi.yaml, or regenerate the gap golden with:\n"+
			"  go test ./internal/app/ -run TestOpenAPICoverage -update\n"+
			"(%d undocumented now vs %d in golden)",
			openapiPath, len(undocumented), len(strings.Split(strings.TrimSpace(string(want)), "\n")))
	}
}
