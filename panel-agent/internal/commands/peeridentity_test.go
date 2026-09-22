package commands

import (
	"context"
	"errors"
	"os/user"
	"strconv"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// JAB-357 AC4: admin_root is a capability bound to the connecting peer, not a
// request flag. fileScopeFor must REFUSE the root scope when the request ctx
// carries no admin-capable peer identity — otherwise a co-located service that
// reached the socket after a connect-gate/group regression could escalate to
// the whole-filesystem read scope by setting admin_root=true.
func TestFileScopeFor_AdminRootRequiresAuthorizedPeer(t *testing.T) {
	// No stamped identity → admin_root refused with permission_denied. Removing
	// the reject branch in fileScopeFor turns this RED (it would return an admin
	// scope + nil error), which is exactly the escalation the AC closes.
	if _, err := fileScopeFor(context.Background(), "0", "root", true); err == nil {
		t.Fatal("admin_root with no peer identity must be refused, got nil error")
	} else {
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodePermissionDenied {
			t.Fatalf("want permission_denied AgentError, got %v", err)
		}
	}

	// An admin-capable peer gets the root scope.
	ctx := WithPeerIdentity(context.Background(), 0, true, true)
	scope, err := fileScopeFor(ctx, "0", "root", true)
	if err != nil {
		t.Fatalf("admin_root with an authorized peer must succeed, got %v", err)
	}
	if scope == nil {
		t.Fatal("expected a non-nil admin scope")
	}

	// A peer that is resolved but NOT admin-capable is refused too — connecting
	// is not the same as being allowed to assert admin_root.
	ctxNoCap := WithPeerIdentity(context.Background(), 1000, true, false)
	if _, err := fileScopeFor(ctxNoCap, "0", "root", true); err == nil {
		t.Fatal("admin_root from a non-admin-capable peer must be refused")
	}

	// Ordinary tenant scope (adminRoot=false) never consults the peer identity:
	// the gate must not fire and confine-to-home must still work with a bare ctx.
	if _, err := fileScopeFor(context.Background(), "1000", "alice", false); err != nil {
		t.Fatalf("tenant scope must not require a peer identity, got %v", err)
	}
}

// JAB-357 AC4 (defensive): root:root ownership is a privileged outcome, granted
// only to an admin-capable peer. fileScopeFor rejects a spoof first, but if that
// branch were ever bypassed, fileOwnerIDs must still fall back to the tenant
// mapping rather than chown a new file to root for an unauthorized caller.
func TestFileOwnerIDs_AdminRootRequiresAuthorizedPeer(t *testing.T) {
	cur, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	curUID, err := strconv.Atoi(cur.Uid)
	if err != nil {
		t.Skipf("non-numeric uid %q", cur.Uid)
	}
	if curUID == 0 {
		t.Skip("uid distinction needs a non-root test user")
	}

	// Admin-capable peer → root:root (0,0).
	adminCtx := WithPeerIdentity(context.Background(), 0, true, true)
	if uid, _ := fileOwnerIDs(adminCtx, cur.Username, true); uid != 0 {
		t.Fatalf("admin-capable peer must own new files as root, got uid=%d", uid)
	}

	// No/absent capability + admin_root=true → tenant fallback (current uid),
	// never root. Flipping this to (0,0) would silently hand root ownership to
	// an unauthorized caller.
	if uid, _ := fileOwnerIDs(context.Background(), cur.Username, true); uid != curUID {
		t.Fatalf("unauthorized peer must fall back to tenant uid %d, got %d", curUID, uid)
	}
}

// withPeerIdentityFrom carries the connecting peer's capability onto a detached
// goroutine's context (the async extract/copy jobs). It must copy the exact
// capability and grant nothing when the source carries none — otherwise an async
// admin job would either break (dropped capability) or fail open (invented one).
func TestWithPeerIdentityFrom(t *testing.T) {
	// Capable source → capability carried forward.
	src := WithPeerIdentity(context.Background(), 1000, true, true)
	if !PeerAdminCapable(withPeerIdentityFrom(context.Background(), src)) {
		t.Fatal("admin capability must be carried onto the detached context")
	}

	// Non-capable source → dst stays non-capable.
	srcNoCap := WithPeerIdentity(context.Background(), 1000, true, false)
	if PeerAdminCapable(withPeerIdentityFrom(context.Background(), srcNoCap)) {
		t.Fatal("a non-admin source must not become admin-capable on copy")
	}

	// Source with no identity at all → dst carries none (fail closed).
	if PeerAdminCapable(withPeerIdentityFrom(context.Background(), context.Background())) {
		t.Fatal("copying from an identity-less context must not grant capability")
	}
}

// PeerAdminCapable fails closed: any context that was not explicitly stamped
// admin-capable returns false.
func TestPeerAdminCapable_FailsClosed(t *testing.T) {
	if PeerAdminCapable(context.Background()) {
		t.Fatal("a bare context must not be admin-capable")
	}
	if PeerAdminCapable(context.WithValue(context.Background(), struct{ k string }{"x"}, 1)) {
		t.Fatal("an unrelated context value must not be read as admin capability")
	}
}
