package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// filesReadParams is the input shape for files.read.
type filesReadParams struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Path     string `json:"path"`
	Limit    int64  `json:"limit,omitempty"` // 0 = no limit, default 1MB
}

// filesReadResponse is the output shape for files.read.
// For text files, Content holds UTF-8 text. For binary (image/octet-stream),
// ContentB64 holds base64 of the raw bytes — a plain string cast would mangle
// binary through JSON's UTF-8 validation.
type filesReadResponse struct {
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	ContentB64 string `json:"content_b64,omitempty"`
	IsBinary   bool   `json:"is_binary,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	MimeType   string `json:"mime_type,omitempty"`
}

func filesReadHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesReadParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate inputs
	if p.Username == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "username required",
		}
	}
	if p.Path == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "path required",
		}
	}

	// Default limit: 1MB
	if p.Limit == 0 {
		p.Limit = 1 << 20 // 1MB
	}

	// Hard cap at 100MB regardless of Limit param
	const hardCap int64 = 100 * 1024 * 1024
	if p.Limit > hardCap {
		p.Limit = hardCap
	}

	// Create filesafe scope with user's home directory
	homeDir := fmt.Sprintf("/home/%s", p.Username)
	scope, err := filesafe.NewScope(p.UserID, p.Username, []string{homeDir})
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to create scope: %v", err),
		}
	}

	// String-gate the path; the open is escape-proof (openat2 RESOLVE_BENEATH),
	// so a parent-symlink swap can't redirect a root-side read (Gitea #428).
	cleanPath, err := scope.Clean(p.Path)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("path validation failed: %v", err),
		}
	}

	// Open file safely (O_RDONLY), escape-proof.
	file, err := scope.OpenInScope(cleanPath, os.O_RDONLY, 0)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to open file: %v", err),
		}
	}
	defer file.Close()

	// A directory opens fine O_RDONLY but io.ReadAll on it fails with a raw
	// "is a directory" — which surfaced as an opaque HTTP 500 when a user hit
	// "download" on a folder (GH #756). Detect it here and return a typed
	// precondition error so the panel can fall back to archiving the folder.
	if fi, statErr := file.Stat(); statErr == nil && fi.IsDir() {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: "path is a directory",
		}
	}

	// Read with limit
	limitedReader := io.LimitReader(file, p.Limit+1)
	content, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to read file: %v", err),
		}
	}

	truncated := int64(len(content)) > p.Limit
	if truncated {
		content = content[:p.Limit]
	}

	// MIME-sniff first 512 bytes
	mimeType := http.DetectContentType(content)
	isBinary := !strings.HasPrefix(mimeType, "text/") &&
		!strings.Contains(mimeType, "json") &&
		!strings.Contains(mimeType, "xml") &&
		!strings.Contains(mimeType, "javascript")

	resp := &filesReadResponse{
		Path:      cleanPath,
		Truncated: truncated,
		MimeType:  mimeType,
		IsBinary:  isBinary,
	}
	if isBinary {
		resp.ContentB64 = base64.StdEncoding.EncodeToString(content)
	} else {
		resp.Content = string(content)
	}
	return resp, nil
}

func init() {
	Default.Register("files.read", filesReadHandler)
}
