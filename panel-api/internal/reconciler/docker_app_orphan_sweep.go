package reconciler

import (
	"context"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// dockerAppVhostSuffix names a Docker app's proxy vhost in sites-enabled:
// `<domain>-dockerapp`, written by docker_app.vhost_apply from the domain's
// Docker-app row.
const dockerAppVhostSuffix = "-dockerapp"

// dockerAppOrphanGrace is how long a Docker app's proxy vhost must have had no
// Docker-app domain row before the sweep removes it. An app edit and a
// reinstall delete a row and create it again under the same name; the grace
// keeps the sweep out of that window.
const dockerAppOrphanGrace = 2 * time.Minute

const dockerAppOrphanCallTimeout = 30 * time.Second

// dockerAppOrphan is a Docker app proxy vhost the sweep found with no
// Docker-app domain row.
type dockerAppOrphan struct {
	since  time.Time // the first pass that found it so
	warned bool      // a failed removal was already logged at Warn
}

// sweepDockerAppSite reports whether site is a Docker app's proxy vhost. Such a
// vhost is never an orphan site:
//   - a Docker-app row of its name, enabled or disabled, owns it;
//   - a pending teardown of its name removes it (domain.delete reaps it);
//   - otherwise it was left behind by a row deleted before its vhost (an app's
//     domain change or delete on an older release). Nothing else removes it,
//     and it keeps serving its old name, so once it has had no Docker-app row
//     for dockerAppOrphanGrace it is removed.
//
// With no row of its name at all, the removal takes the name's certificate
// files too, unless claimed reports that the panel holds a certificate under
// that name for something else. With a row that isn't a Docker app's, only
// the vhost goes: it sorts before `<domain>.conf`, so nginx would answer that
// domain with it, and the certificate is that domain's. The name pattern is
// only ever written by Jabali. orphans collects the names found without a
// Docker-app row in this pass.
func (r *Reconciler) sweepDockerAppSite(ctx context.Context, site string, rowFor func(string) *models.Domain,
	pendingTeardowns, orphans map[string]bool, claimed func(string) bool) bool {
	domain, ok := strings.CutSuffix(site, dockerAppVhostSuffix)
	if !ok || domain == "" {
		return false
	}
	row := rowFor(domain)
	if pendingTeardowns[domain] || (row != nil && row.ManagedBy == models.DomainManagedByDockerApp) {
		return true
	}
	orphans[domain] = true
	if !r.dockerAppOrphanDue(domain, time.Now()) {
		return true
	}
	params := map[string]any{"domain_name": domain}
	purge := row == nil && !claimed(domain)
	if purge {
		params["purge_certificates"] = true
	}
	cctx, cancel := context.WithTimeout(ctx, dockerAppOrphanCallTimeout)
	defer cancel()
	if _, err := r.agent.Call(cctx, "docker_app.vhost_remove", params); err != nil {
		if r.dockerAppOrphanFailed(domain) {
			r.log.Warn("docker-app: removing a leftover proxy vhost failed — will retry", "domain", domain, "err", err)
		} else {
			r.log.Debug("docker-app: removing a leftover proxy vhost failed — will retry", "domain", domain, "err", err)
		}
		return true
	}
	r.forgetDockerAppOrphan(domain)
	r.log.Info("docker-app: removed a leftover proxy vhost", "domain", domain, "purge_certificates", purge)
	return true
}

// dockerAppOrphanDue records domain as found without a Docker-app row and
// reports whether it has been so for dockerAppOrphanGrace.
func (r *Reconciler) dockerAppOrphanDue(domain string, now time.Time) bool {
	r.dockerAppOrphanMu.Lock()
	defer r.dockerAppOrphanMu.Unlock()
	if r.dockerAppOrphanSeen == nil {
		r.dockerAppOrphanSeen = map[string]*dockerAppOrphan{}
	}
	o := r.dockerAppOrphanSeen[domain]
	if o == nil {
		r.dockerAppOrphanSeen[domain] = &dockerAppOrphan{since: now}
		return false
	}
	return now.Sub(o.since) >= dockerAppOrphanGrace
}

// dockerAppOrphanFailed reports whether a failed removal of domain is the
// first one to log at Warn; later ones log at Debug.
func (r *Reconciler) dockerAppOrphanFailed(domain string) bool {
	r.dockerAppOrphanMu.Lock()
	defer r.dockerAppOrphanMu.Unlock()
	o := r.dockerAppOrphanSeen[domain]
	if o == nil || o.warned {
		return false
	}
	o.warned = true
	return true
}

func (r *Reconciler) forgetDockerAppOrphan(domain string) {
	r.dockerAppOrphanMu.Lock()
	defer r.dockerAppOrphanMu.Unlock()
	delete(r.dockerAppOrphanSeen, domain)
}

// keepDockerAppOrphans drops every recorded name not in orphans, so a vhost
// that had a row again, or was gone, in between starts its grace again.
func (r *Reconciler) keepDockerAppOrphans(orphans map[string]bool) {
	r.dockerAppOrphanMu.Lock()
	defer r.dockerAppOrphanMu.Unlock()
	for name := range r.dockerAppOrphanSeen {
		if !orphans[name] {
			delete(r.dockerAppOrphanSeen, name)
		}
	}
}

// certNameClaimedFunc returns a check of whether the panel holds a
// certificate under a name for something other than a Docker app's domain:
// the panel hostname, the mail hostname, a domain's helper name (mail.<domain>
// has a certificate of its own) or a shared certificate. hasRow reports
// whether a domain row of a name exists. The names are looked up on the
// first check, once. When a lookup fails every name is claimed, so nothing is
// removed on a guess.
func (r *Reconciler) certNameClaimedFunc(ctx context.Context, hasRow func(string) bool) func(string) bool {
	var names map[string]bool
	loaded, failed := false, false
	load := func() {
		loaded = true
		names = map[string]bool{}
		if r.serverSettings == nil {
			failed = true
			return
		}
		s, err := r.serverSettings.Get(ctx)
		if err != nil || s == nil {
			failed = true
			return
		}
		for _, n := range []string{s.Hostname, models.PanelMailHostname(s.Hostname), models.EffectiveMailHostname(s.MailHostname, s.Hostname)} {
			if n != "" {
				names[strings.ToLower(n)] = true
			}
		}
		if r.sharedCerts != nil {
			certs, err := r.sharedCerts.ListAll(ctx)
			if err != nil {
				failed = true
				return
			}
			for i := range certs {
				names[strings.ToLower(certs[i].Name)] = true
				names[strings.ToLower(certs[i].ACMELineageName())] = true
			}
		}
	}
	return func(name string) bool {
		if !loaded {
			load()
		}
		if failed || names[name] {
			return true
		}
		for _, p := range domainops.AliasHelperPrefixes() {
			if base, ok := strings.CutPrefix(name, p); ok && base != "" && hasRow(base) {
				return true
			}
		}
		return false
	}
}
