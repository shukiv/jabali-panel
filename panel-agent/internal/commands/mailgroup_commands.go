package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mailgroup_commands.go — M51 (issue #201, ADR-0132). Project a panel
// mail group into the Stalwart registry.
//
// A group is an x:Account principal with @type:"Group". Creating it makes
// Stalwart auto-own a calendar + address book + its own inbox + a sending
// Identity for the group address (verified live, ADR-0132). Membership is
// a user-side edge: each member account's `memberGroupIds` map gains the
// group id; that single edge gives the member, in their own JMAP session,
// read access to the group inbox, send-as via the group Identity, and the
// shared calendar/address book/files. The edge is durable across re-auth,
// so no SQL `queryMemberOf` wiring is needed.
//
// DB-as-truth (ADR-0045): the panel owns the group rows + membership and
// computes diffs; the agent only issues the JMAP projection. The panel
// passes member emails explicitly (it has them in the DB) so the agent
// never has to query current membership.

// --- mailgroup.apply ---------------------------------------------------

type mailGroupApplyParams struct {
	Email        string `json:"email"`         // group address, name@domain
	DisplayName  string `json:"display_name"`  // drives the group's sending Identity name
	Description  string `json:"description"`   // admin note (also stored on the principal)
	InternalOnly bool   `json:"internal_only"` // GH #348: reject external senders
	HasFiles     bool   `json:"has_files"`     // GH #350: name the shared file folder too
}

func mailGroupApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p mailGroupApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	email, err := requireEmail(p.Email)
	if err != nil {
		return nil, err
	}

	gid, err := ensureGroupInRegistry(ctx, email, p.Description)
	if err != nil {
		return nil, err
	}
	// The principal description drives the auto sending Identity's name
	// (the From name members see). Prefer display_name, fall back to the
	// admin description. Best-effort: the group already exists at this
	// point, so a description hiccup shouldn't fail the apply.
	name := strings.TrimSpace(p.DisplayName)
	if name == "" {
		name = strings.TrimSpace(p.Description)
	}
	if name != "" {
		_ = setAccountDescription(ctx, email, name)
	}
	// GH #350: rename the group's auto-created "Personal (<addr>)" calendar +
	// address book to the group name so shared resources read as the group,
	// not each member's "Personal". Best-effort (guards on empty name).
	renameGroupResources(ctx, email, name, p.HasFiles)
	// GH #348: install/remove the internal-only delivery Sieve on the group.
	applyGroupInternalOnly(ctx, email, p.InternalOnly)
	return mailGroupApplyResult{Ok: true, GroupID: gid}, nil
}

type mailGroupApplyResult struct {
	Ok      bool   `json:"ok"`
	GroupID string `json:"group_id"`
}

// ensureGroupInRegistry creates the @type:Group principal if absent and
// returns its registry id. Idempotent: an existing group (alreadyExists /
// primaryKeyViolation) resolves to its current id.
func ensureGroupInRegistry(ctx context.Context, email, description string) (string, error) {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return "", fmt.Errorf("ensureGroupInRegistry: malformed email %q", email)
	}
	localPart, domainName := email[:at], email[at+1:]

	domainID, err := domainIDByName(ctx, domainName)
	if err != nil {
		return "", err
	}
	if domainID == "" {
		if domainID, err = createDomain(ctx, domainName); err != nil {
			return "", err
		}
	}

	create := map[string]any{
		"@type":    "Group",
		"name":     localPart,
		"domainId": domainID,
	}
	if d := strings.TrimSpace(description); d != "" {
		create["description"] = d
	}
	args := map[string]any{"create": map[string]any{"#g": create}}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:Account/set", args, &result); err != nil {
		return "", err
	}
	if c, ok := result.Created["#g"]; ok {
		var created struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(c, &created)
		return created.ID, nil
	}
	if reason, ok := result.NotCreated["#g"]; ok {
		var r struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(reason, &r)
		if r.Type == "alreadyExists" || r.Type == "primaryKeyViolation" {
			// Already projected — resolve its current id (accountIDByEmail
			// matches on {name, domainId}, which a Group principal has too).
			return accountIDByEmail(ctx, email)
		}
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart x:Account/set create group refused: %s", string(reason)),
		}
	}
	return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "x:Account/set group: no created/notCreated entry"}
}

// --- mailgroup.members_set ---------------------------------------------

type mailGroupMembersSetParams struct {
	GroupEmail   string   `json:"group_email"`
	MemberEmails []string `json:"member_emails"` // desired members → memberGroupIds += group
	RemoveEmails []string `json:"remove_emails"` // removed members → memberGroupIds -= group
}

func mailGroupMembersSetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p mailGroupMembersSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	groupEmail, err := requireEmail(p.GroupEmail)
	if err != nil {
		return nil, err
	}
	gid, err := accountIDByEmail(ctx, groupEmail)
	if err != nil {
		return nil, err
	}
	if gid == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "group not yet registered"}
	}

	// Build a single x:Account/set update with the memberGroupIds patch for
	// every member. Adds set the key true; removals set it null.
	update := map[string]any{}
	addMemberPatch(ctx, update, gid, p.MemberEmails, true)
	addMemberPatch(ctx, update, gid, p.RemoveEmails, false)
	if len(update) == 0 {
		return okBody{Ok: true}, nil
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:Account/set", map[string]any{"update": update}, &result); err != nil {
		return nil, err
	}
	if len(result.NotUpdated) > 0 {
		// A member that no longer exists in the registry is not fatal — the
		// DB is authoritative and the edge is moot. Surface as a soft note.
		return mailGroupMembersResult{Ok: true, Skipped: len(result.NotUpdated)}, nil
	}
	return mailGroupMembersResult{Ok: true}, nil
}

type mailGroupMembersResult struct {
	Ok      bool `json:"ok"`
	Skipped int  `json:"skipped,omitempty"`
}

// addMemberPatch resolves each member email to a registry id and adds a
// memberGroupIds/<gid> patch to the shared update map. Members absent from
// the registry (never authed, no JMAP record) are skipped silently — the
// edge will be (re)applied next time the member is touched.
func addMemberPatch(ctx context.Context, update map[string]any, gid string, emails []string, present bool) {
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		mid, err := accountIDByEmail(ctx, e)
		if err != nil || mid == "" {
			continue
		}
		patchKey := "memberGroupIds/" + gid
		if existing, ok := update[mid].(map[string]any); ok {
			existing[patchKey] = patchVal(present)
		} else {
			update[mid] = map[string]any{patchKey: patchVal(present)}
		}
	}
}

func patchVal(present bool) any {
	if present {
		return true
	}
	return nil
}

// --- mailgroup.delete --------------------------------------------------

type mailGroupDeleteParams struct {
	GroupEmail   string   `json:"group_email"`
	MemberEmails []string `json:"member_emails"` // current members to unlink first
}

func mailGroupDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p mailGroupDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	groupEmail, err := requireEmail(p.GroupEmail)
	if err != nil {
		return nil, err
	}
	gid, err := accountIDByEmail(ctx, groupEmail)
	if err != nil {
		return nil, err
	}
	if gid == "" {
		// Never projected — nothing to destroy.
		return okBody{Ok: true}, nil
	}

	// Strip memberships first: Stalwart refuses to destroy a group with
	// linked members (objectIsLinked, ADR-0132).
	if len(p.MemberEmails) > 0 {
		update := map[string]any{}
		addMemberPatch(ctx, update, gid, p.MemberEmails, false)
		if len(update) > 0 {
			var r jmapSetResult
			if err := jmapCall(ctx, "x:Account/set", map[string]any{"update": update}, &r); err != nil {
				return nil, err
			}
		}
	}

	if err := accountDestroy(ctx, gid); err != nil {
		return nil, err
	}
	return okBody{Ok: true}, nil
}

func init() {
	Default.Register("mailgroup.apply", mailGroupApplyHandler)
	Default.Register("mailgroup.members_set", mailGroupMembersSetHandler)
	Default.Register("mailgroup.delete", mailGroupDeleteHandler)
}
