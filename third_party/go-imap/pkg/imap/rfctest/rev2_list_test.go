package rfctest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

type metadataOnlyListMailbox struct {
	mailbox
	folders, messages, fetches int
}

func (m *metadataOnlyListMailbox) Folders() ([]imap.Folder, error) {
	m.folders++
	folders := make([]imap.Folder, 128)
	for i := range folders {
		folders[i].Name = fmt.Sprintf("folder-%03d", i)
	}
	return folders, nil
}

func (m *metadataOnlyListMailbox) Messages(string) ([]imap.Message, error) {
	m.messages++
	return nil, errors.New("message reads are not part of this LIST")
}

func (m *metadataOnlyListMailbox) Fetch(uint32) ([]byte, error) {
	m.fetches++
	return nil, errors.New("message content is not part of this LIST")
}

// TestListMetadataDoesNotReadMessages proves basic LIST and CHILDREN obtain
// names and hierarchy from one folder snapshot, without per-mailbox message
// counts or content reads. It does not claim a latency bound for host storage
// or linear complexity of the engine's hierarchy scans.
//
// checklist: IMAP2-CMD-94
func TestListMetadataDoesNotReadMessages(t *testing.T) {
	for _, tc := range []struct {
		command string
		count   int
	}{
		{`LIST "" "*"`, 128},
		{`LIST "" "%%" RETURN (CHILDREN)`, 128},
		{`LIST "" "NoSuch*"`, 0},
	} {
		t.Run(tc.command, func(t *testing.T) {
			box := &metadataOnlyListMailbox{}
			c := selectionClient(t, box)
			lines, completion := c.command("L1", tc.command)
			requireOK(t, completion, "L1")
			if len(lines) != tc.count {
				t.Fatalf("got %d mailbox responses, want %d", len(lines), tc.count)
			}
			if box.folders != 1 || box.messages != 0 || box.fetches != 0 {
				t.Fatalf("LIST calls: folders=%d messages=%d fetches=%d", box.folders, box.messages, box.fetches)
			}
		})
	}
}
