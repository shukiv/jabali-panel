package commands

import (
	"context"
	"strings"
)

// renameGroupResources renames a resource group's auto-created shared
// collections (calendar + address book) to the group's display name. Stalwart
// auto-creates them named "Personal (<address>)" for any principal (GH #350) —
// on a shared group that reads as every member's own "Personal", which is
// wrong. This projects the group's name onto them instead.
//
// Best-effort: the group principal already exists when this runs, so a naming
// hiccup must never fail the apply (same reflex as setAccountDescription). For
// distribution groups (no calendar/address book) the default-id lookups return
// "" and this no-ops. When hasFiles is set the group's shared file folder is
// named too (GH #350) — Stalwart doesn't auto-create a file node, so this
// creates the root node named after the group (or renames an existing default).
func renameGroupResources(ctx context.Context, groupEmail, displayName string, hasFiles bool) {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return
	}
	acctID, err := accountIDByEmail(ctx, groupEmail)
	if err != nil || acctID == "" {
		return
	}
	if calID, err := defaultCalendarID(ctx, acctID); err == nil && calID != "" {
		_ = jmapSetCollectionName(ctx, jmapCapCalendars, "Calendar/set", acctID, calID, name)
	}
	if abID, err := defaultAddressBookID(ctx, acctID); err == nil && abID != "" {
		_ = jmapSetCollectionName(ctx, jmapCapContacts, "AddressBook/set", acctID, abID, name)
	}
	if hasFiles {
		renameGroupFileNode(ctx, acctID, name)
	}
}

// renameGroupFileNode names the group's shared file folder after the group.
// Unlike calendar/address book, Stalwart does not auto-create a file node for a
// principal, so the group's root folder is otherwise created on demand with the
// default "Shared" name (GH #350). This renames the existing root node when one
// is present, or creates one named after the group so shared files read as the
// group from the moment files are enabled. Best-effort.
func renameGroupFileNode(ctx context.Context, accountID, name string) {
	var listRes struct {
		List []struct {
			ID       string  `json:"id"`
			Name     string  `json:"name"`
			ParentID *string `json:"parentId"`
		} `json:"list"`
	}
	if err := jmapCallWith(ctx, jmapCapFileNode, "FileNode/get",
		map[string]any{"accountId": accountID, "properties": []string{"parentId", "name"}}, &listRes); err != nil {
		return
	}
	for _, n := range listRes.List {
		if n.ParentID == nil || *n.ParentID == "" {
			if n.Name != name {
				_ = jmapSetCollectionName(ctx, jmapCapFileNode, "FileNode/set", accountID, n.ID, name)
			}
			return
		}
	}
	// No root node yet — create one named after the group.
	_, _ = ensureRootFileNode(ctx, accountID, name)
}

// jmapSetCollectionName updates the `name` of one collection via a JMAP */set.
func jmapSetCollectionName(ctx context.Context, capability, method, accountID, id, name string) error {
	args := map[string]any{
		"accountId": accountID,
		"update": map[string]any{
			id: map[string]any{"name": name},
		},
	}
	var result jmapSetResult
	return jmapCallWith(ctx, capability, method, args, &result)
}
