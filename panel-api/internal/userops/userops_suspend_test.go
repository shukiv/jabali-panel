package userops

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeSuspendUsers records SetSuspended calls; other UserRepository methods are
// unused (embedded interface panics if called — a guard against scope creep).
type fakeSuspendUsers struct {
	repository.UserRepository
	lastSetID       string
	lastSetVal      bool
	lastSetReason   string
	setSuspendCalls int
}

func (f *fakeSuspendUsers) SetSuspended(_ context.Context, id string, v bool, reason string) error {
	f.setSuspendCalls++
	f.lastSetID, f.lastSetVal, f.lastSetReason = id, v, reason
	return nil
}
func (f *fakeSuspendUsers) SetSSHForwardingEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}

type fakeSuspendDomains struct {
	repository.DomainRepository
	affected int64
	lastEn   bool
}

func (f *fakeSuspendDomains) BulkSetEnabledByUserID(_ context.Context, _ string, enabled bool) (int64, error) {
	f.lastEn = enabled
	return f.affected, nil
}

func strptr(s string) *string { return &s }

func TestSuspend_FullCascade(t *testing.T) {
	users := &fakeSuspendUsers{}
	doms := &fakeSuspendDomains{affected: 3}
	ag := &recordingAgent{}
	d := Deps{Users: users, Domains: doms, Agent: ag}
	u := &models.User{ID: "u1", Username: strptr("alice")}

	res, err := Suspend(context.Background(), d, u, "spam")
	if err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if res.AlreadySuspended {
		t.Fatal("res.AlreadySuspended = true, want false")
	}
	if users.setSuspendCalls != 1 || !users.lastSetVal || users.lastSetReason != "spam" {
		t.Errorf("SetSuspended not called with (true,'spam'): calls=%d val=%v reason=%q", users.setSuspendCalls, users.lastSetVal, users.lastSetReason)
	}
	if res.DomainsDisabled != 3 || doms.lastEn {
		t.Errorf("domains: disabled=%d lastEnabled=%v, want 3 + false", res.DomainsDisabled, doms.lastEn)
	}
	// OS cascade fired for the linux user.
	// JAB-254: suspension now also locks the tenant's FTP/SFTP aliases.
	if len(ag.calls) != 2 || ag.calls[0].method != "user.suspend" || ag.calls[1].method != "ftpaccount.lock_tenant" {
		t.Errorf("agent calls = %+v, want user.suspend then ftpaccount.lock_tenant", ag.calls)
	}
}

func TestSuspend_AlreadySuspended_NoWrites(t *testing.T) {
	users := &fakeSuspendUsers{}
	res, err := Suspend(context.Background(), Deps{Users: users}, &models.User{ID: "u1", Suspended: true}, "x")
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadySuspended {
		t.Error("res.AlreadySuspended = false, want true")
	}
	if users.setSuspendCalls != 0 {
		t.Errorf("SetSuspended called %d times on an already-suspended user, want 0", users.setSuspendCalls)
	}
}

func TestSuspend_NilKratosWarns(t *testing.T) {
	// User has a Kratos identity but no client is wired → a warning, not a fail.
	res, err := Suspend(context.Background(), Deps{Users: &fakeSuspendUsers{}}, &models.User{ID: "u1", KratosIdentityID: strptr("kid")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.KratosWarning != "kratos_client_unavailable" {
		t.Errorf("KratosWarning = %q, want kratos_client_unavailable", res.KratosWarning)
	}
}

func TestUnsuspend_ReversesCascade(t *testing.T) {
	users := &fakeSuspendUsers{}
	doms := &fakeSuspendDomains{affected: 2}
	ag := &recordingAgent{}
	d := Deps{Users: users, Domains: doms, Agent: ag}
	u := &models.User{ID: "u1", Username: strptr("alice"), Suspended: true}

	res, err := Unsuspend(context.Background(), d, u)
	if err != nil {
		t.Fatal(err)
	}
	if res.AlreadyActive {
		t.Fatal("res.AlreadyActive = true, want false")
	}
	if users.setSuspendCalls != 1 || users.lastSetVal {
		t.Errorf("SetSuspended not called with false: calls=%d val=%v", users.setSuspendCalls, users.lastSetVal)
	}
	if res.DomainsEnabled != 2 || !doms.lastEn {
		t.Errorf("domains: enabled=%d lastEnabled=%v, want 2 + true", res.DomainsEnabled, doms.lastEn)
	}
	if len(ag.calls) != 1 || ag.calls[0].method != "user.unsuspend" {
		t.Errorf("agent calls = %+v, want one user.unsuspend", ag.calls)
	}
}
