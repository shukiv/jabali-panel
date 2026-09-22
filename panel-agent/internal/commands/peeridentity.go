package commands

import "context"

// JAB-357 AC4: admin_root is a capability bound to the connecting peer, not a
// request flag any socket caller may set. The server resolves the peer's UID
// once per connection via SO_PEERCRED and stamps whether that peer is authorized
// to assert admin_root (its UID is on the agent's -admin-uids allow-list, which
// defaults to the connect allow-list = the panel user + root). Command handlers
// read the capability through peerAdminCapable; fileScopeFor refuses admin_root
// when it is absent, so a co-located service that reached the socket after a
// group/gate regression cannot escalate by sending admin_root=true.

type peerIdentityKey struct{}

type peerIdentity struct {
	uid          uint32
	haveUID      bool // false when SO_PEERCRED resolution failed
	adminCapable bool
}

// WithPeerIdentity stamps the connecting peer's UID (haveUID=false when it could
// not be resolved) and whether that peer may assert admin_root.
func WithPeerIdentity(ctx context.Context, uid uint32, haveUID, adminCapable bool) context.Context {
	return context.WithValue(ctx, peerIdentityKey{}, peerIdentity{uid: uid, haveUID: haveUID, adminCapable: adminCapable})
}

// PeerAdminCapable reports whether the connecting peer is authorized to assert
// admin_root. It FAILS CLOSED: a context with no stamped identity — including
// one whose SO_PEERCRED lookup failed — returns false, never true.
func PeerAdminCapable(ctx context.Context) bool {
	id, ok := ctx.Value(peerIdentityKey{}).(peerIdentity)
	return ok && id.adminCapable
}

// withPeerIdentityFrom copies the connecting peer's identity from src onto dst.
// The async file jobs (files.extract.start / files.copy.start) detach their
// work onto a fresh context.Background() so a long extraction is not cancelled
// when the request returns; that detached context would otherwise carry no peer
// identity and peerAdminCapable would fail closed, breaking a legitimately
// authorized admin async job. Copying the identity forward keeps the SAME
// authorization the request was admitted with — it grants nothing new: if src
// carries no identity, dst is returned unchanged and the admin path still fails
// closed.
func withPeerIdentityFrom(dst, src context.Context) context.Context {
	if id, ok := src.Value(peerIdentityKey{}).(peerIdentity); ok {
		return context.WithValue(dst, peerIdentityKey{}, id)
	}
	return dst
}
