package limitsync

import "sync"

// tenantLocks holds one mutex per tenant (user id). Tenants are few enough
// that the map is never pruned.
var tenantLocks sync.Map

func tenantLock(userID string) *sync.Mutex {
	m, _ := tenantLocks.LoadOrStore(userID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// LockTenant serializes changes to one tenant's resource limits within this
// process: an isolated FTP/SFTP account's create or delete, which changes the
// tenant's disk share and limit, against the limits reconciler's apply for
// that tenant. It returns the unlock function.
//
// It does not reach other processes (the CLI); the reconciler re-reads the
// tenant's shares under the lock and skips the tenant when they changed, which
// covers those too once their change is committed.
func LockTenant(userID string) (unlock func()) {
	m := tenantLock(userID)
	m.Lock()
	return m.Unlock
}

// TryLockTenant is LockTenant without waiting: ok is false while the tenant
// is locked.
func TryLockTenant(userID string) (unlock func(), ok bool) {
	m := tenantLock(userID)
	if !m.TryLock() {
		return nil, false
	}
	return m.Unlock, true
}
