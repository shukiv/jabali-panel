package main

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// cliAudit writes a unified audit event for a direct root-CLI mutation
// (Gitea #537). Direct CLI commands bypass the HTTP layer (which records via
// the audit middleware), so operator actions were invisible in the trail.
//
// actor_kind="cli", actor_user_id NULL (no logged-in identity on the root
// shell). The hash-chain columns are left NULL — the panel-api single-writer
// chain consumer backfills "fallback-pending" rows (audit_event.go), which is
// the designed path for out-of-process writers. Best-effort + synchronous: a
// short-lived CLI process can't rely on an async recorder goroutine flushing,
// and an audit failure must never fail the actual operation.
//
// target is the resource id/name (NEVER a secret). subjectUserID is the owner
// of the affected resource when known (nil for server-scoped actions).
func cliAudit(ctx context.Context, action, targetType, target, result string, subjectUserID *string) {
	if sharedDB == nil {
		return
	}
	repo := repository.NewAuditEventRepository(sharedDB)
	_ = repo.Create(ctx, &models.AuditEvent{
		ID:            ids.NewULID(),
		TS:            time.Now().UTC(),
		ActorKind:     "cli",
		ActorUserID:   nil,
		SubjectUserID: subjectUserID,
		Action:        action,
		TargetType:    targetType,
		TargetID:      target,
		Result:        result,
	})
}

// cliAuditRecorder is the audit.Recorder a service gets when a CLI command
// runs it (GH #1816: the ownership service records the changes it makes on
// its own, such as a cascade after an approval). It writes each event
// synchronously, for the reason cliAudit does, and keeps the event's actor
// kind: a change the service made on its own stays a "system" event.
type cliAuditRecorder struct {
	create func(context.Context, *models.AuditEvent) error
}

// newCLIAuditRecorder returns nil without a database.
func newCLIAuditRecorder() audit.Recorder {
	if sharedDB == nil {
		return nil
	}
	return cliAuditRecorder{create: repository.NewAuditEventRepository(sharedDB).Create}
}

func (r cliAuditRecorder) Record(e *models.AuditEvent) {
	if e == nil || r.create == nil {
		return
	}
	if e.ID == "" {
		e.ID = ids.NewULID()
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if e.Result == "" {
		e.Result = models.AuditResultOK
	}
	if e.ActorKind == "" {
		e.ActorKind = models.AuditActorSystem
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.create(ctx, e) // best-effort, like cliAudit
}

// cliAuditOK / cliAuditErr are the common-case shorthands.
func cliAuditOK(ctx context.Context, action, targetType, target string, subjectUserID *string) {
	cliAudit(ctx, action, targetType, target, models.AuditResultOK, subjectUserID)
}

func cliAuditErr(ctx context.Context, action, targetType, target string, subjectUserID *string) {
	cliAudit(ctx, action, targetType, target, models.AuditResultError, subjectUserID)
}
