package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The steady-state test proves the gated projections are skipped when
// nothing changed. These prove the other direction for each of them: a
// change still reaches the Agent on the next tick, in the order the
// passes depend on, and a failed apply stays dirty and is retried.

// failNextAgent fails the next n calls of one method, after recording them
// in the wrapped fakeAgent like any other call.
type failNextAgent struct {
	*fakeAgent
	method string
	n      int
}

func (a *failNextAgent) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := a.fakeAgent.Call(ctx, method, params)
	a.fakeAgent.mu.Lock()
	defer a.fakeAgent.mu.Unlock()
	if method == a.method && a.n > 0 {
		a.n--
		return nil, errors.New("injected agent failure")
	}
	return raw, err
}

// tickCalls runs one normal tick and returns the calls it sent.
func tickCalls(t *testing.T, r *Reconciler, ag *fakeAgent) ([]fakeCall, Report) {
	t.Helper()
	ag.mu.Lock()
	from := len(ag.calls)
	ag.mu.Unlock()
	rep, err := r.Run(context.Background(), RunNormal)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return append([]fakeCall(nil), ag.calls[from:]...), rep
}

// indexesOf returns the positions of the calls to method whose params have
// key set to value (any value when key is "").
func indexesOf(calls []fakeCall, method, key, value string) []int {
	var out []int
	for i, c := range calls {
		if c.method != method {
			continue
		}
		if key != "" {
			p, _ := c.params.(map[string]any)
			if v, _ := p[key].(string); v != value {
				continue
			}
		}
		out = append(out, i)
	}
	return out
}

func countOf(calls []fakeCall, method string) int {
	return len(indexesOf(calls, method, "", ""))
}

func domainByID(t *testing.T, r *Reconciler, id string) *models.Domain {
	t.Helper()
	d, ok := r.domains.(*fakeDomainRepo).domains[id]
	if !ok {
		t.Fatalf("fixture has no domain %s", id)
	}
	return d
}

// rps N→0. The first fragment apply of the tick fails, because the
// Agent's nginx -t still sees the old vhost referencing the zone and
// rolls the fragment back. It must not be stamped: after the domain loop
// re-renders the vhost without limit_req, the post-loop call must send the
// fragment again.
func TestNginxRateLimits_ZoneDropLandsAfterTheVhostStopsUsingIt(t *testing.T) {
	r, ag := steadyStateFixture(t, false)
	tickCalls(t, r, ag)

	domainByID(t, r, "domain-2").RateLimitRPS = 0
	r.agent = &failNextAgent{fakeAgent: ag, method: "nginx.ratelimits.apply", n: 1}
	calls, rep := tickCalls(t, r, ag)

	applies := indexesOf(calls, "nginx.ratelimits.apply", "", "")
	vhost := indexesOf(calls, "domain.create", "domain", "shop.example.net")
	if len(applies) != 2 || len(vhost) != 1 {
		t.Fatalf("want 2 fragment applies around 1 vhost render, got applies=%v vhost=%v", applies, vhost)
	}
	if !(applies[0] < vhost[0] && vhost[0] < applies[1]) {
		t.Fatalf("order: fragment apply %d, vhost %d, fragment apply %d; the retry must follow the vhost", applies[0], vhost[0], applies[1])
	}
	if c := rep.Phases[PhaseNginxRateLimits.Name]; c.Failed != 1 || c.Applied != 1 {
		t.Errorf("report: %+v, want failed=1 applied=1", c)
	}

	calls, _ = tickCalls(t, r, ag)
	if n := countOf(calls, "nginx.ratelimits.apply"); n != 0 {
		t.Errorf("the next steady tick re-sent the fragment %d times", n)
	}
}

// rps 0→N. The zone must be declared before the vhost that references it
// is written, and the post-loop call has nothing left to do.
func TestNginxRateLimits_NewZoneIsDeclaredBeforeTheVhost(t *testing.T) {
	r, ag := steadyStateFixture(t, false)
	domainByID(t, r, "domain-2").RateLimitRPS = 0
	tickCalls(t, r, ag)

	domainByID(t, r, "domain-2").RateLimitRPS = 5
	calls, _ := tickCalls(t, r, ag)

	applies := indexesOf(calls, "nginx.ratelimits.apply", "", "")
	vhost := indexesOf(calls, "domain.create", "domain", "shop.example.net")
	if len(applies) != 1 || len(vhost) != 1 {
		t.Fatalf("want 1 fragment apply and 1 vhost render, got applies=%v vhost=%v", applies, vhost)
	}
	if applies[0] > vhost[0] {
		t.Fatalf("the vhost (call %d) was written before its zone was declared (call %d)", vhost[0], applies[0])
	}
}

// ReconcileOne is the path a freshly created domain takes (GH #896). It
// re-sends the forwarder even when this process already added the zone,
// so a domain deleted and re-created under the same name resolves at once.
func TestRecursorForward_ReconcileOneForcesTheForwarder(t *testing.T) {
	r, ag := steadyStateFixture(t, false)
	tickCalls(t, r, ag)

	ag.mu.Lock()
	from := len(ag.calls)
	ag.mu.Unlock()
	if err := r.ReconcileOne(context.Background(), "domain-1"); err != nil {
		t.Fatal(err)
	}
	ag.mu.Lock()
	calls := append([]fakeCall(nil), ag.calls[from:]...)
	ag.mu.Unlock()
	if len(indexesOf(calls, "pdns.recursor_add_zone", "zone", "example.com")) != 1 {
		t.Fatal("ReconcileOne must re-send the zone's forwarder")
	}
}

func TestRecursorForward_FailedAddIsRetried(t *testing.T) {
	r, ag := steadyStateFixture(t, false)
	// The self-zone is the first forwarder a tick adds.
	r.agent = &failNextAgent{fakeAgent: ag, method: "pdns.recursor_add_zone", n: 1}
	calls, _ := tickCalls(t, r, ag)
	if len(indexesOf(calls, "pdns.recursor_add_zone", "zone", "panel.example.com")) != 1 {
		t.Fatal("precondition: the first tick tried the self-zone")
	}

	calls, rep := tickCalls(t, r, ag)
	adds := indexesOf(calls, "pdns.recursor_add_zone", "", "")
	if len(adds) != 1 || len(indexesOf(calls, "pdns.recursor_add_zone", "zone", "panel.example.com")) != 1 {
		t.Fatalf("the next tick must retry only the failed forwarder, got %d adds", len(adds))
	}
	if c := rep.Phases[PhaseDNSRecursor.Name]; c.Applied != 1 || c.Skipped == 0 {
		t.Errorf("report: %+v", c)
	}
}

// An add and a removal of the same zone share one ledger entry, so neither
// can hide the other.
func TestRecursorForward_AddAndRemovalReplaceEachOther(t *testing.T) {
	r, ag := steadyStateFixture(t, false)
	calls, _ := tickCalls(t, r, ag)
	if len(indexesOf(calls, "pdns.recursor_remove_zone", "zone", "foo.bar.com")) != 1 {
		t.Fatal("precondition: the orphan site's forwarder is removed")
	}

	// The orphan becomes a real domain: its forwarder must be added.
	domains := r.domains.(*fakeDomainRepo)
	orphan := *domainByID(t, r, "domain-1")
	orphan.ID, orphan.Name = "domain-3", "foo.bar.com"
	orphan.DocRoot = "/home/alice/domains/foo.bar.com/public_html"
	domains.domains[orphan.ID] = &orphan
	calls, _ = tickCalls(t, r, ag)
	if len(indexesOf(calls, "pdns.recursor_add_zone", "zone", "foo.bar.com")) != 1 {
		t.Fatal("a zone whose forwarder was removed must be added once it has a domain")
	}

	// A domain row disappears while the Agent still lists its site: its
	// forwarder must be removed.
	restored := *domainByID(t, r, "domain-1")
	delete(domains.domains, "domain-1")
	calls, _ = tickCalls(t, r, ag)
	if len(indexesOf(calls, "pdns.recursor_remove_zone", "zone", "example.com")) != 1 {
		t.Fatal("a zone whose forwarder was added must be removed once it is orphaned")
	}

	// The row comes back within the audit interval of the first add. The
	// ledger's last word on the zone is the removal, so the add is sent.
	domains.domains[restored.ID] = &restored
	calls, _ = tickCalls(t, r, ag)
	if len(indexesOf(calls, "pdns.recursor_add_zone", "zone", "example.com")) != 1 {
		t.Fatal("a zone re-added after its forwarder was removed must be added again")
	}
}

func TestWebmail_ChangesStillReachTheAgent(t *testing.T) {
	r, ag := steadyStateFixture(t, true)
	settings := r.serverSettings.(*fakeServerSettingsRepo).settings
	dom := domainByID(t, r, "domain-1")
	tickCalls(t, r, ag)

	dom.EmailEnabled = false
	calls, _ := tickCalls(t, r, ag)
	if len(indexesOf(calls, "webmail.vhost_remove", "domain_name", "example.com")) != 1 {
		t.Fatal("turning a domain's email off must remove its webmail vhost")
	}

	dom.EmailEnabled = true
	calls, _ = tickCalls(t, r, ag)
	if len(indexesOf(calls, "webmail.vhost_apply", "domain_name", "example.com")) != 1 {
		t.Fatal("turning a domain's email back on must re-apply its webmail vhost")
	}

	// Any input to the vhost is part of its fingerprint: a new certificate
	// path re-applies it.
	cert := r.sslCerts.(*fakeSSLCertRepo).byDomain["domain-1"]
	newPath := "/etc/jabali/ssl/example.com/fullchain-2.pem"
	cert.CertPath = &newPath
	calls, _ = tickCalls(t, r, ag)
	if len(indexesOf(calls, "webmail.vhost_apply", "domain_name", "example.com")) != 1 {
		t.Fatal("a new certificate path must re-apply the webmail vhost")
	}

	settings.WebmailEnabled = false
	calls, _ = tickCalls(t, r, ag)
	if countOf(calls, "service.stop") != 1 || countOf(calls, "service.disable") != 1 {
		t.Fatalf("turning webmail off server-wide must stop and disable the daemon: stop=%d disable=%d",
			countOf(calls, "service.stop"), countOf(calls, "service.disable"))
	}
	if len(indexesOf(calls, "webmail.vhost_remove", "domain_name", "example.com")) != 1 {
		t.Fatal("turning webmail off server-wide must remove the domain's webmail vhost")
	}

	settings.WebmailEnabled = true
	calls, _ = tickCalls(t, r, ag)
	if countOf(calls, "service.start") != 1 || countOf(calls, "service.enable") != 1 {
		t.Fatalf("turning webmail back on must start and enable the daemon: start=%d enable=%d",
			countOf(calls, "service.start"), countOf(calls, "service.enable"))
	}
	if len(indexesOf(calls, "webmail.vhost_apply", "domain_name", "example.com")) != 1 {
		t.Fatal("turning webmail back on must re-apply the domain's webmail vhost")
	}
}

func TestWebmailVhost_FailedApplyIsRetried(t *testing.T) {
	r, ag := steadyStateFixture(t, true)
	r.agent = &failNextAgent{fakeAgent: ag, method: "webmail.vhost_apply", n: 1}
	tickCalls(t, r, ag)

	calls, rep := tickCalls(t, r, ag)
	if len(indexesOf(calls, "webmail.vhost_apply", "domain_name", "example.com")) != 1 {
		t.Fatal("a tick after a failed webmail.vhost_apply must retry it")
	}
	if c := rep.Phases[PhaseWebmailVhost.Name]; c.Applied != 1 {
		t.Errorf("report: %+v", c)
	}
}

// The daemon state is stamped only when both of its calls succeed.
func TestWebmailDaemon_HalfAppliedStateIsRetried(t *testing.T) {
	r, ag := steadyStateFixture(t, true)
	r.agent = &failNextAgent{fakeAgent: ag, method: "service.enable", n: 1}
	tickCalls(t, r, ag)

	calls, rep := tickCalls(t, r, ag)
	if countOf(calls, "service.start") != 1 || countOf(calls, "service.enable") != 1 {
		t.Fatal("a tick after a failed service.enable must retry the daemon state")
	}
	if c := rep.Phases[PhaseWebmailDaemon.Name]; c.Applied != 1 {
		t.Errorf("report: %+v", c)
	}
	calls, _ = tickCalls(t, r, ag)
	if countOf(calls, "service.start")+countOf(calls, "service.enable") != 0 {
		t.Fatal("once applied, the daemon state must be skipped")
	}
}

// A deleted user changes the keep set, so the orphan pool sweep runs on the
// very next tick.
func TestOrphanFPMReap_RunsAsSoonAsAUserIsDeleted(t *testing.T) {
	r, ag := steadyStateFixture(t, false)
	users := r.users.(*fakeUserRepo)
	bob := "bob"
	users.users["user-2"] = &models.User{ID: "user-2", Email: "bob@example.com", Username: &bob}
	tickCalls(t, r, ag)
	if calls, _ := tickCalls(t, r, ag); countOf(calls, "php.pool.reap-orphans") != 0 {
		t.Fatal("precondition: an unchanged user set skips the sweep")
	}

	delete(users.users, "user-2")
	calls, _ := tickCalls(t, r, ag)
	reaps := indexesOf(calls, "php.pool.reap-orphans", "", "")
	if len(reaps) != 1 {
		t.Fatalf("a deleted user must trigger the sweep, got %d calls", len(reaps))
	}
	keep, _ := calls[reaps[0]].params.(map[string]any)["keep_usernames"].([]string)
	if len(keep) != 1 || keep[0] != "alice" {
		t.Errorf("keep_usernames = %v, want [alice]", keep)
	}
}

// An audit run re-sends every projection the steady tick skips.
func TestRun_AuditResendsEveryGatedProjection(t *testing.T) {
	r, ag := steadyStateFixture(t, true)
	tickCalls(t, r, ag)
	tickCalls(t, r, ag)

	ag.mu.Lock()
	from := len(ag.calls)
	ag.mu.Unlock()
	if _, err := r.Run(context.Background(), RunAudit); err != nil {
		t.Fatal(err)
	}
	ag.mu.Lock()
	calls := append([]fakeCall(nil), ag.calls[from:]...)
	ag.mu.Unlock()
	for _, m := range []string{
		"nginx.ratelimits.apply", "pdns.recursor_add_zone", "pdns.recursor_remove_zone",
		"php.pool.reap-orphans", "webmail.vhost_apply", "webmail.vhost_remove",
		"service.start", "service.enable",
	} {
		if countOf(calls, m) == 0 {
			t.Errorf("an audit run did not re-send %s", m)
		}
	}
}
