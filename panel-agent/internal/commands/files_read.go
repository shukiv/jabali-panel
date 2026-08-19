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
	"unicode/utf8"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// filesReadParams is the input shape for files.read.
type filesReadParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root"` // GH #1184 admin FM: root scope + deny-list
	Path      string `json:"path"`
	Limit     int64  `json:"limit,omitempty"` // 0 = no limit, default 1MB
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

// validUTF8Body reports whether content is valid UTF-8. A truncated read can
// slice the final rune mid-sequence, which is a cut, not corruption -- so when
// the read was truncated an incomplete trailing rune is ignored.
func validUTF8Body(content []byte, truncated bool) bool {
	if utf8.Valid(content) {
		return true
	}
	if !truncated {
		return false
	}
	// Tolerate ONLY a final rune the limit sliced in half -- nothing else. Walk
	// back to the last rune-start byte: everything before it must be valid, and
	// the bytes from it must be a genuine incomplete sequence (DecodeRune gives
	// RuneError with size 1). Blindly trimming trailing bytes instead would
	// accept truly corrupt content whose bad byte merely sits near the end.
	for i := len(content) - 1; i >= 0 && i > len(content)-utf8.UTFMax; i-- {
		if !utf8.RuneStart(content[i]) {
			continue
		}
		r, size := utf8.DecodeRune(content[i:])
		return r == utf8.RuneError && size == 1 && utf8.Valid(content[:i])
	}
	return false
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
	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
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

	// JAB-191: anything that is not valid UTF-8 MUST travel as base64. The
	// non-binary branch below hands the bytes back in a JSON string, and
	// encoding/json substitutes U+FFFD for every invalid byte -- so the caller
	// silently receives a corrupted file with no error. MIME sniffing alone does
	// not catch this: a latin1 / windows-1252 PHP or HTML file (very common on
	// legacy sites) sniffs as text/plain and takes the lossy path.
	if !isBinary && !validUTF8Body(content, truncated) {
		isBinary = true
	}

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
