package userops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ---- fakes (all append to one recorder so step ORDER can be asserted) ----

type drRecorder struct{ events []string }

type drDomains struct {
	repository.DomainRepository
	rec       *drRecorder
	byName    map[string]*models.Domain // pre-existing names (FindByName)
	renameErr error
}

func (d *drDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if dm, ok := d.byName[name]; ok {
		return dm, nil
	}
	return nil, repository.ErrNotFound
}

func (d *drDomains) Rename(_ context.Context, _, _, _ string) error {
	if d.renameErr != nil {
		return d.renameErr
	}
	d.rec.events = append(d.rec.events, "db.rename")
	return nil
}

type drUsers struct {
	repository.UserRepository
	user *models.User
	err  error
}

func (u *drUsers) FindByID(_ context.Context, _ string) (*models.User, error) {
	return u.user, u.err
}

type drAgent struct {
	rec     *drRecorder
	callErr error
	// errByMethod fails the named agent methods (checked before callErr), so a
	// test can let some verbs succeed while another fails.
	errByMethod  map[string]error
	last         map[string]any
	reownLast    map[string]any   // captured domain.reown params (stable across later calls)
	refreshCalls []map[string]any // migration.refresh_reconcile params, in order
	// refreshResp, when set, is the migration.refresh_reconcile response body.
	refreshResp json.RawMessage

	// mail.domain.rename fakes. The verb is called twice — dry_run then real.
	mailRenameCalls  []map[string]any
	mailDryStatus    string   // default "ok"
	mailRealStatus   string   // default "not_in_registry"
	mailRealWarnings []string // surfaced on the real call
	mailRealErr      error    // fails ONLY the real (non-dry) call
}

func (a *drAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.rec.events = append(a.rec.events, "agent."+method)
	var m map[string]any
	if mm, ok := params.(map[string]any); ok {
		m = mm
		a.last = mm
		switch method {
		case "domain.reown":
			a.reownLast = mm
		case "migration.refresh_reconcile":
			a.refreshCalls = append(a.refreshCalls, mm)
		case "mail.domain.rename":
			a.mailRenameCalls = append(a.mailRenameCalls, mm)
		}
	}
	if a.errByMethod != nil {
		if e, ok := a.errByMethod[method]; ok {
			return nil, e
		}
	}
	if a.callErr != nil {
		return nil, a.callErr
	}
	switch method {
	case "migration.refresh_reconcile":
		if a.refreshResp != nil {
			return a.refreshResp, nil
		}
		return json.RawMessage(`{"ok":true,"warnings":[]}`), nil
	case "mail.domain.rename":
		dry, _ := m["dry_run"].(bool)
		if dry {
			st := a.mailDryStatus
			if st == "" {
				st = "ok"
			}
			return json.RawMessage(`{"status":"` + st + `"}`), nil
		}
		if a.mailRealErr != nil {
			return nil, a.mailRealErr
		}
		st := a.mailRealStatus
		if st == "" {
			st = "not_in_registry"
		}
		resp := map[string]any{"status": st}
		if len(a.mailRealWarnings) > 0 {
			resp["warnings"] = a.mailRealWarnings
		}
		b, _ := json.Marshal(resp)
		return b, nil
	}
	return json.RawMessage(`{}`), nil
}

type drAppInstalls struct {
	installs []models.ApplicationInstall
	err      error
}

func (a *drAppInstalls) ListByDomainIDs(_ context.Context, _ []string) ([]models.ApplicationInstall, error) {
	return a.installs, a.err
}

type drTeardowns struct {
	rec     *drRecorder
	ensured []string
	deleted []string
}

func (t *drTeardowns) Ensure(_ context.Context, name string) error {
	t.rec.events = append(t.rec.events, "tombstone.ensure")
	t.ensured = append(t.ensured, name)
	return nil
}
func (t *drTeardowns) Delete(_ context.Context, name string) error {
	t.deleted = append(t.deleted, name)
	return nil
}
func (t *drTeardowns) List(_ context.Context) ([]models.DomainTeardown, error) {
	return nil, nil
}
func (t *drTeardowns) MarkAttempt(_ context.Context, _, _ string) error { return nil }

type drDNSZones struct {
	repository.DNSZoneRepository
	rec       *drRecorder
	zone      *models.DNSZone // nil => FindByDomainID returns ErrNotFound
	updErr    error
	renamedTo string
}

func (z *drDNSZones) FindByDomainID(_ context.Context, _ string) (*models.DNSZone, error) {
	if z.zone == nil {
		return nil, repository.ErrNotFound
	}
	return z.zone, nil
}
func (z *drDNSZones) Update(_ context.Context, zone *models.DNSZone) error {
	if z.updErr != nil {
		return z.updErr
	}
	z.rec.events = append(z.rec.events, "zone.rename")
	z.renamedTo = zone.Name
	return nil
}

type drSSLCerts struct {
	repository.SSLCertificateRepository
	rec      *drRecorder
	cert     *models.SSLCertificate // nil => FindByDomainID returns ErrNotFound
	resetErr error
	resetID  string
}

func (s *drSSLCerts) FindByDomainID(_ context.Context, _ string) (*models.SSLCertificate, error) {
	if s.cert == nil {
		return nil, repository.ErrNotFound
	}
	return s.cert, nil
}
func (s *drSSLCerts) ResetForRetry(_ context.Context, id string, _ time.Time) error {
	if s.resetErr != nil {
		return s.resetErr
	}
	s.rec.events = append(s.rec.events, "cert.reset")
	s.resetID = id
	return nil
}

type drSched struct{ scheduled []string }

func (r *drSched) Schedule(id string) { r.scheduled = append(r.scheduled, id) }

func drUIDPtr(v uint32) *uint32 { return &v }

// drHappyOwner is a fully provisioned tenant.
func drHappyOwner() *models.User {
	uname := "u1"
	return &models.User{ID: "user-1", Username: &uname, LinuxUID: drUIDPtr(1001)}
}

func drWebDomain() *models.Domain {
	return &models.Domain{
		ID: "dom-1", UserID: "user-1", Name: "old.com",
		DocRoot: "/home/u1/public_html/old.com",
	}
}

// drNewDeps wires a fully-armed rename: a zone row on the old name and a cert
// row, so the happy path exercises the zone re-key + cert reset heals. The mail
// carry defaults to not_in_registry (a plain web domain), so it proceeds.
func drNewDeps(rec *drRecorder, dom *models.Domain, owner *models.User) (Deps, *drAgent, *drTeardowns) {
	ag := &drAgent{rec: rec}
	td := &drTeardowns{rec: rec}
	d := Deps{
		Domains:         &drDomains{rec: rec, byName: map[string]*models.Domain{}},
		Users:           &drUsers{user: owner},
		DomainTeardowns: td,
		Agent:           ag,
		DNSZones:        &drDNSZones{rec: rec, zone: &models.DNSZone{ID: "zone-1", DomainID: dom.ID, Name: dom.Name}},
		SSLCerts:        &drSSLCerts{rec: rec, cert: &models.SSLCertificate{ID: "cert-1", DomainID: dom.ID}},
	}
	return d, ag, td
}

func drCodeOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var re *RenameError
	if !errors.As(err, &re) {
		t.Fatalf("expected *RenameError, got %T: %v", err, err)
	}
	return re.Code
}

func assertEvents(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// The two mail.domain.rename calls (dry-run then real) bracket the docroot move.
const evDryMail = "agent.mail.domain.rename"

// ---- gate matrix ----

func TestRenameDomain_Gates(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*models.Domain)
		owner   *models.User
		newName string
		want    string
	}{
		{"empty name", nil, drHappyOwner(), "", "invalid_name"},
		{"no-op same name", nil, drHappyOwner(), "OLD.com", "noop"},
		{"panel primary", func(d *models.Domain) { d.IsPanelPrimary = true }, drHappyOwner(), "new.com", "panel_primary"},
		{"web disabled", func(d *models.Domain) { d.WebDisabled = true }, drHappyOwner(), "new.com", "web_disabled"},
		{"custom cert", func(d *models.Domain) { d.SSLMode = models.SSLModeCustom }, drHappyOwner(), "new.com", "ssl_custom_cert"},
		{"shared cert", func(d *models.Domain) { d.SSLMode = models.SSLModeShared }, drHappyOwner(), "new.com", "ssl_custom_cert"},
		{"owner unprovisioned", nil, &models.User{ID: "user-1"}, "new.com", "owner_unprovisioned"},
		{"custom docroot", func(d *models.Domain) { d.DocRoot = "/srv/www/site" }, drHappyOwner(), "new.com", "custom_docroot"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &drRecorder{}
			dom := drWebDomain()
			if tc.mutate != nil {
				tc.mutate(dom)
			}
			d, ag, _ := drNewDeps(rec, dom, tc.owner)
			_, err := RenameDomain(context.Background(), d, &drSched{}, dom, tc.newName)
			if got := drCodeOf(t, err); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
			// A gate refusal must never touch the box or move the row.
			if len(ag.rec.events) != 0 {
				t.Fatalf("gate refusal must not run any step, got events %v", ag.rec.events)
			}
		})
	}
}

// ---- mail carry (GH #1579 task b) ----

// A conflict at the DRY-RUN (the new name already carries mail) refuses BEFORE
// any files move — only the dry-run call ran, no reown, no DB rename.
func TestRenameDomain_MailConflictRefusesBeforeMove(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	ag.mailDryStatus = "conflict"

	_, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "mail_domain_conflict" {
		t.Fatalf("code = %q, want mail_domain_conflict", got)
	}
	assertEvents(t, rec.events, []string{evDryMail})
	if ag.reownLast != nil {
		t.Fatalf("a dry-run conflict must refuse before the file move")
	}
	if dom.Name != "old.com" {
		t.Fatalf("row must be unchanged, got %q", dom.Name)
	}
}

// A conflict on the REAL run (raced another rename since the dry-run) refuses
// after the move but before the DB flip; the row is unchanged.
func TestRenameDomain_MailConflictOnRealRun(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, td := drNewDeps(rec, dom, drHappyOwner())
	ag.mailDryStatus = "ok"
	ag.mailRealStatus = "conflict"

	_, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "mail_domain_conflict" {
		t.Fatalf("code = %q, want mail_domain_conflict", got)
	}
	assertEvents(t, rec.events, []string{evDryMail, "agent.domain.reown", evDryMail})
	if dom.Name != "old.com" {
		t.Fatalf("row must be unchanged on a real-run conflict, got %q", dom.Name)
	}
	if len(td.ensured) != 0 {
		t.Fatalf("no tombstone before the DB flip, got %v", td.ensured)
	}
}

// The dry-run mail check is fail-closed: an agent error there refuses the rename
// before anything moves.
func TestRenameDomain_MailDryRunErrorFailsClosed(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	ag.errByMethod = map[string]error{"mail.domain.rename": errors.New("stalwart down")}

	_, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "unavailable" {
		t.Fatalf("code = %q, want unavailable", got)
	}
	if ag.reownLast != nil {
		t.Fatalf("a dry-run agent error must refuse before the file move")
	}
}

// The real mail rename is fail-closed: an agent error after the move refuses,
// and NOTHING is persisted (the DB row still holds the old name, no tombstone).
func TestRenameDomain_MailRealRenameFailsClosed(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	ag.mailRealErr = errors.New("stalwart down mid-run")

	_, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "mail_rename_failed" {
		t.Fatalf("code = %q, want mail_rename_failed", got)
	}
	if ag.reownLast == nil {
		t.Fatalf("reown should have run before the mail rename")
	}
	for _, e := range rec.events {
		if e == "db.rename" || e == "tombstone.ensure" {
			t.Fatalf("nothing must persist on a mail-rename failure, got %v", rec.events)
		}
	}
	if dom.Name != "old.com" {
		t.Fatalf("row must be unchanged, got %q", dom.Name)
	}
}

// A catch-all the verb could not rewrite comes back as a warning on the 200 —
// the rename itself (mail carried) still succeeds.
func TestRenameDomain_MailCatchallWarningSurfaced(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	ag.mailRealStatus = "renamed"
	ag.mailRealWarnings = []string{"mail carried to the new name, but the catch-all address still points at the old name"}

	warnings, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dom.Name != "new.com" {
		t.Fatalf("row should be renamed, got %q", dom.Name)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "catch-all") {
		t.Fatalf("catch-all warning must surface, got %v", warnings)
	}
}

// The mail carry runs twice (dry then real) and brackets the docroot move; it
// passes the OLD and NEW names to the verb.
func TestRenameDomain_MailCarryCallShape(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())

	if _, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ag.mailRenameCalls) != 2 {
		t.Fatalf("want 2 mail.domain.rename calls (dry + real), got %d", len(ag.mailRenameCalls))
	}
	dry, real := ag.mailRenameCalls[0], ag.mailRenameCalls[1]
	if dry["dry_run"] != true {
		t.Fatalf("first call must be dry_run, got %v", dry)
	}
	if real["dry_run"] == true {
		t.Fatalf("second call must be the real run, got %v", real)
	}
	for _, c := range ag.mailRenameCalls {
		if c["old"] != "old.com" || c["new"] != "new.com" {
			t.Fatalf("mail rename call = %v, want old.com -> new.com", c)
		}
	}
}

func TestRenameDomain_NameTaken(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, _, _ := drNewDeps(rec, dom, drHappyOwner())
	d.Domains.(*drDomains).byName["new.com"] = &models.Domain{ID: "other", Name: "new.com"}
	_, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "name_taken" {
		t.Fatalf("code = %q, want name_taken", got)
	}
	if len(rec.events) != 0 {
		t.Fatalf("name-taken must refuse before any step, got %v", rec.events)
	}
}

// ---- happy path: order, reown params, zone/cert heal, schedule ----

func TestRenameDomain_HappyPath(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, td := drNewDeps(rec, dom, drHappyOwner())
	sched := &drSched{}

	if _, err := RenameDomain(context.Background(), d, sched, dom, "New.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Order is load-bearing: dry-run the mail carry, move the files, carry mail
	// (Stalwart rename in place) BEFORE the DB flip, then rename the row, then
	// tombstone the now-freed old name, then heal DNS + SSL for the new name.
	assertEvents(t, rec.events, []string{
		evDryMail, "agent.domain.reown", evDryMail, "db.rename",
		"tombstone.ensure", "zone.rename", "cert.reset",
	})

	// Tombstone is for the OLD name; never deleted on success.
	if len(td.ensured) != 1 || td.ensured[0] != "old.com" {
		t.Fatalf("tombstone ensured = %v, want [old.com]", td.ensured)
	}
	if len(td.deleted) != 0 {
		t.Fatalf("tombstone must not be deleted on success, got %v", td.deleted)
	}

	// reown moves old -> new docroot under the OWNER's uid (not a new uid).
	if ag.reownLast["old_doc_root"] != "/home/u1/public_html/old.com" {
		t.Fatalf("old_doc_root = %v", ag.reownLast["old_doc_root"])
	}
	if ag.reownLast["new_doc_root"] != "/home/u1/public_html/new.com" {
		t.Fatalf("new_doc_root = %v", ag.reownLast["new_doc_root"])
	}
	if ag.reownLast["new_uid"] != int(1001) {
		t.Fatalf("new_uid = %v (%T), want 1001", ag.reownLast["new_uid"], ag.reownLast["new_uid"])
	}

	// The DNS zone row is re-keyed to the new name and the cert row reset.
	if got := d.DNSZones.(*drDNSZones).renamedTo; got != "new.com" {
		t.Fatalf("zone renamed to %q, want new.com", got)
	}
	if got := d.SSLCerts.(*drSSLCerts).resetID; got != "cert-1" {
		t.Fatalf("cert reset id = %q, want cert-1", got)
	}

	// The in-memory domain reflects the new name + docroot for the caller.
	if dom.Name != "new.com" || dom.DocRoot != "/home/u1/public_html/new.com" {
		t.Fatalf("domain not updated in place: name=%q docroot=%q", dom.Name, dom.DocRoot)
	}
	if len(sched.scheduled) != 1 || sched.scheduled[0] != "dom-1" {
		t.Fatalf("Schedule = %v, want [dom-1]", sched.scheduled)
	}
}

// Default docroot layout (leaf == name): the move renames the leaf in place, so
// no old-name wrapper dir is left — the reown call must NOT ask to prune one.
func TestRenameDomain_DefaultLayoutNoPrune(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain() // /home/u1/public_html/old.com
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	if _, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := ag.reownLast["prune_empty_dir"]; ok {
		t.Fatalf("default layout must not set prune_empty_dir, got %v", ag.reownLast["prune_empty_dir"])
	}
}

// Nested/importer docroot layout: the docroot leaf moves out of the old-name
// wrapper dir, so the reown call must ask the agent to prune that empty wrapper.
func TestRenameDomain_NestedLayoutPrunesOldDir(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	dom.DocRoot = "/home/u1/domains/old.com/public_html"
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	if _, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ag.reownLast["new_doc_root"] != "/home/u1/domains/new.com/public_html" {
		t.Fatalf("new_doc_root = %v", ag.reownLast["new_doc_root"])
	}
	if ag.reownLast["prune_empty_dir"] != "/home/u1/domains/old.com" {
		t.Fatalf("prune_empty_dir = %v, want /home/u1/domains/old.com", ag.reownLast["prune_empty_dir"])
	}
	if dom.DocRoot != "/home/u1/domains/new.com/public_html" {
		t.Fatalf("domain docroot not updated: %q", dom.DocRoot)
	}
}

// A rename with no DNS zone / no cert row (SSL never provisioned) still
// succeeds — those heals are best-effort and skip cleanly.
func TestRenameDomain_HappyPath_NoZoneNoCert(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, _, _ := drNewDeps(rec, dom, drHappyOwner())
	d.DNSZones.(*drDNSZones).zone = nil // FindByDomainID -> ErrNotFound
	d.SSLCerts.(*drSSLCerts).cert = nil

	if _, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEvents(t, rec.events, []string{
		evDryMail, "agent.domain.reown", evDryMail, "db.rename", "tombstone.ensure",
	})
}

// ---- failure handling ----

// A DB rename failure AFTER the files have already moved: the row is unchanged
// (still the live handle for old.com) and NO tombstone was ever written (it
// only comes after the flip), so nothing needs rolling back and the sweep can't
// touch the live domain. A re-run finishes: reown -> AlreadyDone, then rename.
func TestRenameDomain_PersistFailureAfterMove(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, td := drNewDeps(rec, dom, drHappyOwner())
	d.Domains.(*drDomains).renameErr = errors.New("db down")

	_, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "persist_failed" {
		t.Fatalf("code = %q, want persist_failed", got)
	}
	// Files moved (reown ran) but the row is still old.com.
	if ag.reownLast == nil {
		t.Fatalf("reown should have run before the DB rename")
	}
	if dom.Name != "old.com" {
		t.Fatalf("row must be unchanged on persist failure, got %q", dom.Name)
	}
	// No tombstone was written, so there is nothing to (and nothing did) delete.
	if len(td.ensured) != 0 || len(td.deleted) != 0 {
		t.Fatalf("no tombstone should exist on persist failure, ensured=%v deleted=%v", td.ensured, td.deleted)
	}
}

func TestRenameDomain_PersistConflictIsNameTaken(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, _, _ := drNewDeps(rec, dom, drHappyOwner())
	d.Domains.(*drDomains).renameErr = repository.ErrConflict

	_, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "name_taken" {
		t.Fatalf("code = %q, want name_taken (conflict from unique index)", got)
	}
}

// The file move is the first mutation; if it fails, nothing is persisted — no DB
// rename, no tombstone, the row is untouched. (The dry-run mail check ran first
// and passed.)
func TestRenameDomain_MoveFailureBeforeCommit(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, td := drNewDeps(rec, dom, drHappyOwner())
	ag.errByMethod = map[string]error{"domain.reown": errors.New("agent unreachable")}

	_, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "move_failed" {
		t.Fatalf("code = %q, want move_failed", got)
	}
	// No DB rename, no tombstone, row unchanged.
	for _, e := range rec.events {
		if e == "db.rename" || e == "tombstone.ensure" {
			t.Fatalf("no persistence must happen on a pre-commit move failure, got %v", rec.events)
		}
	}
	if dom.Name != "old.com" {
		t.Fatalf("row must be unchanged on move failure, got %q", dom.Name)
	}
	if len(td.ensured) != 0 {
		t.Fatalf("no tombstone on move failure, got %v", td.ensured)
	}
}

// A post-commit heal failure (zone re-key errors) does NOT fail the rename —
// the row + files are already renamed and the reconciler converges the rest.
func TestRenameDomain_HealFailureStillSucceeds(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, _, _ := drNewDeps(rec, dom, drHappyOwner())
	d.DNSZones.(*drDNSZones).updErr = errors.New("zone update failed")
	d.SSLCerts.(*drSSLCerts).resetErr = errors.New("cert reset failed")

	if _, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("a post-commit heal failure must not fail the rename, got %v", err)
	}
	if dom.Name != "new.com" {
		t.Fatalf("row should be renamed despite heal failure, got %q", dom.Name)
	}
}

// ---- WordPress site-URL rewrite (GH #1579 app-URL slice) ----

// A WordPress install on the domain gets its stored site URL rewritten after
// the rename: two migration.refresh_reconcile passes (https then http) against
// the NEW docroot + new name, after the DNS/SSL heals and before Schedule.
func TestRenameDomain_WordPressURLRewrite(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	d.AppInstalls = &drAppInstalls{installs: []models.ApplicationInstall{
		{ID: "app-1", DomainID: dom.ID, AppType: "wordpress", Subdirectory: ""},
	}}

	warnings, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("clean rewrite must produce no warnings, got %v", warnings)
	}

	// The rewrite runs AFTER the DNS/SSL heals.
	assertEvents(t, rec.events, []string{
		evDryMail, "agent.domain.reown", evDryMail, "db.rename", "tombstone.ensure",
		"zone.rename", "cert.reset",
		"agent.migration.refresh_reconcile", "agent.migration.refresh_reconcile",
	})

	if len(ag.refreshCalls) != 2 {
		t.Fatalf("want 2 refresh calls (https + http), got %d: %v", len(ag.refreshCalls), ag.refreshCalls)
	}
	https, http := ag.refreshCalls[0], ag.refreshCalls[1]
	if https["old_url"] != "https://old.com" || https["new_url"] != "https://new.com" {
		t.Fatalf("https pass urls = %v -> %v", https["old_url"], https["new_url"])
	}
	if http["old_url"] != "http://old.com" || http["new_url"] != "http://new.com" {
		t.Fatalf("http pass urls = %v -> %v", http["old_url"], http["new_url"])
	}
	// Runs as the owner, against the NEW docroot, with the NEW domain name.
	if https["os_user"] != "u1" {
		t.Fatalf("os_user = %v, want u1", https["os_user"])
	}
	if https["install_path"] != "/home/u1/public_html/new.com" {
		t.Fatalf("install_path = %v, want the new docroot", https["install_path"])
	}
	if https["domain"] != "new.com" {
		t.Fatalf("domain = %v, want new.com", https["domain"])
	}
}

// A WordPress install in a subdirectory rewrites against docroot/<subdir>.
func TestRenameDomain_WordPressURLRewrite_Subdirectory(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	d.AppInstalls = &drAppInstalls{installs: []models.ApplicationInstall{
		{ID: "app-1", DomainID: dom.ID, AppType: "wordpress", Subdirectory: "blog"},
	}}

	if _, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ag.refreshCalls) != 2 {
		t.Fatalf("want 2 refresh calls, got %d", len(ag.refreshCalls))
	}
	if got := ag.refreshCalls[0]["install_path"]; got != "/home/u1/public_html/new.com/blog" {
		t.Fatalf("install_path = %v, want .../new.com/blog", got)
	}
}

// A non-WordPress install is never rewritten (the panel does not know its
// internal config) and produces no warning.
func TestRenameDomain_NonWordPressAppNoRewrite(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	d.AppInstalls = &drAppInstalls{installs: []models.ApplicationInstall{
		{ID: "app-1", DomainID: dom.ID, AppType: "dokuwiki", Subdirectory: ""},
	}}

	warnings, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("non-WordPress app must not warn, got %v", warnings)
	}
	if len(ag.refreshCalls) != 0 {
		t.Fatalf("non-WordPress app must not trigger a rewrite, got %v", ag.refreshCalls)
	}
}

// An owner whose name is outside the refresh verb's stricter shape (underscore)
// skips the rewrite with a warning rather than sending a rejected call.
func TestRenameDomain_WordPressRewrite_UnderscoreOwnerWarns(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	uname := "u_1"
	owner := &models.User{ID: "user-1", Username: &uname, LinuxUID: drUIDPtr(1001)}
	d, ag, _ := drNewDeps(rec, dom, owner)
	d.AppInstalls = &drAppInstalls{installs: []models.ApplicationInstall{
		{ID: "app-1", DomainID: dom.ID, AppType: "wordpress", Subdirectory: ""},
	}}

	warnings, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ag.refreshCalls) != 0 {
		t.Fatalf("underscore owner must not send a refresh call, got %v", ag.refreshCalls)
	}
	if len(warnings) != 1 {
		t.Fatalf("underscore owner must produce exactly one warning, got %v", warnings)
	}
}

// A refresh-verb failure surfaces as a warning but never fails the committed
// rename (the row + files are already renamed).
func TestRenameDomain_WordPressRewrite_AgentErrorWarns(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	ag.errByMethod = map[string]error{"migration.refresh_reconcile": errors.New("wp-cli boom")}
	d.AppInstalls = &drAppInstalls{installs: []models.ApplicationInstall{
		{ID: "app-1", DomainID: dom.ID, AppType: "wordpress", Subdirectory: ""},
	}}

	warnings, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if err != nil {
		t.Fatalf("a rewrite failure must not fail the rename, got %v", err)
	}
	if dom.Name != "new.com" {
		t.Fatalf("row should be renamed despite rewrite failure, got %q", dom.Name)
	}
	if len(warnings) != 1 {
		t.Fatalf("rewrite failure must produce one warning, got %v", warnings)
	}
}

// The verb reports a wp search-replace failure INSIDE its warnings with a nil
// call error — that must surface as a warning (not a silent clean rename).
func TestRenameDomain_WordPressRewrite_SearchReplaceWarningSurfaced(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	ag.refreshResp = json.RawMessage(
		`{"ok":true,"warnings":["search-replace: wp-cli boom","page cache purge deferred to reconcile/nginx.cache.purge"]}`)
	d.AppInstalls = &drAppInstalls{installs: []models.ApplicationInstall{
		{ID: "app-1", DomainID: dom.ID, AppType: "wordpress", Subdirectory: ""},
	}}

	warnings, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if err != nil {
		t.Fatalf("a search-replace failure must not fail the rename, got %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("want exactly one surfaced warning, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "search-replace: wp-cli boom") {
		t.Fatalf("warning must carry the search-replace failure, got %q", warnings[0])
	}
	// The benign page-cache note must NOT be surfaced as a failure, and the
	// dedupe must collapse the identical failure from both scheme passes.
	if strings.Contains(warnings[0], "page cache") {
		t.Fatalf("benign page-cache note must not surface, got %q", warnings[0])
	}
	if strings.Count(warnings[0], "search-replace: wp-cli boom") != 1 {
		t.Fatalf("identical failure from both passes must be deduped, got %q", warnings[0])
	}
}

// A response carrying ONLY the benign page-cache note (which the verb always
// adds on a real box) is a clean success — no warning is surfaced.
func TestRenameDomain_WordPressRewrite_BenignNoteNotSurfaced(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	ag.refreshResp = json.RawMessage(
		`{"ok":true,"warnings":["page cache purge deferred to reconcile/nginx.cache.purge"]}`)
	d.AppInstalls = &drAppInstalls{installs: []models.ApplicationInstall{
		{ID: "app-1", DomainID: dom.ID, AppType: "wordpress", Subdirectory: ""},
	}}

	warnings, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("benign-only warnings must produce no surfaced warning, got %v", warnings)
	}
}

// A www-canonical install stores its site URL with the "www." host, so the
// rewrite must target www.old -> www.new.
func TestRenameDomain_WordPressURLRewrite_WWW(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	d.AppInstalls = &drAppInstalls{installs: []models.ApplicationInstall{
		{ID: "app-1", DomainID: dom.ID, AppType: "wordpress", Subdirectory: "", UseWWW: true},
	}}

	if _, err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ag.refreshCalls) != 2 {
		t.Fatalf("want 2 refresh calls, got %d", len(ag.refreshCalls))
	}
	if ag.refreshCalls[0]["old_url"] != "https://www.old.com" ||
		ag.refreshCalls[0]["new_url"] != "https://www.new.com" {
		t.Fatalf("www install must rewrite the www host, got %v -> %v",
			ag.refreshCalls[0]["old_url"], ag.refreshCalls[0]["new_url"])
	}
}

// ---- docroot segment rewrite ----

func TestRenameDocRootSegment(t *testing.T) {
	tests := []struct {
		name, docRoot, old, new, want, wantPrune, wantErr string
	}{
		// Default layout: the name IS the docroot leaf → move renames it in place,
		// nothing to prune.
		{"default layout", "/home/u/public_html/old.com", "old.com", "new.com", "/home/u/public_html/new.com", "", ""},
		// Nested/importer layout: the name is a wrapper dir → move empties it, so
		// it must be pruned.
		{"importer layout", "/home/u/domains/old.com/public_html", "old.com", "new.com", "/home/u/domains/new.com/public_html", "/home/u/domains/old.com", ""},
		{"custom (no name segment)", "/srv/www/site", "old.com", "new.com", "", "", "custom_docroot"},
		{"ambiguous (name twice)", "/home/old.com/public_html/old.com", "old.com", "new.com", "", "", "ambiguous_docroot"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, prune, err := renameDocRootSegment(tc.docRoot, tc.old, tc.new)
			if tc.wantErr != "" {
				if err == nil || err.Code != tc.wantErr {
					t.Fatalf("err = %v, want code %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if prune != tc.wantPrune {
				t.Fatalf("pruneOldDir = %q, want %q", prune, tc.wantPrune)
			}
		})
	}
}
