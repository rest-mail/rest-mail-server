package mailimport

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

const metadataMboxSeparator = "From sender@example.test Mon Jan  2 15:04:05 2006\n"

func TestMboxChecksEveryMetadataHeaderOccurrence(t *testing.T) {
	for _, format := range []string{"rfc4155", "mboxrd", "mboxo"} {
		for _, headers := range []string{
			"Status: R\nStatus: Z\n",
			"X-Status: A\nX-Status: Q\n",
			"X-Status: A\nX-Status: Z\n",
			"X-Keywords: \nX-Keywords: bad]keyword\n",
			"Content-Length: \nContent-Length: 4\n",
			"X-IMAP: \nX-IMAP: 1 2\n",
		} {
			t.Run(format+"/"+strings.SplitN(headers, ":", 2)[0], func(t *testing.T) {
				calls := 0
				source := metadataMboxSeparator + "Subject: private source\n" + headers + "\nbody\n\n"
				err := Mbox(t.Context(), strings.NewReader(source), "INBOX", format, 4096, func(string, apitypes.ImportMessageBody) error {
					calls++
					return nil
				})
				if err == nil || calls != 0 {
					t.Fatalf("unsupported metadata reached import: visits%d error%v", calls, err)
				}
			})
		}
	}
}

func TestMboxCombinesSupportedFlagsAndRemovesOnlyTheMetadata(t *testing.T) {
	raw := "Subject: original\nStatus: O\nstatus: R\nX-Status: A\nx-status: F\nX-Status: T\nX-Status: D\nX-Keywords: $Forwarded\nX-Keywords: $Junk\n\nbody\nStatus: R\n"
	want := "Subject: original\n\nbody\nStatus: R\n"
	for _, format := range []string{"rfc4155", "mboxrd", "mboxo"} {
		var messages []apitypes.ImportMessageBody
		err := Mbox(t.Context(), strings.NewReader(metadataMboxSeparator+raw+"\n"), "Archive", format, 4096, func(_ string, message apitypes.ImportMessageBody) error {
			messages = append(messages, message)
			return nil
		})
		if err != nil || len(messages) != 1 {
			t.Fatalf("%s: messages%d error%v", format, len(messages), err)
		}
		if !bytes.Equal(messages[0].RawMessage, []byte(want)) || !reflect.DeepEqual(messages[0].Flags, []string{`\Seen`, `\Deleted`, `\Answered`, `\Flagged`, `\Draft`, "$Forwarded", "$Junk"}) {
			t.Fatalf("%s: raw source or flags changed: %+v", format, messages[0])
		}
	}
}

func TestMboxSizeLimitExcludesEnvelopeFraming(t *testing.T) {
	raw := "Subject: x\n\nbody\n"
	for _, format := range []string{"rfc4155", "mboxrd", "mboxo"} {
		for _, limit := range []int64{int64(len(raw)), int64(len(raw) - 1)} {
			visits := 0
			err := Mbox(context.Background(), strings.NewReader(metadataMboxSeparator+raw+"\n"), "INBOX", format, limit, func(_ string, message apitypes.ImportMessageBody) error {
				visits++
				if string(message.RawMessage) != raw {
					t.Fatal("framing changed the imported message")
				}
				return nil
			})
			if limit == int64(len(raw)) {
				if err != nil || visits != 1 {
					t.Fatalf("%s: exact-size message refused: visits%d error%v", format, visits, err)
				}
			} else if err == nil || visits != 0 {
				t.Fatalf("%s: oversized message accepted: visits%d error%v", format, visits, err)
			}
		}
	}
}

// #1838: a flag-only edit on the source changes only Dovecot's metadata
// headers, so both exports give the same octets and a rerun is skipped by the
// destination's Message-ID and source deduplication instead of copied.
func TestMboxFlagOnlyEditKeepsTheSameOctets(t *testing.T) {
	message := func(metadata string) string {
		return "Return-Path: <a@example.test>\r\nMessage-ID: <734.blf.0.0@fixture.invalid>\r\n" + metadata +
			"Subject: folded\r\n subject\r\n\r\nbody\r\n"
	}
	before := message("Status: RO\r\nX-Status: A\r\nX-UID: 1\r\nX-Keywords: $Forwarded\r\n  $Junk\r\n")
	after := message("X-IMAPbase: 1700000000 2\r\nStatus: O\r\nX-Status: F\r\nX-UID: 7\r\n")
	read := func(source string) apitypes.ImportMessageBody {
		var got []apitypes.ImportMessageBody
		err := Mbox(t.Context(), strings.NewReader(metadataMboxSeparator+source+"\r\n"), "INBOX", "mboxrd", 4096, func(_ string, m apitypes.ImportMessageBody) error {
			got = append(got, m)
			return nil
		})
		if err != nil || len(got) != 1 {
			t.Fatalf("messages%d error%v", len(got), err)
		}
		return got[0]
	}
	first, second := read(before), read(after)
	want := message("")
	if string(first.RawMessage) != want || string(second.RawMessage) != want {
		t.Fatalf("metadata left in the message:\n%q\n%q", first.RawMessage, second.RawMessage)
	}
	if !reflect.DeepEqual(first.Flags, []string{`\Seen`, `\Answered`, "$Forwarded", "$Junk"}) || !reflect.DeepEqual(second.Flags, []string{`\Flagged`}) {
		t.Fatalf("flags: %v then %v", first.Flags, second.Flags)
	}
}
