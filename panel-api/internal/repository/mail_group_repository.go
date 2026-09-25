package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// MailGroupRepository is the data access layer for mail groups + their
// membership edges (M51, ADR-0132). panel-API is the only writer; the
// reconciler reads these rows to converge the Stalwart registry.
//
// EmailCached is maintained by BEFORE INSERT/UPDATE triggers from
// migration 000170 — never set it here directly.
type MailGroupRepository interface {
	FindByID(ctx context.Context, id string) (*models.MailGroup, error)
	ListByDomainID(ctx context.Context, domainID string) ([]MailGroupWithCount, error)
	ListAllWithDomain(ctx context.Context) ([]MailGroupWithDomain, error)
	Create(ctx context.Context, g *models.MailGroup) error
	UpdateMeta(ctx context.Context, id, displayName, description string) error
	UpdateResources(ctx context.Context, id string, mailbox, calendar, addressbook, files bool) error
	// UpdateInternalOnly toggles the group's internal-delivery-only flag (GH #348).
	UpdateInternalOnly(ctx context.Context, id string, internalOnly bool) error
	Delete(ctx context.Context, id string) error
	ExistsByDomainAndLocalPart(ctx context.Context, domainID, localPart string) (bool, error)

	// Membership.
	ListMemberMailboxIDs(ctx context.Context, groupID string) ([]string, error)
	ListMemberEmails(ctx context.Context, groupID string) ([]string, error)
	SetMembers(ctx context.Context, groupID string, mailboxIDs []string) error
	AddMember(ctx context.Context, groupID, mailboxID string) error
	RemoveMember(ctx context.Context, groupID, mailboxID string) error

	// ListMembershipsByDomain returns every (mailbox -> group) edge in a
	// domain, joined with the group's display name + address — for the mail
	// users "Groups" column (#238).
	ListMembershipsByDomain(ctx context.Context, domainID string) ([]MailboxGroupMembership, error)

	// ListMembershipsByUserID returns every (mailbox -> group) edge across all
	// domains a user owns — the owner-scoped bulk projection for the tenant
	// Mailboxes tab's cross-domain view (JAB-370 Selection). Mirrors
	// ListMembershipsByDomain but scopes by the domain's owner, not one domain.
	ListMembershipsByUserID(ctx context.Context, userID string) ([]MailboxGroupMembership, error)

	// ListDeliverableMemberEmails returns the members of one group that can
	// receive mail (not disabled, not send-only) — the recipient set of a
	// distribution list (GH #1818). Same gate as the SQL directory's
	// queryRecipient for a mailbox.
	ListDeliverableMemberEmails(ctx context.Context, groupID string) ([]string, error)
	// ListAllDeliverableMembers is ListDeliverableMemberEmails for every group
	// in one query, for the reconcile pass.
	ListAllDeliverableMembers(ctx context.Context) ([]MailGroupMemberEmail, error)
}

// MailGroupMemberEmail is one deliverable (group, member address) pair.
type MailGroupMemberEmail struct {
	GroupID string `gorm:"column:group_id"`
	Email   string `gorm:"column:email"`
}

// MailboxGroupMembership is one mailbox->group edge with the group's label.
type MailboxGroupMembership struct {
	MailboxID  string `gorm:"column:mailbox_id" json:"mailbox_id"`
	GroupID    string `gorm:"column:group_id" json:"group_id"`
	GroupName  string `gorm:"column:group_name" json:"group_name"`
	GroupEmail string `gorm:"column:group_email" json:"group_email"`
}

// MailGroupWithCount is a group plus its member count, for list views.
type MailGroupWithCount struct {
	models.MailGroup
	MemberCount int64 `gorm:"column:member_count" json:"member_count"`
}

// MailGroupWithDomain is a group joined with its domain + owner, for the
// admin server-wide groups table (mirrors MailboxWithDomain).
type MailGroupWithDomain struct {
	models.MailGroup
	DomainName   string `gorm:"column:domain_name" json:"domain_name"`
	OwnerUserID  string `gorm:"column:owner_user_id" json:"owner_user_id"`
	UserUsername string `gorm:"column:user_username" json:"user_username"`
	MemberCount  int64  `gorm:"column:member_count" json:"member_count"`
}

type mailGroupRepo struct{ db *gorm.DB }

func NewMailGroupRepository(db *gorm.DB) MailGroupRepository {
	return &mailGroupRepo{db: db}
}

func (r *mailGroupRepo) FindByID(ctx context.Context, id string) (*models.MailGroup, error) {
	var g models.MailGroup
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &g, nil
}

func (r *mailGroupRepo) ListByDomainID(ctx context.Context, domainID string) ([]MailGroupWithCount, error) {
	var rows []MailGroupWithCount
	err := r.db.WithContext(ctx).
		Table("mail_groups g").
		Select("g.*, (SELECT COUNT(*) FROM mail_group_members m WHERE m.group_id = g.id) AS member_count").
		Where("g.domain_id = ?", domainID).
		Order("g.email_cached ASC").
		Scan(&rows).Error
	return rows, err
}

func (r *mailGroupRepo) ListAllWithDomain(ctx context.Context) ([]MailGroupWithDomain, error) {
	var rows []MailGroupWithDomain
	err := r.db.WithContext(ctx).
		Table("mail_groups g").
		Select("g.*, d.name AS domain_name, d.user_id AS owner_user_id, COALESCE(u.username, '') AS user_username, " +
			"(SELECT COUNT(*) FROM mail_group_members m WHERE m.group_id = g.id) AS member_count").
		Joins("JOIN domains d ON d.id = g.domain_id").
		Joins("LEFT JOIN users u ON u.id = d.user_id").
		Order("g.email_cached ASC").
		Scan(&rows).Error
	return rows, err
}

func (r *mailGroupRepo) Create(ctx context.Context, g *models.MailGroup) error {
	return r.db.WithContext(ctx).Create(g).Error
}

// UpdateMeta updates the display name + description. Dedicated method (no
// Select allowlist) per the domain.Update-allowlist lesson — a column not
// in an allowlist gets silently dropped.
func (r *mailGroupRepo) UpdateMeta(ctx context.Context, id, displayName, description string) error {
	res := r.db.WithContext(ctx).
		Model(&models.MailGroup{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"display_name": displayName,
			"description":  description,
			"updated_at":   time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *mailGroupRepo) UpdateInternalOnly(ctx context.Context, id string, internalOnly bool) error {
	res := r.db.WithContext(ctx).
		Model(&models.MailGroup{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"internal_only": internalOnly,
			"updated_at":    time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *mailGroupRepo) UpdateResources(ctx context.Context, id string, mailbox, calendar, addressbook, files bool) error {
	res := r.db.WithContext(ctx).
		Model(&models.MailGroup{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"has_mailbox":     mailbox,
			"has_calendar":    calendar,
			"has_addressbook": addressbook,
			"has_files":       files,
			"updated_at":      time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *mailGroupRepo) Delete(ctx context.Context, id string) error {
	// mail_group_members cascade via FK.
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.MailGroup{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *mailGroupRepo) ExistsByDomainAndLocalPart(ctx context.Context, domainID, localPart string) (bool, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&models.MailGroup{}).
		Where("domain_id = ? AND local_part = ?", domainID, localPart).
		Count(&n).Error
	return n > 0, err
}

func (r *mailGroupRepo) ListMemberMailboxIDs(ctx context.Context, groupID string) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).
		Model(&models.MailGroupMember{}).
		Where("group_id = ?", groupID).
		Order("mailbox_id ASC").
		Pluck("mailbox_id", &ids).Error
	return ids, err
}

// ListMemberEmails returns the cached email of every member mailbox — the
// shape the agent needs to patch each member's memberGroupIds.
func (r *mailGroupRepo) ListMemberEmails(ctx context.Context, groupID string) ([]string, error) {
	var emails []string
	err := r.db.WithContext(ctx).
		Table("mail_group_members m").
		Joins("JOIN mailboxes mb ON mb.id = m.mailbox_id").
		Where("m.group_id = ?", groupID).
		Order("mb.email_cached ASC").
		Pluck("mb.email_cached", &emails).Error
	return emails, err
}

// SetMembers replaces the membership set in one transaction: delete edges
// no longer wanted, insert new ones. Idempotent — re-running with the same
// set is a no-op at the row level.
func (r *mailGroupRepo) SetMembers(ctx context.Context, groupID string, mailboxIDs []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(mailboxIDs) == 0 {
			return tx.Where("group_id = ?", groupID).Delete(&models.MailGroupMember{}).Error
		}
		// Drop edges not in the desired set.
		if err := tx.Where("group_id = ? AND mailbox_id NOT IN ?", groupID, mailboxIDs).
			Delete(&models.MailGroupMember{}).Error; err != nil {
			return err
		}
		// Insert the desired set, ignoring rows that already exist.
		now := time.Now().UTC()
		rows := make([]models.MailGroupMember, 0, len(mailboxIDs))
		for _, mid := range mailboxIDs {
			rows = append(rows, models.MailGroupMember{GroupID: groupID, MailboxID: mid, CreatedAt: now})
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	})
}

func (r *mailGroupRepo) AddMember(ctx context.Context, groupID, mailboxID string) error {
	row := models.MailGroupMember{GroupID: groupID, MailboxID: mailboxID, CreatedAt: time.Now().UTC()}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

// RemoveMember drops a single mailbox↔group edge. Idempotent — removing a
// mailbox that isn't a member is a no-op (GH #238).
func (r *mailGroupRepo) RemoveMember(ctx context.Context, groupID, mailboxID string) error {
	return r.db.WithContext(ctx).
		Where("group_id = ? AND mailbox_id = ?", groupID, mailboxID).
		Delete(&models.MailGroupMember{}).Error
}

func (r *mailGroupRepo) ListMembershipsByDomain(ctx context.Context, domainID string) ([]MailboxGroupMembership, error) {
	var rows []MailboxGroupMembership
	err := r.db.WithContext(ctx).
		Table("mail_group_members m").
		Select("m.mailbox_id, g.id AS group_id, g.display_name AS group_name, g.email_cached AS group_email").
		Joins("JOIN mail_groups g ON g.id = m.group_id").
		Where("g.domain_id = ?", domainID).
		Order("g.display_name ASC").
		Scan(&rows).Error
	return rows, err
}

func (r *mailGroupRepo) ListMembershipsByUserID(ctx context.Context, userID string) ([]MailboxGroupMembership, error) {
	var rows []MailboxGroupMembership
	err := r.db.WithContext(ctx).
		Table("mail_group_members m").
		Select("m.mailbox_id, g.id AS group_id, g.display_name AS group_name, g.email_cached AS group_email").
		Joins("JOIN mail_groups g ON g.id = m.group_id").
		Joins("JOIN domains d ON d.id = g.domain_id").
		Where("d.user_id = ?", userID).
		Order("g.display_name ASC").
		Scan(&rows).Error
	return rows, err
}

func (r *mailGroupRepo) ListDeliverableMemberEmails(ctx context.Context, groupID string) ([]string, error) {
	emails := []string{}
	err := r.db.WithContext(ctx).
		Table("mail_group_members m").
		Joins("JOIN mailboxes mb ON mb.id = m.mailbox_id").
		Where("m.group_id = ? AND mb.is_disabled = ? AND mb.send_only = ?", groupID, false, false).
		Order("mb.email_cached ASC").
		Pluck("mb.email_cached", &emails).Error
	return emails, err
}

func (r *mailGroupRepo) ListAllDeliverableMembers(ctx context.Context) ([]MailGroupMemberEmail, error) {
	var rows []MailGroupMemberEmail
	err := r.db.WithContext(ctx).
		Table("mail_group_members m").
		Select("m.group_id, mb.email_cached AS email").
		Joins("JOIN mailboxes mb ON mb.id = m.mailbox_id").
		Where("mb.is_disabled = ? AND mb.send_only = ?", false, false).
		Order("m.group_id ASC, mb.email_cached ASC").
		Scan(&rows).Error
	return rows, err
}
