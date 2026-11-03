package mail

import "testing"

func TestReplyReferences(t *testing.T) {
	for _, test := range []struct{ name, parent, inReplyTo, references, want string }{
		{"root", "<root@example.test>", "", "", "<root@example.test>"},
		{"chain", "<parent@example.test>", "<previous@example.test>", "<root@example.test>\r\n <previous@example.test>", "<root@example.test> <previous@example.test> <parent@example.test>"},
		{"single parent fallback", "parent@example.test", "root@example.test", "", "<root@example.test> <parent@example.test>"},
		{"multiple parents omitted", "<parent@example.test>", "<first@example.test> <second@example.test>", "", "<parent@example.test>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ReplyReferences(test.parent, test.inReplyTo, test.references); got != test.want {
				t.Errorf("References = %q, want %q", got, test.want)
			}
		})
	}
}
