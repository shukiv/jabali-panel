package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// userPasswordParams is the input shape for user.password. Pass
// either plaintext via Password OR a pre-hashed crypt(3) line via
// PasswordHash (cpanel shadow's $6$SHA-512 entry). PasswordHash
// runs chpasswd -e so the source's Linux SSH/SFTP password keeps
// working on the destination without the operator handing the
// plaintext to the customer again.
type userPasswordParams struct {
	Username     string `json:"username"`
	Password     string `json:"password,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
}

// userPasswordResponse is the output shape for user.password.
type userPasswordResponse struct {
	Username string `json:"username"`
}

func userPasswordHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userPasswordParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate username format.
	if !usernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid username %q: must match ^[a-z_][a-z0-9_-]{0,31}$", p.Username),
		}
	}

	// Reject when neither password nor password_hash is supplied.
	if p.Password == "" && p.PasswordHash == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "password or password_hash required",
		}
	}

	// Check if user is protected.
	if protectedUsers[p.Username] {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("cannot change password for protected user %q", p.Username),
		}
	}

	// Check if user exists.
	checkCmd := execCommandContext(ctx, "id", p.Username)
	if err := checkCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("user %q does not exist", p.Username),
		}
	}

	// Choose chpasswd mode. PasswordHash takes precedence — operator
	// migration code can pass both (plaintext for kratos, hash for
	// /etc/shadow); this handler only updates /etc/shadow so hash
	// wins when present.
	var (
		args  []string
		input string
	)
	if p.PasswordHash != "" {
		args = []string{"-e"}
		input = p.Username + ":" + p.PasswordHash + "\n"
	} else {
		args = nil
		input = p.Username + ":" + p.Password + "\n"
	}
	chpasswdCmd := execCommandContext(ctx, "chpasswd", args...)
	chpasswdCmd.Stdin = strings.NewReader(input)
	if err := chpasswdCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("chpasswd failed: %v", err),
		}
	}

	return userPasswordResponse{
		Username: p.Username,
	}, nil
}

func init() {
	Default.Register("user.password", userPasswordHandler)
}
