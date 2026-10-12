package api

import (
	"crypto/rand"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// resetPassword handles POST /admin/users/:id/password/reset (M54).
//
// Under username-only login the email is a plain trait, so Kratos self-service
// recovery does not exist (spike-proven). The only working reset is an admin
// directly setting a fresh strong password and handing it to the user. This
// generates a temp password, rotates it through userops.RotatePassword (Kratos,
// then the Linux account, then the stored hash, the same pipeline as Edit User),
// and returns the plaintext EXACTLY ONCE (reveal-once, like mailbox / db-user /
// automation-token secrets). Admin-only (route registration).
//
// GH #2058: this used to set the Kratos password only. SSH and SFTP kept the
// old password, and the stored hash stayed old, so the user's own password
// change (which checks current_password against it) refused the new one.
func (h *userHandler) resetPassword(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}
	user, err := h.cfg.Repo.FindByID(c.Request.Context(), userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if user.KratosIdentityID == nil || *user.KratosIdentityID == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "user_has_no_kratos_identity",
			"detail": "User predates Kratos migration. Run `jabali admin rebuild-kratos`, then re-attempt.",
		})
		return
	}
	if h.cfg.KratosClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kratos not available"})
		return
	}

	temp, err := generateTempPassword(18)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "password generation failed"})
		return
	}
	rotateErr := userops.RotatePassword(c.Request.Context(), h.userOpsDeps(), user, temp)
	switch {
	case rotateErr == nil, errors.Is(rotateErr, userops.ErrAgentPassword):
		// Kratos holds the new password in both cases.
	case errors.Is(rotateErr, kratosclient.ErrIdentityNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "kratos identity not found"})
		return
	case errors.Is(rotateErr, userops.ErrKratosSetPassword):
		c.JSON(http.StatusBadGateway, gin.H{"error": "kratos_error", "detail": "failed to set password"})
		return
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// JAB-3: a password reset changes the identity's auth state; drop any cached
	// positive whoami for it so pre-reset sessions aren't accepted for the TTL.
	h.cfg.KratosClient.InvalidateIdentity(*user.KratosIdentityID)

	// reveal-once: plaintext returned now, never persisted, never logged.
	if rotateErr != nil {
		// The panel login already uses the new password, so withholding it
		// would lock the user out. The Linux account (SSH, SFTP) still has the
		// old one; a new reset retries it. The agent's error is logged by
		// RotatePassword and never echoed.
		c.JSON(http.StatusOK, gin.H{"password": temp, "os_synced": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"password": temp})
}

// generateTempPassword returns an n-char password from an unambiguous
// alphanumeric alphabet (no 0/O/1/l/I) via crypto/rand.
func generateTempPassword(n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf), nil
}
