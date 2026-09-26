package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ssh.user.write_nspawn_pin — materialize the per-user nspawn image pin
// at /etc/jabali/users/<username>/nspawn-image.
//
// Reconciler is the source of truth for both the DB column
// (hosting_packages.nspawn_image_version) and this filesystem mirror. The wrapper
// reads the file on each connect and resolves to nologin if missing or
// unreadable. Empty/blank image clears the pin (file removed).

var nspawnImageRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// nspawnPinDir is a var only so tests can point it at a TempDir.
var nspawnPinDir = "/etc/jabali/users"

type sshUserWriteNspawnPinParams struct {
	Username string `json:"username"`
	Image    string `json:"image"` // empty string → remove pin file
}

type sshUserWriteNspawnPinResponse struct {
	Username string `json:"username"`
	Image    string `json:"image"`
	Path     string `json:"path"`
	Removed  bool   `json:"removed,omitempty"`
	Wrote    bool   `json:"wrote,omitempty"`
}

func sshUserWriteNspawnPinHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sshUserWriteNspawnPinParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	// The account-name rule (user.create), not the image rule: panel
	// usernames may contain `_`, and refusing them left those users without
	// a pin. It admits no `/` or `.`, so the name is safe as a path element.
	if !usernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid username",
		}
	}
	if p.Image != "" && !nspawnImageRe.MatchString(p.Image) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "image must match [a-z0-9-]+",
		}
	}

	dir := filepath.Join(nspawnPinDir, p.Username)
	pinFile := filepath.Join(dir, "nspawn-image")

	// Empty image → remove pin (user falls back to default-nspawn-image).
	if p.Image == "" {
		if err := os.Remove(pinFile); err != nil && !os.IsNotExist(err) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("remove %s: %v", pinFile, err),
			}
		}
		return &sshUserWriteNspawnPinResponse{
			Username: p.Username,
			Image:    "",
			Path:     pinFile,
			Removed:  true,
		}, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir %s: %v", dir, err),
		}
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("chmod %s: %v", dir, err),
		}
	}

	tmp := pinFile + ".new"
	if err := os.WriteFile(tmp, []byte(p.Image+"\n"), 0o644); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write %s: %v", tmp, err),
		}
	}
	if err := os.Rename(tmp, pinFile); err != nil {
		_ = os.Remove(tmp)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("rename %s -> %s: %v", tmp, pinFile, err),
		}
	}
	return &sshUserWriteNspawnPinResponse{
		Username: p.Username,
		Image:    p.Image,
		Path:     pinFile,
		Wrote:    true,
	}, nil
}

func init() {
	Default.Register("ssh.user.write_nspawn_pin", sshUserWriteNspawnPinHandler)
}
