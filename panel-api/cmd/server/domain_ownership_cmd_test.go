package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type ownershipZoneFinder struct {
	d   *models.Domain
	err error
}

func (f ownershipZoneFinder) FindByID(context.Context, string) (*models.Domain, error) {
	return f.d, f.err
}

// GH #1816 / ADR-0170: the DNS-01 hook never republishes the zone of a
// domain whose owner has not proven the name, and a lookup failure refuses
// too.
func TestZonePushAllowed(t *testing.T) {
	zone := &models.DNSZone{ID: "z1", DomainID: "d1", Name: "example.com"}
	verified := &models.Domain{ID: "d1", Name: "example.com",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}}
	pending := &models.Domain{ID: "d1", Name: "example.com",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipPending}}

	if err := zonePushAllowed(context.Background(), ownershipZoneFinder{d: verified}, zone); err != nil {
		t.Fatalf("a verified domain's zone must be pushed, got %v", err)
	}
	err := zonePushAllowed(context.Background(), ownershipZoneFinder{d: pending}, zone)
	if err == nil || !strings.Contains(err.Error(), "not proven") {
		t.Fatalf("a pending domain's zone must not be pushed, got %v", err)
	}
	if err := zonePushAllowed(context.Background(), ownershipZoneFinder{err: errors.New("db down")}, zone); err == nil {
		t.Fatal("a failed domain lookup must refuse the push")
	}
}

// GH #1816: the recursor backfill wants a forward only for an enabled,
// proven domain, so a pending domain's forward is planned for removal.
func TestBackfillWantsForward(t *testing.T) {
	verified := models.OwnershipState{OwnershipStatus: models.OwnershipVerified}
	pending := models.OwnershipState{OwnershipStatus: models.OwnershipPending}
	cases := []struct {
		name string
		d    models.Domain
		want bool
	}{
		{"enabled and verified", models.Domain{IsEnabled: true, OwnershipState: verified}, true},
		{"enabled but pending", models.Domain{IsEnabled: true, OwnershipState: pending}, false},
		{"disabled", models.Domain{IsEnabled: false, OwnershipState: verified}, false},
	}
	for _, tc := range cases {
		if got := backfillWantsForward(&tc.d); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	plan := computeBackfillPlan(map[string]bool{}, map[string]actualForwarder{
		"pending.example": {Addr: backfillForwarderAddr, Port: backfillForwarderPort},
	})
	if len(plan) != 1 || plan[0].Action != "remove" {
		t.Fatalf("a forward with no wanted domain must be removed, got %+v", plan)
	}
}

// GH #1816: the CLI doors that insert a domain row themselves (the panel's
// own hostname, a root docker-app install) stamp it as an administrator's
// create before the insert.
func TestCLIDomainInsertsStampOwnership(t *testing.T) {
	for file, create := range map[string]string{
		"panel_primary_cmd.go":     "domains.Create(ctx, d)",
		"docker_app_tenant_cmd.go": "domRepo.Create(ctx, dom)",
	} {
		src := stripLineComments(readGoSource(t, file))
		at := strings.Index(src, create)
		if at < 0 {
			t.Fatalf("%s: expected %s", file, create)
		}
		stamp := strings.LastIndex(src[:at], "domainops.StampOwnership(")
		if stamp < 0 || !strings.Contains(src[stamp:at], ", true, ") {
			t.Fatalf("%s: the insert must be preceded by an admin StampOwnership", file)
		}
	}
}

// GH #1816: switching ownership proof off from the CLI needs --yes, and a
// word other than on/off is refused, before anything is written.
func TestDomainOwnershipPolicyCmd_OffNeedsYes(t *testing.T) {
	cmd := newDomainOwnershipPolicyCmd()
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, []string{"off"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("off without --yes: %v", err)
	}
	if err := cmd.RunE(cmd, []string{"maybe"}); err == nil || !strings.Contains(err.Error(), "want on or off") {
		t.Fatalf("an unknown word: %v", err)
	}
}

// The admin approve and revoke leave an audit row.
func TestDomainOwnershipCmd_AdminActionsAreAudited(t *testing.T) {
	src := stripLineComments(readGoSource(t, "domain_ownership_cmd.go"))
	for _, action := range []string{`"domain.ownership.approve"`, `"domain.ownership.revoke"`, `"domain.ownership.policy"`} {
		if !strings.Contains(src, "cliAuditOK(ctx, "+action) {
			t.Errorf("%s is not audited", action)
		}
	}
}

// GH #1816: the ownership service run by a CLI command audits its own
// changes (the cascade after an approve, a DNS proof) through a recorder that
// writes at once and keeps the "system" actor.
func TestDomainOwnershipCmd_ServiceChangesAreAudited(t *testing.T) {
	src := stripLineComments(readGoSource(t, "domain_ownership_cmd.go"))
	if !strings.Contains(src, "Audit:    newCLIAuditRecorder(),") {
		t.Fatal("cliOwnershipService must hand the service a CLI audit recorder")
	}

	var got []*models.AuditEvent
	rec := cliAuditRecorder{create: func(_ context.Context, e *models.AuditEvent) error {
		got = append(got, e)
		return nil
	}}
	rec.Record(audit.DomainOwnershipSystem("u1", "domain.ownership.verify", "domain", "d2", map[string]any{"method": "parent"}))
	if len(got) != 1 {
		t.Fatalf("Record must write before it returns, got %d events", len(got))
	}
	e := got[0]
	if e.ID == "" || e.TS.IsZero() || e.Result != models.AuditResultOK {
		t.Errorf("defaults not stamped: id %q ts %v result %q", e.ID, e.TS, e.Result)
	}
	if e.ActorKind != models.AuditActorSystem || e.ActorUserID != nil {
		t.Errorf("actor = %s/%v, want system with no user", e.ActorKind, e.ActorUserID)
	}
	if e.SubjectUserID == nil || *e.SubjectUserID != "u1" || e.TargetID != "d2" {
		t.Errorf("subject/target = %v/%s", e.SubjectUserID, e.TargetID)
	}
}

// GH #1816: a CLI revoke clears the mail login cache like the API one, so
// the revoked domain's mailboxes lose webmail at once. The command sets up
// the agent client and the service is handed it.
func TestDomainOwnershipRevokeCmd_ReachesTheAgent(t *testing.T) {
	cmd := newDomainOwnershipRevokeCmd()
	if cmd.PreRunE == nil || reflect.ValueOf(cmd.PreRunE).Pointer() != reflect.ValueOf(requireDBAndAgent).Pointer() {
		t.Fatal("revoke must run requireDBAndAgent: without the agent, the webmail logins of the revoked domain's mailboxes survive")
	}
	src := stripLineComments(readGoSource(t, "domain_ownership_cmd.go"))
	if !strings.Contains(src, "d.Agent = sharedAgent") {
		t.Fatal("cliOwnershipService must hand the service the agent client")
	}
}

// GH #1816: `jabali update` converges the Stalwart Directory queries again
// after the migrations. "provision new software" runs the converger before
// them, and on a box that had not migrated domains.ownership_status yet it
// keeps the unfiltered queries; without this step the login and recipient
// filters would only land on the next update.
func TestUpdate_ConvergesStalwartDirectoryAfterMigrations(t *testing.T) {
	src := stripLineComments(readGoSource(t, "update.go"))
	migrate := strings.Index(src, `{"run migrations", func() error {`)
	converge := strings.Index(src, `{"converge Stalwart directory queries", func() error {`)
	if migrate < 0 || converge < 0 {
		t.Fatalf("steps not found: run migrations at %d, converge at %d", migrate, converge)
	}
	if converge < migrate {
		t.Fatal("the Stalwart directory converge step must run after the migrations")
	}
	step := src[converge:]
	if end := strings.Index(step[1:], `{"`); end > 0 {
		step = step[:end+1]
	}
	if !strings.Contains(step, "converge_stalwart_directory_queries") {
		t.Fatal("the step must call install.sh's converge_stalwart_directory_queries")
	}
}
