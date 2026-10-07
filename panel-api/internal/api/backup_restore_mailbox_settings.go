package api

import (
	"context"
	"errors"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailboxops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// restoreMailboxSettings is backupmetadata.MailboxSettings for a restore that
// overwrites the account's existing rows (GH #1993). It changes a mailbox the
// way the mailbox page does, so the mail server follows.
type restoreMailboxSettings struct {
	mailboxes repository.MailboxRepository
	agent     agent.AgentInterface
}

func (s restoreMailboxSettings) SetQuota(ctx context.Context, mb *models.Mailbox, quotaBytes uint64) error {
	if quotaBytes < minMailboxQuotaBytes {
		return mailboxops.ErrQuotaTooSmall
	}
	if mb.EmailCached == "" {
		return errors.New("the mailbox has no address on record")
	}
	if err := s.mailboxes.UpdateQuota(ctx, mb.ID, quotaBytes); err != nil {
		return err
	}
	s.notify(ctx, "mailbox.set_quota", map[string]any{"id": mb.ID, "email": mb.EmailCached, "quota_bytes": quotaBytes})
	return nil
}

func (s restoreMailboxSettings) SetDisabled(ctx context.Context, mb *models.Mailbox, disabled bool) error {
	if err := s.mailboxes.SetDisabled(ctx, mb.ID, disabled); err != nil {
		return err
	}
	// Stalwart answers webmail from its login cache, so a disable flushes it,
	// or the mailbox keeps its webmail access.
	if disabled {
		s.notify(ctx, "mail.auth_cache.flush", map[string]any{})
	}
	return nil
}

func (s restoreMailboxSettings) notify(ctx context.Context, command string, params any) {
	if s.agent == nil {
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, mailboxAgentTimeout)
	defer cancel()
	_, _ = s.agent.Call(agentCtx, command, params)
}
