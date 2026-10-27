package mailimport

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	apitypes "github.com/restmail/restmail/internal/api/types"
)

const metadataMboxSeparator = "From sender@example.test Mon Jan  2 15:04:05 2006\n"

func TestMboxChecksEveryMetadataHeaderOccurrence(t *testing.T) {
	for _, format := range []string{"rfc4155", "mboxrd", "mboxo"} {
		for _, headers := range []string{
			"Status: R\nStatus: Z\n",
			"X-Status: A\nX-Status: D\n",
			"X-Status: A\nX-Status: Z\n",
			"X-Keywords: \nX-Keywords: private-keyword\n",
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

func TestMboxCombinesSupportedFlagsWithoutRewritingRawHeaders(t *testing.T) {
	raw := "Subject: original\nStatus: O\nstatus: R\nX-Status: A\nx-status: F\nX-Status: T\n\nbody\n"
	for _, format := range []string{"rfc4155", "mboxrd", "mboxo"} {
		var messages []apitypes.ImportMessageBody
		err := Mbox(t.Context(), strings.NewReader(metadataMboxSeparator+raw+"\n"), "Archive", format, 4096, func(_ string, message apitypes.ImportMessageBody) error {
			messages = append(messages, message)
			return nil
		})
		if err != nil || len(messages) != 1 {
			t.Fatalf("%s: messages%d error%v", format, len(messages), err)
		}
		if !bytes.Equal(messages[0].RawMessage, []byte(raw)) || !reflect.DeepEqual(messages[0].Flags, []string{`\Seen`, `\Answered`, `\Flagged`, `\Draft`}) {
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
