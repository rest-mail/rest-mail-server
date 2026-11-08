package models

// FolderModSeq is the highest mod-sequence one folder of a mailbox has given
// out: its IMAP HIGHESTMODSEQ (RFC 7162 §3.1.2.1). A trigger on messages raises
// it in the same transaction as every change a client can see there: a message
// stored in or moved into the folder, a flag change, and a message leaving it
// by a move, a delete or an expunge. The new value becomes the changed
// message's mod_seq, or the vanished UID's. Nothing lowers it, and it outlives
// the folder's messages and the folder itself.
type FolderModSeq struct {
	MailboxID     uint    `gorm:"primaryKey;autoIncrement:false"`
	Folder        string  `gorm:"primaryKey;size:255"`
	HighestModSeq uint64  `gorm:"column:highest_mod_seq;not null"`
	Mailbox       Mailbox `gorm:"foreignKey:MailboxID;constraint:OnDelete:CASCADE"`
}

// FolderVanishedUID is a message that left one folder of a mailbox, by an
// expunge, a delete or a move, with the folder's mod-sequence at the moment it
// left. IMAP QRESYNC answers VANISHED (EARLIER) from these rows (RFC 7162
// §3.2.5.1, §3.2.6). The trigger that raises FolderModSeq writes them in the
// same transaction; a message that comes back into the folder takes its row
// out again.
type FolderVanishedUID struct {
	MailboxID uint    `gorm:"primaryKey;autoIncrement:false"`
	Folder    string  `gorm:"primaryKey;size:255"`
	UID       uint64  `gorm:"column:uid;primaryKey;autoIncrement:false"`
	ModSeq    uint64  `gorm:"column:mod_seq;not null;index"`
	Mailbox   Mailbox `gorm:"foreignKey:MailboxID;constraint:OnDelete:CASCADE"`
}
