package userops

import "encoding/json"

// ParseReprovisionedUID extracts the freshly-allocated uid from a user.create
// agent response and returns it as a *uint32 to persist into users.linux_uid.
//
// Reprovisioning recreates the OS account with useradd, which allocates a NEW
// uid (the verb accepts no uid to pin), so the value the DB recorded at first
// provisioning goes stale. Everything that maps the panel row through
// linux_uid — POSIX quota, the nft skuid egress-dispatch rules, SFTP/FTP jails —
// then targets the wrong uid. Both the REST handler and the operator CLI call
// this so they persist the reprovisioned uid identically (JAB-287).
//
// Best-effort by design: a malformed response or a non-positive uid returns nil,
// and the caller keeps the prior value rather than failing an OS reprovision
// that already succeeded.
func ParseReprovisionedUID(raw []byte) *uint32 {
	var pr struct {
		UID int `json:"uid"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil || pr.UID <= 0 {
		return nil
	}
	uid := uint32(pr.UID)
	return &uid
}
