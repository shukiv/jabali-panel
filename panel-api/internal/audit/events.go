package audit

import (
	"encoding/json"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Typed constructors so call sites never stringly-type actor_kind /
// result / action and never forget subject scoping. The recorder
// fills ID/TS/defaults; these set the semantic fields. meta is
// structured context ONLY — callers must never pass request bodies or
// secrets (ADR-0105).

func metaJSON(kv map[string]any) json.RawMessage {
	if len(kv) == 0 {
		return nil
	}
	b, err := json.Marshal(kv)
	if err != nil {
		return nil
	}
	return b
}

func sp(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

// APIMutation is the generic event the Step-2 recorder middleware
// emits for a mutating REST call. action is the normalised
// "METHOD /route/template" (NOT the concrete URL — no high-cardinality
// ids). subjectUserID is the owner of the affected resource (may be
// "" for server-scoped actions → invisible to /me/activity).
func APIMutation(actorUserID, actorKind, subjectUserID, action, targetType, targetID, result, sourceIP, requestID string) *models.AuditEvent {
	return &models.AuditEvent{
		ActorUserID:   sp(actorUserID),
		ActorKind:     actorKind,
		SubjectUserID: sp(subjectUserID),
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		Result:        result,
		SourceIP:      sp(sourceIP),
		RequestID:     sp(requestID),
	}
}

// ImpersonationStart / ImpersonationStop — ADR-0015. subjectUserID is
// the impersonated user (so it shows in THEIR /me/activity, gated by
// server_settings.audit_show_impersonation at the read layer).
func ImpersonationStart(adminUserID, subjectUserID, sourceIP, requestID string) *models.AuditEvent {
	return &models.AuditEvent{
		ActorUserID: sp(adminUserID), ActorKind: models.AuditActorAdmin,
		SubjectUserID: sp(subjectUserID), Action: "impersonation.start",
		TargetType: "user", TargetID: subjectUserID, Result: models.AuditResultOK,
		SourceIP: sp(sourceIP), RequestID: sp(requestID),
	}
}

func ImpersonationStop(adminUserID, subjectUserID, sourceIP, requestID string) *models.AuditEvent {
	return &models.AuditEvent{
		ActorUserID: sp(adminUserID), ActorKind: models.AuditActorAdmin,
		SubjectUserID: sp(subjectUserID), Action: "impersonation.stop",
		TargetType: "user", TargetID: subjectUserID, Result: models.AuditResultOK,
		SourceIP: sp(sourceIP), RequestID: sp(requestID),
	}
}

// BreakGlassLogin — ADR-0016 CLI admin login. actorKind=cli.
func BreakGlassLogin(adminUserID, purpose, sourceIP string) *models.AuditEvent {
	return &models.AuditEvent{
		ActorUserID: sp(adminUserID), ActorKind: models.AuditActorCLI,
		SubjectUserID: sp(adminUserID), Action: "auth.break_glass_login",
		TargetType: "user", TargetID: adminUserID, Result: models.AuditResultOK,
		SourceIP: sp(sourceIP),
		Meta:     metaJSON(map[string]any{"purpose": purpose}),
	}
}

// TokenMint / TokenRevoke — M44 automation tokens.
func TokenMint(adminUserID, tokenID, name string, scopes []string, sourceIP, requestID string) *models.AuditEvent {
	return &models.AuditEvent{
		ActorUserID: sp(adminUserID), ActorKind: models.AuditActorAdmin,
		Action: "automation.token.mint", TargetType: "token", TargetID: tokenID,
		Result: models.AuditResultOK, SourceIP: sp(sourceIP), RequestID: sp(requestID),
		Meta: metaJSON(map[string]any{"name": name, "scopes": scopes}),
	}
}

func TokenRevoke(adminUserID, tokenID, sourceIP, requestID string) *models.AuditEvent {
	return &models.AuditEvent{
		ActorUserID: sp(adminUserID), ActorKind: models.AuditActorAdmin,
		Action: "automation.token.revoke", TargetType: "token", TargetID: tokenID,
		Result: models.AuditResultOK, SourceIP: sp(sourceIP), RequestID: sp(requestID),
	}
}

// SecurityToggle — CrowdSec/UFW/AppArmor/egress on/off. Server-scoped
// (subject NULL) → admin-only visibility by construction.
func SecurityToggle(adminUserID, subsystem, state, sourceIP, requestID string) *models.AuditEvent {
	return &models.AuditEvent{
		ActorUserID: sp(adminUserID), ActorKind: models.AuditActorAdmin,
		Action: "security.toggle", TargetType: "security", TargetID: subsystem,
		Result: models.AuditResultOK, SourceIP: sp(sourceIP), RequestID: sp(requestID),
		Meta: metaJSON(map[string]any{"subsystem": subsystem, "state": state}),
	}
}

// DomainOwnership — an administrator approved or revoked the ownership proof
// of a domain or a web alias, or switched the proof requirement (GH #1816 /
// ADR-0170). subjectUserID is the owner of the affected name ("" for the
// server-wide switch, which stays admin-only).
func DomainOwnership(adminUserID, subjectUserID, action, targetType, targetID, sourceIP, requestID string, meta map[string]any) *models.AuditEvent {
	return &models.AuditEvent{
		ActorUserID: sp(adminUserID), ActorKind: models.AuditActorAdmin,
		SubjectUserID: sp(subjectUserID), Action: action,
		TargetType: targetType, TargetID: targetID, Result: models.AuditResultOK,
		SourceIP: sp(sourceIP), RequestID: sp(requestID), Meta: metaJSON(meta),
	}
}

// DomainOwnershipSystem is an ownership change the panel made on its own
// (GH #1816): a DNS proof accepted, the parent rule applied, or a name nobody
// proved removed. It has no actor user; subjectUserID is the name's owner, so
// the change shows in their activity too.
func DomainOwnershipSystem(subjectUserID, action, targetType, targetID string, meta map[string]any) *models.AuditEvent {
	return &models.AuditEvent{
		ActorKind: models.AuditActorSystem, SubjectUserID: sp(subjectUserID), Action: action,
		TargetType: targetType, TargetID: targetID, Result: models.AuditResultOK, Meta: metaJSON(meta),
	}
}
