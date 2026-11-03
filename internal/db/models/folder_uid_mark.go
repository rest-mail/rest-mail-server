package models

// FolderUIDMark is the highest message ID ever stored in one folder of a
// mailbox. A message's ID is its IMAP UID, so one more than this is the
// folder's UIDNEXT (RFC 3501 §2.3.1.1): it must not fall when messages are
// expunged, which a maximum over the rows still present would (#804). A
// trigger on messages raises it in the same transaction as every insert and
// every move of a row into the folder; nothing lowers it, and it outlives the
// folder's messages and the folder itself.
type FolderUIDMark struct {
	MailboxID  uint    `gorm:"primaryKey;autoIncrement:false"`
	Folder     string  `gorm:"primaryKey;size:255"`
	HighestUID uint64  `gorm:"column:highest_uid;not null"`
	Mailbox    Mailbox `gorm:"foreignKey:MailboxID;constraint:OnDelete:CASCADE"`
}
