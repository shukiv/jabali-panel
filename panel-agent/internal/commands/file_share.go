package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// file_share.go — M52 (ADR-0133). Converge a shared file folder's JMAP
// FileNode.shareWith, mirroring calendar_share.go / addressbook_share.go.
//
// Files differ from calendars/address books: a host principal has NO node
// until one is created (verified live — FileNode/get returns []). So this
// command find-or-creates a single top-level folder node on the host, then
// shares THAT node. FileNode uses its own shareWith rights vocabulary
// (verified live): mayRead, mayAddChildren, mayModifyContent, mayRename,
// mayDelete, mayShare.

// jmapCapFileNode is the JMAP capability FileNode/* methods require.
const jmapCapFileNode = "urn:ietf:params:jmap:filenode"

type fileShareSetParams struct {
	OwnerEmail string            `json:"owner_email"`
	FolderName string            `json:"folder_name"` // name for the root folder if it must be created
	Shares     map[string]string `json:"shares"`      // targetEmail → "read"|"readwrite"|"admin"
}

type fileShareSetResponse struct {
	Ok     bool   `json:"ok"`
	NodeID string `json:"node_id"` // the shared folder node id (panel caches it as collection_id)
}

// fileACLForLevel maps a coarse grant level to FileNode shareWith keys.
// Unknown levels degrade to read-only.
func fileACLForLevel(level string) map[string]bool {
	acl := map[string]bool{"mayRead": true}
	switch level {
	case "readwrite":
		acl["mayAddChildren"] = true
		acl["mayModifyContent"] = true
		acl["mayRename"] = true
	case "admin":
		acl["mayAddChildren"] = true
		acl["mayModifyContent"] = true
		acl["mayRename"] = true
		acl["mayDelete"] = true
		acl["mayShare"] = true
	}
	return acl
}

func fileShareSetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p fileShareSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if _, err := requireEmail(p.OwnerEmail); err != nil {
		return nil, err
	}

	acctID, err := accountIDByEmail(ctx, p.OwnerEmail)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve account: %v", err)}
	}
	if acctID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "shared folder host not yet registered"}
	}

	targetIDs := make(map[string]map[string]bool, len(p.Shares))
	for targetEmail, level := range p.Shares {
		tid, err := accountIDByEmail(ctx, targetEmail)
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve target %s: %v", targetEmail, err)}
		}
		if tid == "" {
			continue
		}
		targetIDs[tid] = fileACLForLevel(level)
	}

	nodeID, err := ensureRootFileNode(ctx, acctID, p.FolderName)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve folder: %v", err)}
	}

	args := map[string]any{
		"accountId": acctID,
		"update": map[string]any{
			nodeID: map[string]any{
				"shareWith": targetIDs,
			},
		},
	}
	var result jmapSetResult
	if err := jmapCallWith(ctx, jmapCapFileNode, "FileNode/set", args, &result); err != nil {
		return nil, err
	}
	if reason, ok := result.NotUpdated[nodeID]; ok {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("FileNode/set refused: %s", string(reason))}
	}
	return fileShareSetResponse{Ok: true, NodeID: nodeID}, nil
}

// ensureRootFileNode returns the host's top-level shared folder node id,
// creating one named folderName (default "Shared") if the host has none.
// In the one-principal-per-resource model the host owns a single folder.
func ensureRootFileNode(ctx context.Context, accountID, folderName string) (string, error) {
	var listRes struct {
		List []struct {
			ID       string  `json:"id"`
			ParentID *string `json:"parentId"`
		} `json:"list"`
	}
	if err := jmapCallWith(ctx, jmapCapFileNode, "FileNode/get",
		map[string]any{"accountId": accountID, "properties": []string{"parentId"}}, &listRes); err != nil {
		return "", err
	}
	for _, n := range listRes.List {
		if n.ParentID == nil || *n.ParentID == "" {
			return n.ID, nil
		}
	}

	if folderName == "" {
		folderName = "Shared"
	}
	// Stalwart 1.0 refuses a node create without "@type" ("@type is required
	// on creation"), so a host never got its folder and nothing was shared.
	var setRes jmapSetResult
	if err := jmapCallWith(ctx, jmapCapFileNode, "FileNode/set",
		map[string]any{"accountId": accountID, "create": map[string]any{"n": map[string]any{
			"@type": "Directory",
			"name":  folderName,
		}}}, &setRes); err != nil {
		return "", err
	}
	raw, ok := setRes.Created["n"]
	if !ok {
		if reason, bad := setRes.NotCreated["n"]; bad {
			return "", fmt.Errorf("FileNode create refused: %s", string(reason))
		}
		return "", fmt.Errorf("FileNode create: no created entry")
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("parse created node: %w", err)
	}
	return created.ID, nil
}

func init() {
	Default.Register("file.share_set", fileShareSetHandler)
}
