package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// mailElsewhereResync re-sends an unchanged list, so a domain the mail server
// registered again (or a lookup key or relay switch changed on the server by
// hand) is put right within ten minutes.
const mailElsewhereResync = 10 * time.Minute

// reconcileMailElsewhere sends the agent the domains this server must not act
// as the mail server for (models.MailHostedElsewhere: email off, or mail
// hosted by another provider). The agent keeps them in Stalwart's in-memory
// lookup "jabali-mail-elsewhere", which the installer's route and RCPT
// expressions read: mail from this server to such a domain goes to its MX, and
// unauthenticated mail for it is refused. See panel-agent
// mail_elsewhere_sync.go.
//
// The full list goes every time; the agent removes what is not listed. The
// call is skipped while the list is unchanged and was synced less than
// mailElsewhereResync ago. A failed or skipped call (Stalwart not reachable)
// is retried next tick.
func (r *Reconciler) reconcileMailElsewhere(ctx context.Context) {
	if r.domains == nil || r.agent == nil || r.serverSettings == nil {
		return
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.settingsGet(sctx)
	scancel()
	if err != nil || srv == nil || !srv.MailEnabled {
		return
	}

	domains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("mail-elsewhere: list domains failed", "error", err)
		return
	}
	seen := map[string]bool{}
	names := []string{}
	for i := range domains {
		d := &domains[i]
		if !models.MailHostedElsewhere(d.EmailEnabled, d.MailProvider) {
			continue
		}
		name := strings.ToLower(d.Name)
		if domainops.ValidateDomainName(name) != nil {
			r.log.Warn("mail-elsewhere: skipping a domain name the mail server would refuse", "domain", d.Name)
			continue
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	h := sha256.Sum256([]byte(strings.Join(names, "\n")))
	sum := hex.EncodeToString(h[:])

	r.mailElsewhereMu.Lock()
	fresh := sum == r.mailElsewhereSum && time.Since(r.mailElsewhereAt) < mailElsewhereResync
	r.mailElsewhereMu.Unlock()
	if fresh {
		return
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	raw, err := r.agent.Call(cctx, "mail.elsewhere.sync", map[string]any{"domains": names})
	cancel()
	if err != nil {
		r.log.Warn("mail-elsewhere: sync failed, will retry next tick", "domains", len(names), "error", err)
		return
	}
	var resp struct {
		Skipped     bool `json:"skipped"`
		Routing     bool `json:"routing"`
		KeysAdded   int  `json:"keys_added"`
		KeysRemoved int  `json:"keys_removed"`
		RelayOn     int  `json:"relay_on"`
		RelayOff    int  `json:"relay_off"`
	}
	_ = json.Unmarshal(raw, &resp)
	if resp.Skipped {
		r.log.Debug("mail-elsewhere: mail server not reachable, retrying next tick")
		return
	}
	if resp.KeysAdded+resp.KeysRemoved+resp.RelayOn+resp.RelayOff > 0 {
		r.log.Info("mail-elsewhere: mail server updated", "domains", len(names),
			"keys_added", resp.KeysAdded, "keys_removed", resp.KeysRemoved,
			"relay_on", resp.RelayOn, "relay_off", resp.RelayOff, "routing", resp.Routing)
	}
	if !resp.Routing && len(names) > 0 {
		r.log.Warn("mail-elsewhere: the mail server's routing for these domains is not configured yet; run `jabali update`", "domains", len(names))
	}

	r.mailElsewhereMu.Lock()
	r.mailElsewhereSum = sum
	r.mailElsewhereAt = time.Now()
	r.mailElsewhereMu.Unlock()
}
