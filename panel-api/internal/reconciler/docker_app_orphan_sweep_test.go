package reconciler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// A Docker app's proxy vhost (`<domain>-dockerapp`) belongs to its Docker-app
// domain row, enabled or disabled. It is not an orphan site, and its name gets
// no recursor forwarder removal. Left over from a row deleted before its vhost
// (an app domain change or delete on an older release), it keeps serving its
// old name: once it has had no Docker-app row for dockerAppOrphanGrace it is
// removed.

type fakeSharedCertList struct {
	repository.SharedCertificateRepository
	certs []models.SharedCertificate
	err   error
}

func (f *fakeSharedCertList) ListAll(context.Context) ([]models.SharedCertificate, error) {
	return f.certs, f.err
}

func dockerAppRow(id, name string) *models.Domain {
	appID := "app-" + id
	return &models.Domain{ID: id, Name: name, ManagedBy: models.DomainManagedByDockerApp, DockerAppID: &appID}
}

// vhostRemoves returns the docker_app.vhost_remove calls as domain -> purge flag.
func vhostRemoves(ag *fakeMTAStsAgent) map[string]bool {
	out := map[string]bool{}
	for _, c := range ag.calls {
		if c.Command != "docker_app.vhost_remove" {
			continue
		}
		purge, _ := c.Params["purge_certificates"].(bool)
		out[c.Params["domain_name"].(string)] = purge
	}
	return out
}

// agedDockerAppOrphans marks names as orphaned for longer than the grace.
func agedDockerAppOrphans(r *Reconciler, names ...string) {
	r.dockerAppOrphanMu.Lock()
	defer r.dockerAppOrphanMu.Unlock()
	if r.dockerAppOrphanSeen == nil {
		r.dockerAppOrphanSeen = map[string]*dockerAppOrphan{}
	}
	for _, n := range names {
		r.dockerAppOrphanSeen[n] = &dockerAppOrphan{since: time.Now().Add(-dockerAppOrphanGrace - time.Minute)}
	}
}

func TestSweepAgentSites_LiveDockerAppVhostsAreNotOrphans(t *testing.T) {
	r, _, _, _, ag := mtaStsReconcilerForTest(t)
	sites := map[string]bool{
		"default": true, "app.org-dockerapp": true, "off.org-dockerapp": true,
		"tomb.org-dockerapp": true, "stray.org": true,
	}
	enabled := map[string]*models.Domain{"app.org": dockerAppRow("d1", "app.org")}
	disabled := map[string]*models.Domain{"off.org": dockerAppRow("d2", "off.org")}
	agedDockerAppOrphans(r, "app.org", "off.org", "tomb.org")

	r.sweepAgentSites(context.Background(), sites, enabled, disabled, map[string]bool{"tomb.org": true})

	if got := vhostRemoves(ag); len(got) != 0 {
		t.Fatalf("vhost_remove for %v, want none", got)
	}
	for _, c := range ag.calls {
		if z, _ := c.Params["zone"].(string); strings.HasSuffix(z, "-dockerapp") {
			t.Errorf("%s for the Docker app vhost %q", c.Command, z)
		}
	}
	if r.lastOrphanKey != "stray.org" {
		t.Fatalf("orphans reported %q, want only stray.org", r.lastOrphanKey)
	}
}

func TestSweepAgentSites_LeftoverDockerAppVhostRemovedAfterGrace(t *testing.T) {
	r, _, _, _, ag := mtaStsReconcilerForTest(t)
	sites := map[string]bool{"gone.org-dockerapp": true}

	r.sweepAgentSites(context.Background(), sites, nil, nil, nil)
	if got := vhostRemoves(ag); len(got) != 0 {
		t.Fatalf("first pass removed %v; a row deleted and recreated under the same name must get the grace", got)
	}
	r.sweepAgentSites(context.Background(), sites, nil, nil, nil)
	if got := vhostRemoves(ag); len(got) != 0 {
		t.Fatalf("second pass inside the grace removed %v", got)
	}

	agedDockerAppOrphans(r, "gone.org")
	r.sweepAgentSites(context.Background(), sites, nil, nil, nil)
	got := vhostRemoves(ag)
	if purge, ok := got["gone.org"]; !ok || !purge || len(got) != 1 {
		t.Fatalf("vhost_remove calls %v, want gone.org with purge_certificates", got)
	}
	if r.lastOrphanKey != "" {
		t.Fatalf("orphans reported %q, want none", r.lastOrphanKey)
	}
}

// A leftover next to a domain that is not a Docker app's sorts before
// `<domain>.conf` and would answer for it. Only the vhost goes: the
// certificate is that domain's.
func TestSweepAgentSites_LeftoverDockerAppVhostOfAWebDomainKeepsItsCertificate(t *testing.T) {
	r, _, _, _, ag := mtaStsReconcilerForTest(t)
	sites := map[string]bool{"web.org": true, "web.org-dockerapp": true}
	enabled := map[string]*models.Domain{"web.org": {ID: "d1", Name: "web.org"}}
	agedDockerAppOrphans(r, "web.org")

	r.sweepAgentSites(context.Background(), sites, enabled, nil, nil)

	got := vhostRemoves(ag)
	if purge, ok := got["web.org"]; !ok || purge || len(got) != 1 {
		t.Fatalf("vhost_remove calls %v, want web.org without purge_certificates", got)
	}
}

// Names the panel holds a certificate for some other way keep it: the panel
// hostname, the mail hostname, a domain's helper names and shared
// certificates.
func TestSweepAgentSites_LeftoverDockerAppVhostKeepsACertificateTheNameHasElsewhere(t *testing.T) {
	r, _, _, ss, ag := mtaStsReconcilerForTest(t)
	custom := "mx.example.net"
	ss.settings.MailHostname = &custom
	r.sharedCerts = &fakeSharedCertList{certs: []models.SharedCertificate{{Name: "shared.org"}, {Name: "*.wild.org"}}}
	names := []string{
		"host.example.com", "mail.host.example.com", "mx.example.net",
		"mail.live.org", "www.off.org", "shared.org", "wildcard.wild.org",
		"free.org",
	}
	sites := map[string]bool{}
	for _, n := range names {
		sites[n+"-dockerapp"] = true
	}
	enabled := map[string]*models.Domain{"live.org": {ID: "d1", Name: "live.org"}}
	disabled := map[string]*models.Domain{"off.org": {ID: "d2", Name: "off.org"}}
	agedDockerAppOrphans(r, names...)

	r.sweepAgentSites(context.Background(), sites, enabled, disabled, nil)

	got := vhostRemoves(ag)
	if len(got) != len(names) {
		t.Fatalf("vhost_remove calls %v, want one for each of %v", got, names)
	}
	for _, n := range names {
		if want := n == "free.org"; got[n] != want {
			t.Errorf("%s: purge_certificates = %v, want %v", n, got[n], want)
		}
	}
}

// When the names can't be checked, the vhost still goes and the certificate
// stays.
func TestSweepAgentSites_LeftoverDockerAppVhostKeepsCertificateWhenLookupFails(t *testing.T) {
	for name, setup := range map[string]func(r *Reconciler, ss *fakeServerSettingsRepo){
		"settings": func(r *Reconciler, ss *fakeServerSettingsRepo) { ss.settings = nil },
		"shared certs": func(r *Reconciler, ss *fakeServerSettingsRepo) {
			r.sharedCerts = &fakeSharedCertList{err: errors.New("db down")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, _, _, ss, ag := mtaStsReconcilerForTest(t)
			setup(r, ss)
			agedDockerAppOrphans(r, "gone.org")

			r.sweepAgentSites(context.Background(), map[string]bool{"gone.org-dockerapp": true}, nil, nil, nil)

			got := vhostRemoves(ag)
			if purge, ok := got["gone.org"]; !ok || purge {
				t.Fatalf("vhost_remove calls %v, want gone.org without purge_certificates", got)
			}
		})
	}
}

// A failed removal is retried on the next pass, and the vhost still isn't
// reported as an orphan.
func TestSweepAgentSites_LeftoverDockerAppVhostRemovalFailureRetries(t *testing.T) {
	r, _, _, _, ag := mtaStsReconcilerForTest(t)
	ag.failCmd = map[string]error{"docker_app.vhost_remove": errors.New("nginx -t failed")}
	sites := map[string]bool{"gone.org-dockerapp": true}
	agedDockerAppOrphans(r, "gone.org")
	for i := 0; i < 2; i++ {
		r.sweepAgentSites(context.Background(), sites, nil, nil, nil)
	}
	n := 0
	for _, c := range ag.calls {
		if c.Command == "docker_app.vhost_remove" {
			n++
		}
	}
	if n != 2 || r.lastOrphanKey != "" {
		t.Fatalf("removal attempts %d, orphans %q; want 2 attempts and no orphan", n, r.lastOrphanKey)
	}
}

// The grace starts again for a name that came back: a vhost that had a row
// again in between is not removed the moment it loses it.
func TestSweepAgentSites_DockerAppOrphanGraceRestartsWhenTheVhostHadARow(t *testing.T) {
	r, _, _, _, ag := mtaStsReconcilerForTest(t)
	sites := map[string]bool{"app.org-dockerapp": true}
	agedDockerAppOrphans(r, "app.org")

	r.sweepAgentSites(context.Background(), sites, map[string]*models.Domain{"app.org": dockerAppRow("d1", "app.org")}, nil, nil)
	r.sweepAgentSites(context.Background(), sites, nil, nil, nil)

	if got := vhostRemoves(ag); len(got) != 0 {
		t.Fatalf("vhost_remove for %v right after the row went; want the grace first", got)
	}
}

// The same holds for a vhost that was gone from sites-enabled in between.
func TestSweepAgentSites_DockerAppOrphanGraceRestartsWhenTheVhostWasGone(t *testing.T) {
	r, _, _, _, ag := mtaStsReconcilerForTest(t)
	agedDockerAppOrphans(r, "app.org")

	r.sweepAgentSites(context.Background(), map[string]bool{}, nil, nil, nil)
	r.sweepAgentSites(context.Background(), map[string]bool{"app.org-dockerapp": true}, nil, nil, nil)

	if got := vhostRemoves(ag); len(got) != 0 {
		t.Fatalf("vhost_remove for %v on its first pass back; want the grace first", got)
	}
}

// A Docker app's domain whose vhost is in sites-enabled is not logged as
// missing on every pass: the vhost is `<domain>-dockerapp`.
func TestReconcileEnabledDomain_DockerAppVhostPresentIsNotLoggedAsMissing(t *testing.T) {
	for _, tc := range []struct {
		sites map[string]bool
		want  bool
	}{
		{map[string]bool{"example.com-dockerapp": true}, false},
		{map[string]bool{}, true},
	} {
		r, _, dom := plannerFixture(t)
		dom.ManagedBy = models.DomainManagedByDockerApp
		dom.DocRoot = ""
		var buf bytes.Buffer
		r.log = slog.New(slog.NewTextHandler(&buf, nil))

		r.reconcileEnabledDomain(context.Background(), dom.Name, dom, tc.sites)

		if got := strings.Contains(buf.String(), "reconcile: docker-app proxy domain"); got != tc.want {
			t.Errorf("sites %v: logged as missing = %v, want %v", tc.sites, got, tc.want)
		}
	}
}
