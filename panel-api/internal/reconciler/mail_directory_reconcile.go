package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// mail_directory_reconcile.go — GH #1637, ADR-0171. Every domain whose mail
// the panel hosts gets a directory: one address book, owned by the Stalwart
// Group jabali-directory@<domain>, that lists the domain's mailboxes and is
// shared read-only with them. Webmail and CardDAV clients read it for
// same-domain recipient suggestions, which Stalwart's server-wide principal
// listing no longer gives a mailbox (#1605).
//
// The pass sends the whole desired state to the agent's mail.directory.apply
// and lets the ledger skip a domain whose state has not changed. A mailbox
// row changes the fingerprint when it is added, removed, renamed, disabled or
// made send-only, so the directory follows within a tick.
const (
	mailDirectoryApplyBudgetPerTick = 20
	mailDirectoryRetryInterval      = 15 * time.Minute
	mailDirectoryMaxName            = 255
)

var errMailDirectoryAddressTaken = errors.New("mail directory: the directory address is taken by a mailbox, mail group or shared resource")

// mailDirectoryEntry and mailDirectorySpec are the wire shape of
// mail.directory.apply.
type mailDirectoryEntry struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type mailDirectorySpec struct {
	HostEmail   string               `json:"host_email"`
	DisplayName string               `json:"display_name"`
	Entries     []mailDirectoryEntry `json:"entries"`
	Readers     []string             `json:"readers"`
}

// WithMailDirectory wires the directory pass. All three repos are required:
// the group and shared-resource repos guard the reserved address, so a nil
// one leaves the pass off rather than unguarded.
func (r *Reconciler) WithMailDirectory(mailboxes repository.MailboxRepository, groups repository.MailGroupRepository, resources repository.SharedResourceRepository) *Reconciler {
	r.mailDirMailboxes = mailboxes
	r.mailDirGroups = groups
	r.mailDirResources = resources
	return r
}

// mailDirectoryName is a mailbox display name as the directory lists it:
// trimmed, without control characters, at most mailDirectoryMaxName bytes.
func mailDirectoryName(raw string) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	for len(name) > mailDirectoryMaxName {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return strings.TrimSpace(name)
}

// mailDirectoryDomain reports whether the panel hosts dom's mail.
func mailDirectoryDomain(dom models.Domain) bool {
	return dom.EmailEnabled && (dom.MailProvider == "" || dom.MailProvider == models.MailProviderJabali)
}

// mailDirectoryPlan is what the pass knows about one domain's directory.
type mailDirectoryPlan struct {
	Spec mailDirectorySpec
	// ReaderIDs are the mailbox rows behind Spec.Readers. They are part of the
	// fingerprint and not of the spec: a mailbox deleted and created again at
	// the same address is a new Stalwart account, whose grant the agent must
	// add although the address list did not change.
	ReaderIDs []string
	// Needed is false when the domain needs no directory: it has no mailbox a
	// person uses, or its name does not canonicalise.
	Needed bool
	// Taken is true when a mailbox row holds the directory address.
	Taken bool
}

// fingerprint is the ledger hash of the plan.
func (p mailDirectoryPlan) fingerprint() string {
	return fingerprint(struct {
		Spec      mailDirectorySpec
		ReaderIDs []string
	}{p.Spec, p.ReaderIDs})
}

// buildMailDirectoryPlan returns the directory of dom from its mailbox rows.
func buildMailDirectoryPlan(dom models.Domain, mbs []models.Mailbox) mailDirectoryPlan {
	var p mailDirectoryPlan
	_, domain, err := mailaddr.Canonicalise(mailaddr.DirectoryLocalPart + "@" + dom.Name)
	if err != nil {
		return p
	}
	p.Spec = mailDirectorySpec{
		HostEmail:   mailaddr.DirectoryLocalPart + "@" + domain,
		DisplayName: mailDirectoryName(domain + " directory"), // capped like a mailbox name
		Entries:     []mailDirectoryEntry{},
		Readers:     []string{},
	}
	type reader struct{ email, id string }
	var readers []reader
	for _, mb := range mbs {
		if strings.EqualFold(mb.LocalPart, mailaddr.DirectoryLocalPart) {
			p.Taken = true
		}
		if mb.System || mb.SendOnly {
			continue // infrastructure principals and SMTP-only accounts
		}
		// A disabled mailbox still keeps the domain's directory, so that its
		// own grant is withdrawn.
		p.Needed = true
		if mb.IsDisabled {
			continue
		}
		local, d, err := mailaddr.Canonicalise(mb.EmailCached)
		if err != nil || local+"@"+d != mb.EmailCached || d != domain {
			continue // not in the form the agent accepts; never send what it refuses
		}
		p.Spec.Entries = append(p.Spec.Entries, mailDirectoryEntry{Email: mb.EmailCached, Name: mailDirectoryName(mb.DisplayName)})
		readers = append(readers, reader{mb.EmailCached, mb.ID})
	}
	sort.Slice(p.Spec.Entries, func(i, j int) bool { return p.Spec.Entries[i].Email < p.Spec.Entries[j].Email })
	sort.Slice(readers, func(i, j int) bool { return readers[i].email < readers[j].email })
	p.ReaderIDs = []string{}
	for _, rd := range readers {
		p.Spec.Readers = append(p.Spec.Readers, rd.email)
		p.ReaderIDs = append(p.ReaderIDs, rd.id)
	}
	return p
}

func (r *Reconciler) reconcileMailDirectories(ctx context.Context) {
	if r.agent == nil || r.mailDirMailboxes == nil || r.mailDirGroups == nil || r.mailDirResources == nil ||
		r.domains == nil || r.serverSettings == nil {
		return
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.settingsGet(sctx)
	scancel()
	if err != nil || srv == nil || !srv.MailEnabled {
		return
	}
	all, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("mail-directory: list domains failed", "error", err)
		return
	}
	var doms []models.Domain
	var ids []string
	for _, d := range all {
		if mailDirectoryDomain(d) {
			doms = append(doms, d)
			ids = append(ids, d.ID)
		} else {
			r.ledger.forget(PhaseMailDirectory, d.ID) // mail off: its accounts are purged
		}
	}
	if len(ids) == 0 {
		return
	}
	mbs, err := r.mailDirMailboxes.ListByDomainIDs(ctx, ids)
	if err != nil {
		r.log.Warn("mail-directory: load mailboxes failed", "error", err)
		return
	}
	byDomain := make(map[string][]models.Mailbox, len(ids))
	for _, mb := range mbs {
		byDomain[mb.DomainID] = append(byDomain[mb.DomainID], mb)
	}
	sort.Slice(doms, func(i, j int) bool { return doms[i].Name < doms[j].Name })

	now := time.Now()
	applied := 0
	for _, d := range doms {
		plan := buildMailDirectoryPlan(d, byDomain[d.ID])
		if !plan.Needed {
			r.ledger.forget(PhaseMailDirectory, d.ID)
			continue
		}
		if applied >= mailDirectoryApplyBudgetPerTick {
			return // the rest are applied on following ticks
		}
		if r.mailDirectoryBackingOff(d.ID, now) {
			continue
		}
		ran, err := r.project(ctx, PhaseMailDirectory, d.ID, plan.fingerprint(), false, func() error {
			if plan.Taken || r.mailDirectoryAddressTaken(ctx, d.ID) {
				return errMailDirectoryAddressTaken
			}
			return r.applyMailDirectory(ctx, plan.Spec)
		})
		if !ran {
			continue
		}
		applied++
		r.mailDirMu.Lock()
		if err != nil {
			if r.mailDirRetryAt == nil {
				r.mailDirRetryAt = map[string]time.Time{}
			}
			r.mailDirRetryAt[d.ID] = now.Add(mailDirectoryRetryInterval)
		} else {
			delete(r.mailDirRetryAt, d.ID)
		}
		r.mailDirMu.Unlock()
		if err != nil {
			r.log.Warn("mail-directory: apply failed", "domain", d.Name, "error", err)
		}
	}
}

// mailDirectoryApplyTimeout bounds one apply of n entries and readers. Each
// reader is one Stalwart query and each card a share of a batched write, so a
// large domain gets more than the minute a small one needs: a fixed budget
// would fail it on every retry.
func mailDirectoryApplyTimeout(n int) time.Duration {
	return time.Minute + time.Duration(n)*10*time.Millisecond
}

// applyMailDirectory sends spec to the agent. A reader the agent could not
// find in Stalwart is an error, so the domain is not stamped and is retried:
// that mailbox has no grant yet.
func (r *Reconciler) applyMailDirectory(ctx context.Context, spec mailDirectorySpec) error {
	cctx, cancel := context.WithTimeout(ctx, mailDirectoryApplyTimeout(len(spec.Entries)+len(spec.Readers)))
	defer cancel()
	raw, err := r.agent.Call(cctx, "mail.directory.apply", spec)
	if err != nil {
		return err
	}
	var res struct {
		ReadersUnresolved int `json:"readers_unresolved"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &res); err != nil {
			return fmt.Errorf("mail.directory.apply result: %w", err)
		}
	}
	if res.ReadersUnresolved > 0 {
		return fmt.Errorf("mail.directory.apply: %d of %d mailboxes are not in Stalwart yet", res.ReadersUnresolved, len(spec.Readers))
	}
	return nil
}

// mailDirectoryAddressTaken reports whether a mail group or shared resource
// of the domain holds the directory address. Its Stalwart principal would
// then be taken for the directory's host: a shared resource's host is a
// Group too, and the apply would replace its address book. Rows are matched
// by domain id and the local part of their address, in any case: a shared
// resource stores the domain name as it was typed. A
// lookup error counts as taken, so nothing is written on a guess.
func (r *Reconciler) mailDirectoryAddressTaken(ctx context.Context, domainID string) bool {
	if exists, err := r.mailDirGroups.ExistsByDomainAndLocalPart(ctx, domainID, mailaddr.DirectoryLocalPart); err != nil || exists {
		return true
	}
	resources, err := r.mailDirResources.ListByDomainID(ctx, domainID)
	if err != nil {
		return true
	}
	for _, sr := range resources {
		// The address is what the resource's host principal is made from;
		// a row without one has no host.
		if sr.EmailCached != nil {
			if local, _, ok := strings.Cut(*sr.EmailCached, "@"); ok && strings.EqualFold(local, mailaddr.DirectoryLocalPart) {
				return true
			}
		}
	}
	return false
}

// mailDirectoryBackingOff reports whether domainID's last apply failed less
// than mailDirectoryRetryInterval ago, so a domain that keeps failing does not
// use up every tick's budget.
func (r *Reconciler) mailDirectoryBackingOff(domainID string, now time.Time) bool {
	r.mailDirMu.Lock()
	defer r.mailDirMu.Unlock()
	until, ok := r.mailDirRetryAt[domainID]
	return ok && now.Before(until)
}
