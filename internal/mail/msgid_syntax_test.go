package mail

import (
	"strings"
	"testing"
)

func TestValidateMsgID(t *testing.T) {
	for _, s := range []string{
		"<restmail-delivery@ext-contract.test>",
		"<CAKx+ab=cd_9@mail.gmail.com>",
		" <a.b.c@example.com>\t",
		`<"quoted local"@example.com>`,
		`<"a\"b"@example.com>`,
		"<id@[192.0.2.1]>",
		"<ünïcode@例え.jp>",
		GenerateMessageID("example.com"),
		"<" + strings.Repeat("a", MaxMsgIDLength-len("<@b>")) + "@b>",
	} {
		if err := ValidateMsgID(s); err != nil {
			t.Errorf("ValidateMsgID(%q) = %v, want valid", s, err)
		}
	}

	for _, tc := range []struct {
		in   string
		want error
	}{
		{"<a@b>\r\nBcc: victim@example.com", ErrMsgIDControl},
		{"<a@b>\nX-Injected: yes", ErrMsgIDControl},
		{"<a@b\r>", ErrMsgIDControl},
		{"<a\x00@b>", ErrMsgIDControl},
		{"<a@b\x7f>", ErrMsgIDControl},
		{"a@b", ErrMsgIDSyntax},
		{"<a@b", ErrMsgIDSyntax},
		{"a@b>", ErrMsgIDSyntax},
		{"<ab>", ErrMsgIDSyntax},
		{"<@b>", ErrMsgIDSyntax},
		{"<a@>", ErrMsgIDSyntax},
		{"<a@b@c>", ErrMsgIDSyntax},
		{"<a..b@c>", ErrMsgIDSyntax},
		{"<.a@c>", ErrMsgIDSyntax},
		{"<a b@c>", ErrMsgIDSyntax},
		{"<a@b c>", ErrMsgIDSyntax},
		{"<a@[b[c]>", ErrMsgIDSyntax},
		{`<"open@b>`, ErrMsgIDSyntax},
		{"<a@b> <c@d>", ErrMsgIDSyntax}, // a Message-ID holds one
		{"", ErrMsgIDSyntax},
		{"   ", ErrMsgIDSyntax},
		{"<a@b> trailing", ErrMsgIDSyntax},
		{"<\xff@b>", ErrMsgIDSyntax},
		{"<" + strings.Repeat("a", MaxMsgIDLength) + "@b>", ErrMsgIDTooLong},
	} {
		if err := ValidateMsgID(tc.in); err != tc.want {
			t.Errorf("ValidateMsgID(%q) = %v, want %v", tc.in, err, tc.want)
		}
	}
}

func TestValidateMsgIDList(t *testing.T) {
	for _, s := range []string{
		"<a@b>",
		"<a@b> <c@d>",
		"<a@b>\t<c@d>  <e.f@g.h>",
		"<a@b><c@d>",
		" <a@b> ",
		// References longer than one line: each msg-id is capped, not the list, because the
		// field folds between them.
		strings.TrimSpace(strings.Repeat("<"+strings.Repeat("x", 60)+"@example.com> ", 40)),
	} {
		if err := ValidateMsgIDList(s); err != nil {
			t.Errorf("ValidateMsgIDList(%q) = %v, want valid", s, err)
		}
	}

	for _, tc := range []struct {
		in   string
		want error
	}{
		{"<a@b>\r\n <c@d>", ErrMsgIDControl}, // folded: the JSON value is unfolded, a line break is never kept
		{"<a@b>\r\nX-Injected: yes", ErrMsgIDControl},
		{"<a@b>\n<c@d>", ErrMsgIDControl},
		{"<a@b>, <c@d>", ErrMsgIDListForm},
		{"a@b c@d", ErrMsgIDListForm},
		{"<a@b> c@d", ErrMsgIDListForm},
		{"", ErrMsgIDListForm},
		{"\t", ErrMsgIDListForm},
		{"<a@b> <" + strings.Repeat("a", MaxMsgIDLength) + "@b>", ErrMsgIDTooLong},
	} {
		if err := ValidateMsgIDList(tc.in); err != tc.want {
			t.Errorf("ValidateMsgIDList(%q) = %v, want %v", tc.in, err, tc.want)
		}
	}
}
