package commands

import (
	"context"
	"encoding/json"
	"testing"
)

// Mailbox sharing: Mailbox/query and Mailbox/set are mail-capability methods.
// Without urn:ietf:params:jmap:mail in "using", Stalwart answers unknownMethod
// (verified on 0.16.15), so every share failed.
func TestMailboxShareSet_SendsMailCapability(t *testing.T) {
	f := newMGFake()
	owner := f.addAccount("alice@example.com", "User")
	target := f.addAccount("bob@example.com", "User")
	wireJMAP(t, f.server(t))

	if _, err := mailboxShareSetHandler(context.Background(), json.RawMessage(
		`{"owner_email":"alice@example.com","shares":{"bob@example.com":{"mayRead":true,"mayAdmin":true}}}`)); err != nil {
		t.Fatalf("share_set: %v", err)
	}
	sw := f.shares[owner+"/inbox-"+owner]
	rights, _ := sw[target].(map[string]any)
	if rights["mayReadItems"] != true || rights["mayShare"] != true {
		t.Fatalf("shareWith not written for %s: %#v", target, sw)
	}
}

// A Group account is linked by its members, so Stalwart refuses to destroy it
// (objectIsLinked) while a member still exists. The purge must retry it after
// the members are gone instead of leaving it behind.
func TestMailDomainPurge_DestroysGroupListedBeforeItsMembers(t *testing.T) {
	f := newMGFake()
	group := f.addAccount("team@example.com", "Group") // acct1: listed first
	member := f.addAccount("alice@example.com", "User")
	f.accounts[member].memberGroupIDs[group] = true
	wireJMAP(t, f.server(t))

	out, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"example.com"}`))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, ok := f.accounts[group]; ok {
		t.Fatalf("group account survived the purge (objectIsLinked on the first pass)")
	}
	if _, ok := f.accounts[member]; ok {
		t.Fatalf("member account survived the purge")
	}
	if got := out.(mailDomainPurgeResult).Destroyed; got != 2 {
		t.Fatalf("destroyed = %d, want 2", got)
	}
}

// Deleting a domain (remove_domain) must also remove the Stalwart domain and
// the DKIM signatures that link to it — otherwise the domain survives with the
// old owner's settings (catch-all, DKIM key) for whoever adds it next.
func TestMailDomainPurge_RemoveDomainDestroysDkimAndDomain(t *testing.T) {
	f := newMGFake()
	f.domains["other.com"] = "dom2"
	f.addAccount("alice@example.com", "User")
	f.addList("sales@example.com", "alice@example.com")
	f.dkim["sig1"] = "dom1"
	f.dkim["sig2"] = "dom1"
	f.dkim["sig3"] = "dom2"
	wireJMAP(t, f.server(t))

	out, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"example.com","remove_domain":true}`))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, ok := f.domains["example.com"]; ok {
		t.Fatalf("Stalwart domain survived a domain removal")
	}
	if _, ok := f.dkim["sig1"]; ok {
		t.Fatalf("DKIM signature of the removed domain survived")
	}
	if _, ok := f.dkim["sig3"]; !ok {
		t.Fatalf("DKIM signature of another domain was removed")
	}
	if _, ok := f.domains["other.com"]; !ok {
		t.Fatalf("another domain was removed")
	}
	res := out.(mailDomainPurgeResult)
	if !res.DomainRemoved || res.DestroyedDkim != 2 {
		t.Fatalf("result = %+v, want domain_removed and 2 DKIM signatures", res)
	}
}

// A mail-only purge (no remove_domain) keeps the Stalwart domain and its DKIM
// signatures: re-enabling mail reuses them (domain.email_disable design).
func TestMailDomainPurge_KeepsDomainWithoutRemoveFlag(t *testing.T) {
	f := newMGFake()
	f.addAccount("alice@example.com", "User")
	f.dkim["sig1"] = "dom1"
	wireJMAP(t, f.server(t))

	if _, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"example.com"}`)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, ok := f.domains["example.com"]; !ok {
		t.Fatalf("a mail-only purge removed the Stalwart domain")
	}
	if _, ok := f.dkim["sig1"]; !ok {
		t.Fatalf("a mail-only purge removed the DKIM signature")
	}
}

// If the domain still cannot be destroyed (something outside the purge links
// it), the purge reports it but does not fail: a domain delete must not be
// blocked forever by a leftover Stalwart object.
func TestMailDomainPurge_DomainStillLinkedIsReportedNotFatal(t *testing.T) {
	f := newMGFake()
	f.domains["other.com"] = "dom2"
	group := f.addAccount("team@example.com", "Group")
	outsider := f.addAccount("bob@other.com", "User")
	f.accounts[outsider].memberGroupIDs[group] = true // cross-domain member keeps the group linked
	wireJMAP(t, f.server(t))

	out, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"example.com","remove_domain":true}`))
	if err != nil {
		t.Fatalf("purge must not fail when the domain stays linked: %v", err)
	}
	res := out.(mailDomainPurgeResult)
	if res.DomainRemoved || res.DomainError == "" {
		t.Fatalf("result = %+v, want domain_removed=false with a reason", res)
	}
	if _, ok := f.domains["example.com"]; !ok {
		t.Fatalf("fake removed a linked domain")
	}
}
