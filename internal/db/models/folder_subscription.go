package models

import "time"

// FolderSubscription is one name the account has subscribed or unsubscribed on
// its IMAP subscription list (RFC 3501 §6.3.6, RFC 9051 §6.3.7): the folders
// LSUB and LIST (SUBSCRIBED) return. The name is a store name, as
// messages.folder holds it, and need not exist: a folder deleted or renamed
// away while subscribed stays on the list, which RFC 9051 §6.3.7 asks for
// ("SHOULD NOT unilaterally remove an existing mailbox name from the
// subscription list even if a mailbox by that name no longer exists") and
// §6.3.6 leaves to the client on RENAME ("renaming a mailbox doesn't update
// subscription information on the original name"). So nothing here follows a
// folder: only SUBSCRIBE and UNSUBSCRIBE change the list.
//
// The standard folders (INBOX, Sent, Drafts, Trash, Spam, Archive) are
// subscribed until the account unsubscribes one, so a mail app that shows only
// subscribed folders (Thunderbird's "show only subscribed") still shows them
// (#1675). A row records only what the account chose: Subscribed false
// unsubscribes a standard folder. Creating a mailbox writes no row.
type FolderSubscription struct {
	MailboxID  uint      `gorm:"primaryKey;autoIncrement:false"`
	Folder     string    `gorm:"primaryKey;size:255"`
	Subscribed bool      `gorm:"not null"`
	CreatedAt  time.Time `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
	Mailbox    Mailbox   `gorm:"foreignKey:MailboxID;constraint:OnDelete:CASCADE"`
}
