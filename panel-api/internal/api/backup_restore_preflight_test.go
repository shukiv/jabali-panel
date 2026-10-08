package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: the restore preflight. Before a restore from an uploaded file the
// panel checks the backup against this server: a PHP version the account's
// sites use and this server lacks blocks the restore; missing PHP extensions,
// and PostgreSQL, mail, DNS or Docker apps turned off here, are warnings, and
// the restore leaves those parts out.

// pfFacts is a server with every feature on and PHP 8.3 with intl.
func pfFacts() restorePreflightFacts {
	return restorePreflightFacts{settingsRead: true, postgres: true, mail: true, dns: true, docker: true,
		phpVersions:   map[string]bool{"8.3": true},
		phpExtensions: map[string]map[string]bool{"8.3": {"intl": true}}}
}

func pfInspect(s *internalbackup.BundleSummary) uploadInspect {
	return uploadInspect{Summary: s, PreflightSupported: true}
}

// levels is the preflight's checks as area=level.
func levels(p restorePreflight) string {
	var out []string
	for _, c := range p.Checks {
		out = append(out, c.Area+"="+c.Level)
	}
	return strings.Join(out, " ")
}

func pfMessage(p restorePreflight, area string) string {
	for _, c := range p.Checks {
		if c.Area == area {
			return c.Message
		}
	}
	return ""
}

func TestComputeRestorePreflight(t *testing.T) {
	php83 := func() *internalbackup.BundleSummary {
		return &internalbackup.BundleSummary{PHPVersions: []string{"8.3"}, PHPExtensions: map[string][]string{"8.3": {"intl"}}}
	}
	for _, c := range []struct {
		name    string
		ins     uploadInspect
		facts   func(*restorePreflightFacts)
		blocked bool
		levels  string
		message string // in the first check's message
	}{
		{name: "an old agent blocks", ins: uploadInspect{}, blocked: true, levels: "agent=block", message: "too old"},
		{name: "unread settings block", ins: pfInspect(php83()), facts: func(f *restorePreflightFacts) { f.settingsRead = false },
			blocked: true, levels: "settings=block"},
		{name: "no metadata has nothing to check", ins: pfInspect(nil), levels: "backup=info"},
		{name: "all there", ins: pfInspect(php83()), levels: "php=ok php_extensions=ok"},
		{name: "a missing PHP version blocks",
			ins:     pfInspect(&internalbackup.BundleSummary{PHPVersions: []string{"7.4", "8.3"}, PHPExtensions: map[string][]string{"8.3": {"intl"}}}),
			blocked: true, levels: "php=block php_extensions=ok", message: "PHP 7.4 isn't installed on this server"},
		{name: "two missing PHP versions block",
			ins:     pfInspect(&internalbackup.BundleSummary{PHPVersions: []string{"7.4", "8.0"}}),
			blocked: true, levels: "php=block", message: "PHP 7.4 and 8.0 aren't installed on this server"},
		{name: "unread PHP versions block", ins: pfInspect(php83()), facts: func(f *restorePreflightFacts) { f.phpVersions = nil },
			blocked: true, levels: "php=block"},
		{name: "a missing extension warns",
			ins:    pfInspect(&internalbackup.BundleSummary{PHPVersions: []string{"8.3"}, PHPExtensions: map[string][]string{"8.3": {"intl", "redis", "imagick"}}}),
			levels: "php=ok php_extensions=warn", message: "PHP 8.3, which the account's sites use, is installed"},
		{name: "extensions not recorded", ins: pfInspect(&internalbackup.BundleSummary{PHPVersions: []string{"8.3"}}),
			levels: "php=ok php_extensions=info"},
		{name: "extensions not read here", ins: pfInspect(php83()), facts: func(f *restorePreflightFacts) { f.phpExtensions = nil },
			levels: "php=ok php_extensions=info"},
		{name: "PostgreSQL off warns",
			ins:   pfInspect(&internalbackup.BundleSummary{PostgresDatabases: []string{"alice_shop"}, PostgresUsers: 1}),
			facts: func(f *restorePreflightFacts) { f.postgres = false }, levels: "postgres=warn",
			message: "PostgreSQL is turned off on this server, so the backup's 1 PostgreSQL database (alice_shop) and 1 PostgreSQL database user won't be restored"},
		{name: "PostgreSQL on", ins: pfInspect(&internalbackup.BundleSummary{PostgresDatabases: []string{"alice_shop"}}), levels: "postgres=ok"},
		{name: "mail off warns", ins: pfInspect(&internalbackup.BundleSummary{Mailboxes: 3, Forwarders: 1}),
			facts: func(f *restorePreflightFacts) { f.mail = false }, levels: "mail=warn", message: "3 mailboxes and 1 forwarder won't be restored"},
		{name: "DNS off warns", ins: pfInspect(&internalbackup.BundleSummary{DNSRecords: 2}),
			facts: func(f *restorePreflightFacts) { f.dns = false }, levels: "dns=warn", message: "2 custom DNS records won't be restored"},
		{name: "Docker off warns", ins: pfInspect(&internalbackup.BundleSummary{DockerApps: []string{"n8n-2"}}),
			facts: func(f *restorePreflightFacts) { f.docker = false }, levels: "docker=warn", message: "1 Docker app (n8n-2) won't be restored"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := pfFacts()
			if c.facts != nil {
				c.facts(&f)
			}
			p := computeRestorePreflight(c.ins, f)
			if p.Blocked != c.blocked || levels(p) != c.levels {
				t.Fatalf("blocked %v checks %q, want %v %q: %+v", p.Blocked, levels(p), c.blocked, c.levels, p.Checks)
			}
			if c.message != "" && !strings.Contains(p.Checks[0].Message, c.message) {
				t.Errorf("message %q, want it to say %q", p.Checks[0].Message, c.message)
			}
		})
	}
}

// A part turned off here is left out even when the summary names none of it:
// the summary is read from the uploader's file.
func TestComputeRestorePreflight_SkipsFollowTheServer(t *testing.T) {
	f := pfFacts()
	f.postgres, f.mail, f.dns, f.docker = false, false, false, false
	p := computeRestorePreflight(pfInspect(&internalbackup.BundleSummary{}), f)
	if s := p.skips; !s.postgres || !s.mail || !s.dns || !s.docker || len(s.notes) != 0 {
		t.Errorf("skips %+v, want all four and no notes for a backup without them", s)
	}
	p = computeRestorePreflight(pfInspect(&internalbackup.BundleSummary{Mailboxes: 1, DNSRecords: 1}), f)
	if strings.Join(p.skips.notes, "|") != "mail (1 mailbox): not restored: mail is turned off on this server|DNS (1 custom DNS record): not restored: DNS is turned off on this server" {
		t.Errorf("notes %q", p.skips.notes)
	}
	if p := computeRestorePreflight(pfInspect(nil), pfFacts()); p.skips.postgres || p.skips.mail || p.skips.dns || p.skips.docker {
		t.Errorf("all features on, skips %+v", p.skips)
	}
}

// pfAlice74 is alice's archive whose sites use PHP 7.4.
const pfAlice74 = `{"user":{"username":"alice","email":"alice@example.org"},"components":["home","db","mail"],"preflight_supported":true,
	"summary":{"php_versions":["7.4"],"postgres_databases":["alice_shop"],"postgres_users":0,"mailboxes":2,"forwarders":0,"dns_records":0,"docker_apps":[]}}`

func TestRestoreUploadInspect_GivesThePreflightNotTheSummary(t *testing.T) {
	restoreUploadDir = t.TempDir()
	t.Cleanup(func() { restoreUploadDir = "/var/lib/jabali-uploads" })
	cfg := ucConfig()
	cfg.Agent = (&ubAgent{inspect: pfAlice74, phpVersions: `"8.3"`}).agent()
	cfg.Users = ubUsers{}
	cfg.ServerSettings = &fakeSettingsRepo{s: allFeaturesOn()}
	h := &backupHandler{cfg: cfg}
	stage(t, "upload0001")

	w := keRequest(t, h.restoreUploadInspect, ubAdmin, true, map[string]any{"upload_id": "upload0001"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var out map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if _, ok := out["summary"]; ok {
		t.Errorf("inspect passed the agent's summary through: %s", w.Body)
	}
	var p restorePreflight
	if err := json.Unmarshal(out["preflight"], &p); err != nil || !p.Blocked || levels(p) != "php=block postgres=ok mail=ok" {
		t.Errorf("preflight %s (%v), want PHP 7.4 to block", out["preflight"], err)
	}
}

func TestRestoreUploadApply_ThePreflightBlocks(t *testing.T) {
	restoreUploadDir = t.TempDir()
	t.Cleanup(func() { restoreUploadDir = "/var/lib/jabali-uploads" })
	a := &ubAgent{inspect: pfAlice74, phpVersions: `"8.3"`}
	cfg := ucConfig()
	cfg.Agent = a.agent()
	cfg.Users = ubUsers{}
	cfg.ServerSettings = &fakeSettingsRepo{s: allFeaturesOn()}
	h := &backupHandler{cfg: cfg}
	stage(t, "upload0001")

	w := keRequest(t, h.restoreUploadApply, ubAdmin, true, map[string]any{"upload_id": "upload0001", "target_username": "alice"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "restore_preflight_blocked") || !strings.Contains(w.Body.String(), "PHP 7.4") {
		t.Fatalf("status %d %s, want 409 restore_preflight_blocked naming PHP 7.4", w.Code, w.Body)
	}
	if len(a.seen) != 0 {
		t.Errorf("a blocked restore ran the agent restore: %v", a.seen)
	}
	if _, err := readRestoreUploadOutcome(restoreUploadOutcomePath(ubAdmin, "upload0001")); err == nil {
		t.Error("a blocked restore started")
	}
}

func TestRestoreUploadedBackup_ThePreflightBlocks(t *testing.T) {
	a := &ubAgent{inspect: pfAlice74, phpVersions: `"8.3"`}
	e := newUBEnv(t, a)
	b := keep(t, e, models.UploadedBackupKeep)

	w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore", map[string]any{"target_username": "alice"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "restore_preflight_blocked") {
		t.Fatalf("status %d %s, want 409 restore_preflight_blocked", w.Code, w.Body)
	}
	if got := e.repo.get(b.ID); got.RestoreStatus != "" || len(a.seen) != 0 {
		t.Errorf("a blocked restore was claimed (%q) or ran (%v)", got.RestoreStatus, a.seen)
	}

	// Once the version is installed, the same restore runs.
	a.phpVersions = `"7.4","8.3"`
	if w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore", map[string]any{"target_username": "alice"}); w.Code != http.StatusAccepted {
		t.Fatalf("status %d %s, want 202", w.Code, w.Body)
	}
	waitRestore(t, e, b.ID)
}

func TestRestoreUploadedBackup_LeavesOutWhatIsTurnedOff(t *testing.T) {
	a := &ubAgent{
		inspect: `{"user":{"username":"alice"},"preflight_supported":true,
			"summary":{"php_versions":[],"postgres_databases":["alice_shop"],"postgres_users":1,"mailboxes":2,"forwarders":0,"dns_records":0,"docker_apps":["n8n-2"]}}`,
		// The archive has mail: with mail on, a second pass would restore it.
		restore: func() (string, error) {
			return `{"applied":[],"upload_confinement_enforced":true,"stages":[{"name":"home"},{"name":"mail"}]}`, nil
		},
	}
	e := newUBEnv(t, a)
	e.settings.PostgresEnabled, e.settings.MailEnabled, e.settings.DockerAppsForUsersEnabled = false, false, false
	b := keep(t, e, models.UploadedBackupKeep)

	if w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups/"+b.ID+"/restore", map[string]any{"target_username": "alice"}); w.Code != http.StatusAccepted {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	done := waitRestore(t, e, b.ID)
	if len(a.seen) != 1 {
		t.Fatalf("%d agent restore passes, want 1 (no mail pass): %v", len(a.seen), a.seen)
	}
	p := a.seen[0]
	if p["skip_postgres"] != true || strings.Join(strs(p["skip_components"]), ",") != "docker,mail" {
		t.Errorf("restore_from_tar params %v, want skip_postgres and docker+mail skipped", p)
	}
	report := ""
	if done.RestoreResult != nil {
		report = *done.RestoreResult
	}
	for _, want := range []string{"PostgreSQL (1 PostgreSQL database (alice_shop) and 1 PostgreSQL database user): not restored",
		"mail (2 mailboxes): not restored", "Docker apps (1 Docker app (n8n-2)): not restored"} {
		if !strings.Contains(report, want) {
			t.Errorf("report %s, want %q", report, want)
		}
	}
}

func TestRestoreUploadedAccount_DNSTurnedOffSkipsTheDNSStep(t *testing.T) {
	h, a, calls := ucUploadPasses(t, func(int) string {
		return `{"stages":[{"name":"home"}],"upload_confinement_enforced":true}`
	})
	var labels []string
	steps := 0
	report := func(p restoreProgress) {
		labels = append(labels, p.Label)
		steps = p.Steps
	}
	if _, err := h.restoreUploadedAccount(context.Background(), a.path, "alice", "T", []string{"home"}, uploadOverwrite, false, restoreSkips{dns: true}, report); err != nil {
		t.Fatal(err)
	}
	for _, l := range labels {
		if l == restoreStepDNSLabel {
			t.Errorf("DNS is off, but the restore ran its DNS step: %v", labels)
		}
	}
	if steps != 2 || len(*calls) != 1 {
		t.Errorf("%d steps, %d passes; want 2 steps (files, rows) and 1 pass", steps, len(*calls))
	}
}

func TestRestoreMetadataDeps_SkipWiring(t *testing.T) {
	h := &backupHandler{}
	d := h.restoreMetadataDeps(&uploadedData{skipMail: true, skipPostgres: true})
	if !d.SkipMail || !d.SkipPostgres {
		t.Errorf("deps SkipMail=%v SkipPostgres=%v, want both", d.SkipMail, d.SkipPostgres)
	}
	if d := h.restoreMetadataDeps(nil); d.SkipMail || d.SkipPostgres {
		t.Error("a restore from this server's own backups left parts out")
	}
}

func TestUploadedBackupPreflight(t *testing.T) {
	e := newUBEnv(t, &ubAgent{inspect: pfAlice74, phpVersions: `"7.4"`})
	e.settings.PostgresEnabled = false
	b := keep(t, e, models.UploadedBackupKeep)

	w := e.do(t, http.MethodGet, "/api/v1/admin/uploaded-backups/"+b.ID+"/preflight", nil)
	var out struct {
		Data restorePreflight `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if out.Data.Blocked || levels(out.Data) != "php=ok php_extensions=info postgres=warn mail=ok" {
		t.Errorf("preflight %+v", out.Data)
	}
	if !strings.Contains(pfMessage(out.Data, "postgres"), "alice_shop") {
		t.Errorf("postgres line %q", pfMessage(out.Data, "postgres"))
	}
}

func TestRegisterUploadedBackup_ReturnsThePreflight(t *testing.T) {
	e := newUBEnv(t, &ubAgent{inspect: pfAlice74, phpVersions: `"8.3"`})
	stage(t, "upload0001")
	w := e.do(t, http.MethodPost, "/api/v1/admin/uploaded-backups", map[string]any{"upload_id": "upload0001"})
	var out struct {
		Data struct {
			Preflight *restorePreflight `json:"preflight"`
		} `json:"data"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Data.Preflight == nil || !out.Data.Preflight.Blocked {
		t.Fatalf("status %d %s, want 201 with a blocking preflight", w.Code, w.Body)
	}
}
