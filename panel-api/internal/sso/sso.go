// Package sso provides phpMyAdmin SSO service methods.
package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Service is the phpMyAdmin SSO service.
type Service struct {
	db       *gorm.DB
	users    repository.UserRepository
	tokens   repository.PhpMyAdminSSOTokenRepository
	agent    agent.AgentInterface
	ssoKey   *ssokey.Key
	log      *slog.Logger
	tokenTTL time.Duration
}

// NewService creates a new SSO service.
func NewService(
	db *gorm.DB,
	users repository.UserRepository,
	tokens repository.PhpMyAdminSSOTokenRepository,
	agent agent.AgentInterface,
	ssoKey *ssokey.Key,
	log *slog.Logger,
) *Service {
	return &Service{
		db:       db,
		users:    users,
		tokens:   tokens,
		agent:    agent,
		ssoKey:   ssoKey,
		log:      log,
		tokenTTL: 5 * time.Minute,
	}
}

// SSOInterface defines the methods needed by the reconciler for SSO operations.
type SSOInterface interface {
	// EnsureShadow ensures a user has a shadow MySQL account.
	// If the account already exists, returns nil. If it doesn't, the agent
	// is called to create it and the credentials are encrypted and persisted.
	EnsureShadow(ctx context.Context, userID string) error
}

// EnsureShadow ensures a user has a shadow MySQL account. If the account
// already exists, the plaintext password is returned unchanged. If it doesn't,
// the agent is called to create it and the password is encrypted and persisted.
// The whole operation happens in a FOR UPDATE transaction to avoid TOCTOU races.
func (s *Service) EnsureShadow(ctx context.Context, userID string) error {
	// Fetch user to get panel username (outside transaction, just for username)
	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}

	if user.Username == nil || *user.Username == "" {
		return errors.New("user has no username")
	}

	// Begin transaction with FOR UPDATE lock
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Load current shadow account state with FOR UPDATE lock
		var currentUser models.User
		if err := tx.
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&currentUser, "id = ?", userID).Error; err != nil {
			return fmt.Errorf("select for update: %w", err)
		}

		// If shadow account exists, just return nil
		if currentUser.MysqladminUsername != nil {
			if currentUser.MysqladminPasswordEnc == nil {
				return errors.New("shadow account exists but password is null")
			}
			return nil
		}

		// Shadow account doesn't exist. Call agent to create it.
		resp, err := s.agent.Call(ctx, "db.mysqladmin.ensure", map[string]interface{}{
			"panel_username": user.Username,
		})
		if err != nil {
			// Agent call failed; transaction will implicitly rollback, row stays NULL.
			// Caller can retry.
			return fmt.Errorf("agent db.mysqladmin.ensure failed: %w", err)
		}

		// Extract username and password from agent response
		var agentResp map[string]interface{}
		if err := json.Unmarshal(resp, &agentResp); err != nil {
			return fmt.Errorf("unmarshal agent response: %w", err)
		}

		mysqlUsername, ok := agentResp["mysqladmin_username"].(string)
		if !ok || mysqlUsername == "" {
			return errors.New("agent response missing or invalid mysqladmin_username")
		}

		mysqlPassword, ok := agentResp["mysqladmin_password"].(string)
		if !ok || mysqlPassword == "" {
			return errors.New("agent response missing or invalid mysqladmin_password")
		}

		// Encrypt the plaintext password
		encryptedPassword, err := s.ssoKey.Seal([]byte(mysqlPassword))
		if err != nil {
			return fmt.Errorf("encrypt password: %w", err)
		}

		// Update the user row within the transaction
		now := time.Now()
		updateUser := models.User{
			ID:                      userID,
			MysqladminUsername:      &mysqlUsername,
			MysqladminPasswordEnc:   encryptedPassword,
			MysqladminProvisionedAt: &now,
		}

		if err := tx.
			Model(&models.User{}).
			Where("id = ?", userID).
			Updates(updateUser).Error; err != nil {
			// UPDATE failed; agent may have created the user without panel knowing.
			// Next call will re-enter, agent will rotate the password.
			return fmt.Errorf("update user shadow account: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Grant the shadow user exactly this tenant's own databases, and revoke any
	// other database grant it holds, on every open. This replaces the old
	// one-time GRANT ON `<username>\_%`.* wildcard, which also matched a
	// sibling tenant whose username starts with "<username>_". A failure here
	// fails the open: phpMyAdmin must not start with a grant that may still
	// reach another tenant's databases.
	return s.syncMysqlShadowGrants(ctx, userID, *user.Username)
}

// syncMysqlShadowGrants hands the agent the EXPLICIT list of the tenant's own
// MariaDB databases from the control-plane DB (scoped by user_id), never a name
// pattern. An empty list is still sent: it revokes whatever the shadow user
// holds.
func (s *Service) syncMysqlShadowGrants(ctx context.Context, userID, panelUsername string) error {
	var rows []models.Database
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND engine = ?", userID, "mariadb").
		Find(&rows).Error; err != nil {
		return fmt.Errorf("list databases for phpMyAdmin grants: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, d := range rows {
		if d.Name != "" {
			names = append(names, d.Name)
		}
	}
	if _, err := s.agent.Call(ctx, "db.mysqladmin.sync_grants", map[string]interface{}{
		"panel_username": panelUsername,
		"db_names":       names,
	}); err != nil {
		return fmt.Errorf("agent db.mysqladmin.sync_grants: %w", err)
	}
	return nil
}

// MintToken generates a new SSO token, hashes it, and stores it in the DB.
// Returns the plaintext token (base64url-encoded) and any error.
func (s *Service) MintToken(ctx context.Context, userID, databaseID string, dbName string) (string, error) {
	// Generate 32 random bytes
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}

	// Base64url-encode the raw bytes
	plaintextToken := base64.RawURLEncoding.EncodeToString(tokenBytes)

	// SHA-256 hash
	hash := sha256.Sum256(tokenBytes)
	hashStr := fmt.Sprintf("%x", hash[:])

	// Create token row with 5-min TTL
	token := &models.PhpMyAdminSSOToken{
		ID:         ids.NewULID(),
		UserID:     userID,
		DatabaseID: databaseID,
		TokenHash:  hashStr,
		ExpiresAt:  time.Now().Add(s.tokenTTL),
		CreatedAt:  time.Now(),
	}

	if err := s.tokens.Create(ctx, token); err != nil {
		return "", fmt.Errorf("insert sso token: %w", err)
	}

	s.log.DebugContext(ctx, "minted SSO token", "user_id", userID, "db_id", databaseID)

	return plaintextToken, nil
}

