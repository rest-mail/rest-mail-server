package audit

import (
	"fmt"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// verifyBatch is how many entries VerifyDB reads at a time.
const verifyBatch = 1000

// Result is what a check of the chain found. Checked counts the entries that
// held; LastSeq and LastHash are the last of them. When the chain is broken,
// BrokenAtSeq is the first entry that does not hold and Reason says why.
type Result struct {
	OK          bool   `json:"ok"`
	Checked     int64  `json:"checked"`
	FirstSeq    int64  `json:"first_seq"`
	LastSeq     int64  `json:"last_seq"`
	LastHash    string `json:"last_hash"`
	BrokenAtSeq *int64 `json:"broken_at_seq,omitempty"`
	Reason      string `json:"reason,omitempty"`
	// Note is set when the log does not start at seq 1: the check then starts
	// from the first entry's own prev_hash, which nothing here can vouch for.
	Note string `json:"note,omitempty"`
	// Anchor is the anchor the chain was checked against, when one was
	// given. OK then also means the log still holds that entry, unchanged.
	Anchor *Anchor `json:"anchor,omitempty"`
}

// Anchor is a copy of one entry's (seq, hash), kept somewhere the server's
// operator does not control. Whoever owns the database can rewrite an entry
// and recompute every hash after it, and the chain still holds; the anchor no
// longer matches.
type Anchor struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

// chain checks entries one at a time, in the order they should be in.
type chain struct {
	res      Result
	prevSeq  int64
	prevHash string
	anchor   *Anchor
}

func newChain(anchor *Anchor) *chain {
	return &chain{res: Result{OK: true, LastHash: GenesisHash, Anchor: anchor}, prevHash: GenesisHash, anchor: anchor}
}

// add checks the next entry, and reports whether the chain still holds.
func (c *chain) add(e *models.AuditEntry) bool {
	if !c.res.OK {
		return false
	}
	if c.res.Checked == 0 {
		c.res.FirstSeq = e.Seq
		if e.Seq > 1 {
			c.prevSeq, c.prevHash = e.Seq-1, e.PrevHash
			c.res.Note = fmt.Sprintf("the log starts at seq %d; the check starts from that entry's prev_hash", e.Seq)
		}
	}
	switch {
	case e.Seq != c.prevSeq+1:
		return c.broken(e, fmt.Sprintf("expected seq %d, found %d", c.prevSeq+1, e.Seq))
	case e.PrevHash != c.prevHash:
		return c.broken(e, "prev_hash is not the hash of the entry before")
	case Hash(e) != e.Hash:
		return c.broken(e, "hash does not match the entry's contents")
	case c.anchor != nil && e.Seq == c.anchor.Seq && e.Hash != c.anchor.Hash:
		return c.broken(e, "hash does not match the anchor: the log was rewritten up to here")
	}
	c.prevSeq, c.prevHash = e.Seq, e.Hash
	c.res.Checked++
	c.res.LastSeq, c.res.LastHash = e.Seq, e.Hash
	return true
}

func (c *chain) broken(e *models.AuditEntry, reason string) bool {
	seq := e.Seq
	c.res.OK = false
	c.res.BrokenAtSeq = &seq
	c.res.Reason = reason
	return false
}

// finish checks, once every entry has been added, that a chain which held
// also reached the anchor: an anchored entry that is no longer in the log
// means entries were removed.
func (c *chain) finish() Result {
	a := c.anchor
	if !c.res.OK || a == nil {
		return c.res
	}
	switch {
	case c.res.Checked == 0:
		c.anchorMissing(fmt.Sprintf("the log is empty, but the anchor is at seq %d", a.Seq))
	case a.Seq < c.res.FirstSeq:
		c.anchorMissing(fmt.Sprintf("the log starts at seq %d, after the anchor at seq %d", c.res.FirstSeq, a.Seq))
	case a.Seq > c.res.LastSeq:
		c.anchorMissing(fmt.Sprintf("the log ends at seq %d, before the anchor at seq %d", c.res.LastSeq, a.Seq))
	}
	return c.res
}

func (c *chain) anchorMissing(reason string) {
	seq := c.anchor.Seq
	c.res.OK = false
	c.res.BrokenAtSeq = &seq
	c.res.Reason = reason
}

// Verify checks a run of entries, in order: each must follow the one before
// (seq one higher, prev_hash its hash) and hash to its own hash. A run that
// starts above seq 1 is checked from its first entry's prev_hash.
func Verify(entries []models.AuditEntry) Result {
	return VerifyAnchored(entries, nil)
}

// VerifyAnchored is Verify, and with an anchor the run must also hold the
// anchored entry with the anchored hash. A nil anchor checks the chain alone.
func VerifyAnchored(entries []models.AuditEntry, anchor *Anchor) Result {
	c := newChain(anchor)
	for i := range entries {
		if !c.add(&entries[i]) {
			break
		}
	}
	return c.finish()
}

// VerifyDB checks the whole log in the database, from the lowest seq up, a
// batch at a time, and stops at the first entry that does not hold.
func VerifyDB(db *gorm.DB) (Result, error) {
	return VerifyDBAnchored(db, nil)
}

// VerifyDBAnchored is VerifyDB, checked against an anchor as VerifyAnchored is.
func VerifyDBAnchored(db *gorm.DB, anchor *Anchor) (Result, error) {
	c := newChain(anchor)
	for {
		q := db.Order("seq ASC").Limit(verifyBatch)
		if c.res.Checked > 0 {
			q = q.Where("seq > ?", c.prevSeq)
		}
		var batch []models.AuditEntry
		if err := q.Find(&batch).Error; err != nil {
			return Result{}, err
		}
		for i := range batch {
			if !c.add(&batch[i]) {
				return c.res, nil
			}
		}
		if len(batch) < verifyBatch {
			return c.finish(), nil
		}
	}
}
