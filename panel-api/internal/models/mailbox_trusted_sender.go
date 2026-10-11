package models

import "time"

// MailboxTrustedSender is one sender a mailbox trusts (GH #2017): mail from
// Address is not treated as spam when it passes SPF or DMARC. Address is
// canonical (mailaddr.CanonicaliseSender). The reconciler writes a mailbox's
// rows into Stalwart as contact cards, which the spam filter trusts.
type MailboxTrustedSender struct {
	ID        string    `gorm:"column:id;type:char(26);primaryKey" json:"id"`
	MailboxID string    `gorm:"column:mailbox_id;type:char(26);not null;uniqueIndex:uq_mailbox_trusted_sender,priority:1" json:"mailbox_id"`
	Address   string    `gorm:"column:address;type:varchar(320);not null;uniqueIndex:uq_mailbox_trusted_sender,priority:2" json:"address"`
	CreatedAt time.Time `gorm:"column:created_at;type:datetime(6);not null" json:"created_at"`
}

func (MailboxTrustedSender) TableName() string { return "mailbox_trusted_senders" }
