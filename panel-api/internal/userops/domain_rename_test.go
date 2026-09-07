package userops

import (
	"context"
	"encoding/json"
	"errors"
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
	last    map[string]any
}

func (a *drAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.rec.events = append(a.rec.events, "agent."+method)
	if m, ok := params.(map[string]any); ok {
		a.last = m
	}
	if a.callErr != nil {
		return nil, a.callErr
	}
	return json.RawMessage(`{}`), nil
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

type drMailboxes struct {
	count int64
	err   error
}

func (m *drMailboxes) CountByDomainID(_ context.Context, _ string) (int64, error) {
	return m.count, m.err
}

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

// drNewDeps wires a fully-armed rename: zero mailboxes, a zone row on the old
// name, and a cert row — so the happy path exercises the zone re-key + cert
// reset heals.
func drNewDeps(rec *drRecorder, dom *models.Domain, owner *models.User) (Deps, *drAgent, *drTeardowns) {
	ag := &drAgent{rec: rec}
	td := &drTeardowns{rec: rec}
	d := Deps{
		Domains:         &drDomains{rec: rec, byName: map[string]*models.Domain{}},
		Users:           &drUsers{user: owner},
		DomainTeardowns: td,
		Agent:           ag,
		Mailboxes:       &drMailboxes{count: 0},
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
		{"mail active", func(d *models.Domain) { d.EmailEnabled = true }, drHappyOwner(), "new.com", "mail_active"},
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
			err := RenameDomain(context.Background(), d, &drSched{}, dom, tc.newName)
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

// ---- mailbox gate (fail-closed) ----

func TestRenameDomain_MailboxesPresent(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, _, _ := drNewDeps(rec, dom, drHappyOwner())
	d.Mailboxes = &drMailboxes{count: 3}
	err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "mailboxes_present" {
		t.Fatalf("code = %q, want mailboxes_present", got)
	}
	if len(rec.events) != 0 {
		t.Fatalf("mailboxes-present must refuse before any step, got %v", rec.events)
	}
}

func TestRenameDomain_MailboxCheckUnwired(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, _, _ := drNewDeps(rec, dom, drHappyOwner())
	d.Mailboxes = nil // fail-closed: cannot prove zero mailboxes
	err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "unavailable" {
		t.Fatalf("code = %q, want unavailable", got)
	}
	if len(rec.events) != 0 {
		t.Fatalf("unwired mailbox check must refuse before any step, got %v", rec.events)
	}
}

func TestRenameDomain_MailboxCountError(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, _, _ := drNewDeps(rec, dom, drHappyOwner())
	d.Mailboxes = &drMailboxes{err: errors.New("db down")}
	err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "unavailable" {
		t.Fatalf("code = %q, want unavailable (can't verify zero mailboxes)", got)
	}
	if len(rec.events) != 0 {
		t.Fatalf("mailbox count error must refuse before any step, got %v", rec.events)
	}
}

func TestRenameDomain_NameTaken(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, _, _ := drNewDeps(rec, dom, drHappyOwner())
	d.Domains.(*drDomains).byName["new.com"] = &models.Domain{ID: "other", Name: "new.com"}
	err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
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

	if err := RenameDomain(context.Background(), d, sched, dom, "New.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Order is load-bearing: move the files, THEN rename the row (the
	// authoritative flip), THEN tombstone the now-freed old name, THEN heal
	// DNS + SSL for the new name. Files-before-DB makes a mid-run failure
	// re-runnable, and tombstoning only after the flip means the sweep can
	// never tear down a live domain.
	want := []string{"agent.domain.reown", "db.rename", "tombstone.ensure", "zone.rename", "cert.reset"}
	if len(rec.events) != len(want) {
		t.Fatalf("events = %v, want %v", rec.events, want)
	}
	for i := range want {
		if rec.events[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (all: %v)", i, rec.events[i], want[i], rec.events)
		}
	}

	// Tombstone is for the OLD name; never deleted on success.
	if len(td.ensured) != 1 || td.ensured[0] != "old.com" {
		t.Fatalf("tombstone ensured = %v, want [old.com]", td.ensured)
	}
	if len(td.deleted) != 0 {
		t.Fatalf("tombstone must not be deleted on success, got %v", td.deleted)
	}

	// reown moves old -> new docroot under the OWNER's uid (not a new uid).
	if ag.last["old_doc_root"] != "/home/u1/public_html/old.com" {
		t.Fatalf("old_doc_root = %v", ag.last["old_doc_root"])
	}
	if ag.last["new_doc_root"] != "/home/u1/public_html/new.com" {
		t.Fatalf("new_doc_root = %v", ag.last["new_doc_root"])
	}
	if ag.last["new_uid"] != int(1001) {
		t.Fatalf("new_uid = %v (%T), want 1001", ag.last["new_uid"], ag.last["new_uid"])
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
	if err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := ag.last["prune_empty_dir"]; ok {
		t.Fatalf("default layout must not set prune_empty_dir, got %v", ag.last["prune_empty_dir"])
	}
}

// Nested/importer docroot layout: the docroot leaf moves out of the old-name
// wrapper dir, so the reown call must ask the agent to prune that empty wrapper.
func TestRenameDomain_NestedLayoutPrunesOldDir(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	dom.DocRoot = "/home/u1/domains/old.com/public_html"
	d, ag, _ := drNewDeps(rec, dom, drHappyOwner())
	if err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ag.last["new_doc_root"] != "/home/u1/domains/new.com/public_html" {
		t.Fatalf("new_doc_root = %v", ag.last["new_doc_root"])
	}
	if ag.last["prune_empty_dir"] != "/home/u1/domains/old.com" {
		t.Fatalf("prune_empty_dir = %v, want /home/u1/domains/old.com", ag.last["prune_empty_dir"])
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

	if err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"agent.domain.reown", "db.rename", "tombstone.ensure"}
	if len(rec.events) != len(want) {
		t.Fatalf("events = %v, want %v", rec.events, want)
	}
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

	err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "persist_failed" {
		t.Fatalf("code = %q, want persist_failed", got)
	}
	// Files moved (reown ran) but the row is still old.com.
	if ag.last == nil {
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

	err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
	if got := drCodeOf(t, err); got != "name_taken" {
		t.Fatalf("code = %q, want name_taken (conflict from unique index)", got)
	}
}

// The file move is the FIRST step; if it fails, nothing is persisted — no DB
// rename, no tombstone, the row is untouched.
func TestRenameDomain_MoveFailureBeforeCommit(t *testing.T) {
	rec := &drRecorder{}
	dom := drWebDomain()
	d, _, td := drNewDeps(rec, dom, drHappyOwner())
	d.Agent.(*drAgent).callErr = errors.New("agent unreachable")

	err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com")
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

	if err := RenameDomain(context.Background(), d, &drSched{}, dom, "new.com"); err != nil {
		t.Fatalf("a post-commit heal failure must not fail the rename, got %v", err)
	}
	if dom.Name != "new.com" {
		t.Fatalf("row should be renamed despite heal failure, got %q", dom.Name)
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
