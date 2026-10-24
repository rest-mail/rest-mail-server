package queue

import (
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

// A queued message is sent as the domain its row names when the envelope
// sender is null, and as the envelope sender's otherwise.
func TestSendsAs(t *testing.T) {
	for _, tc := range []struct {
		item models.OutboundQueue
		want string
	}{
		{models.OutboundQueue{Sender: "alice@hosted.test"}, "hosted.test"},
		{models.OutboundQueue{Sender: "", SendingDomain: "hosted.test"}, "hosted.test"},
		{models.OutboundQueue{Sender: ""}, ""},
	} {
		if got := sendsAs(tc.item); got != tc.want {
			t.Errorf("sendsAs(%+v) = %q, want %q", tc.item, got, tc.want)
		}
	}
}

// Mail sent with a null reverse path is never bounced (RFC 5321 §6.1): the
// worker has no database, so looking for a mailbox to bounce into would panic.
func TestGenerateBounce_NullSenderIsNeverBounced(t *testing.T) {
	w := &Worker{}
	w.generateBounce(models.OutboundQueue{ID: 7, Sender: "", SendingDomain: "hosted.test", Recipient: "bob@example.net"},
		&SMTPError{Code: 550, Enhanced: "5.1.1", Message: "no such user"})
}
