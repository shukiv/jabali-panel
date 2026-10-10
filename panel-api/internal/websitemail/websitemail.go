// Package websitemail builds and sends the agent's mail.relay.apply call
// (GH #2056, ADR 0174): where the sites' PHP mail() goes, and in smarthost
// mode the smarthost login and the sites allowed to use it. The website-mail
// settings endpoint applies it on save and the reconciler keeps the box on it.
package websitemail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// Command is the agent command.
const Command = "mail.relay.apply"

// Sender is one site user allowed to send through the smarthost.
type Sender struct {
	Username string   `json:"username"`
	Domains  []string `json:"domains"`
	Default  string   `json:"default"`
}

// Smarthost is the relay's login. Password is plaintext: it only travels to
// the agent over its socket, which writes it to the relay's own config.
type Smarthost struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Helo     string `json:"helo,omitempty"`
}

// ApplyRequest is mail.relay.apply's params.
type ApplyRequest struct {
	Mode      string     `json:"mode"`
	Smarthost *Smarthost `json:"smarthost,omitempty"`
	Senders   []Sender   `json:"senders,omitempty"`
}

// ApplyResponse is mail.relay.apply's result.
type ApplyResponse struct {
	Ok      bool   `json:"ok"`
	Mode    string `json:"mode"`
	Changed bool   `json:"changed"`
	Senders int    `json:"senders"`
	// Skipped names the senders the agent left out and why.
	Skipped []string `json:"skipped,omitempty"`
}

// Deps are the lookups the sender list needs.
type Deps struct {
	Users   repository.UserRepository
	Domains repository.DomainRepository
}

// ErrNoKey: the smarthost has a login but the panel can't open the sealed
// password.
var ErrNoKey = errors.New("the panel's sso.key is not configured, so the smarthost password can't be read")

// Senders lists who may send website mail through the smarthost: users with a
// Linux account and a hosting package, not admins, not suspended, with their
// enabled domains whose ownership is verified (GH #1816). A user's default
// domain is their oldest. Users without a package are left out (GH #282): the
// smarthost sends with the operator's login and reputation, a privileged
// feature.
func Senders(ctx context.Context, d Deps) ([]Sender, error) {
	domains, _, err := d.Domains.List(ctx, repository.ListOptions{Limit: 10000, OmitHeavyColumns: true})
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	byUser := map[string][]models.Domain{}
	for i := range domains {
		dom := domains[i]
		if !dom.IsEnabled || !domainops.OwnershipVerified(&dom) {
			continue
		}
		byUser[dom.UserID] = append(byUser[dom.UserID], dom)
	}
	ids := make([]string, 0, len(byUser))
	for id := range byUser {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	users, err := d.Users.FindByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("find users: %w", err)
	}

	out := []Sender{}
	for _, u := range users {
		if u.IsAdmin || u.Suspended || u.Username == nil || *u.Username == "" || u.PackageID == nil || *u.PackageID == "" {
			continue
		}
		doms := byUser[u.ID]
		sort.SliceStable(doms, func(i, j int) bool { return doms[i].CreatedAt.Before(doms[j].CreatedAt) })
		names := make([]string, 0, len(doms))
		for _, dom := range doms {
			names = append(names, strings.ToLower(dom.Name))
		}
		out = append(out, Sender{Username: *u.Username, Domains: names, Default: names[0]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

// Request builds the mail.relay.apply params for the saved settings.
func Request(ctx context.Context, d Deps, s *models.ServerSettings, key *ssokey.Key) (ApplyRequest, error) {
	if models.EffectiveWebsiteMailMode(s) != models.WebsiteMailSmarthost {
		return ApplyRequest{Mode: models.WebsiteMailLocal}, nil
	}
	sh := &Smarthost{
		Host:     s.SmarthostHost,
		Port:     s.SmarthostPort,
		TLS:      s.SmarthostTLS,
		Username: s.SmarthostUsername,
		Helo:     s.Hostname,
	}
	if sh.Username != "" {
		if key == nil {
			return ApplyRequest{}, ErrNoKey
		}
		pw, err := key.Open(s.SmarthostPasswordEnc)
		if err != nil {
			return ApplyRequest{}, fmt.Errorf("open the smarthost password: %w", err)
		}
		sh.Password = string(pw)
	}
	senders, err := Senders(ctx, d)
	if err != nil {
		return ApplyRequest{}, err
	}
	return ApplyRequest{Mode: models.WebsiteMailSmarthost, Smarthost: sh, Senders: senders}, nil
}

// Fingerprint identifies a request for the reconciler's done-cache. It
// covers the password, hashed; it is only ever kept in memory.
func Fingerprint(req ApplyRequest) string {
	b, _ := json.Marshal(req)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Apply sends the request to the agent.
func Apply(ctx context.Context, a agent.AgentInterface, req ApplyRequest) (*ApplyResponse, error) {
	raw, err := a.Call(ctx, Command, req)
	if err != nil {
		return nil, err
	}
	var resp ApplyResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode %s: %w", Command, err)
	}
	return &resp, nil
}
