package ftpops

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Password bounds for every subaccount credential (create and rotation).
const (
	PasswordMinLen = 12
	PasswordMaxLen = 128
)

// ErrWeakPassword is the typed reason for a password outside the bounds.
var ErrWeakPassword = errors.New("ftpops: weak password")

// ValidationError is a rejected input: a typed Reason the adapter maps to its
// own error code, plus a human-readable Detail it may surface verbatim.
type ValidationError struct {
	Reason error
	Detail string
}

func (e *ValidationError) Error() string { return e.Detail }
func (e *ValidationError) Unwrap() error { return e.Reason }

func invalid(reason error, detail string) error {
	return &ValidationError{Reason: reason, Detail: detail}
}

// ValidatePassword enforces the subaccount password bounds.
func ValidatePassword(password string) error {
	if len(password) < PasswordMinLen || len(password) > PasswordMaxLen {
		return invalid(ErrWeakPassword, fmt.Sprintf("password must be %d-%d characters", PasswordMinLen, PasswordMaxLen))
	}
	return nil
}

// SetPassword rotates a subaccount's password on the host.
//
// JAB-261: chpasswd drops the shadow lock, so the account's desired lock state
// is sent in the same verb and the agent re-locks a disabled account. Returns a
// *ValidationError (ErrWeakPassword) before any host call, else the raw agent
// error.
func SetPassword(ctx context.Context, d Deps, acct *models.FtpAccount, tenantUsername, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	return agentCall(ctx, d.Agent, "ftpaccount.set_password", map[string]any{
		"tenant_username": tenantUsername,
		"username":        acct.Username,
		"password":        password,
		"enabled":         acct.IsEnabled,
	})
}
