package mailimport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	goimap "github.com/rest-mail/go-imap/pkg/imap"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"io"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Message = apitypes.ImportMessageBody
type Result = apitypes.ImportMessageResult
type Visit func(string, Message) error

func foldInbox(name string) string {
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}

// Mbox accepts From-framed rfc4155, mboxrd or mboxo. The caller chooses the
// escaping format explicitly; no Content-Length-framed variant is guessed.
func Mbox(ctx context.Context, source io.Reader, folder, format string, maxBytes int64, visit Visit) error {
	return readMbox(ctx, source, folder, format, maxBytes, visit, false, nil)
}

func Thunderbird(ctx context.Context, source io.Reader, folder string, maxBytes int64, visit Visit, expunged func()) error {
	return readMbox(ctx, source, folder, "mboxrd", maxBytes, visit, true, expunged)
}

func readMbox(ctx context.Context, source io.Reader, folder, format string, maxBytes int64, visit Visit, thunderbird bool, expunged func()) error {
	if format != "rfc4155" && format != "mboxrd" && format != "mboxo" {
		return errors.New("mbox format must be rfc4155, mboxrd or mboxo")
	}
	if maxBytes < 1 || maxBytes > 1<<30 {
		return errors.New("message size limit must be between 1 byte and 1 GiB")
	}
	if folder == "" {
		folder = "INBOX"
	}
	if problem := rmail.FolderNameProblem(folder); problem != "" {
		return fmt.Errorf("destination folder: %s", problem)
	}
	folder = foldInbox(folder)
	r := bufio.NewReader(source)
	var raw []byte
	var date time.Time
	count := 0
	inBody := false
	discard := false
	flush := func() error {
		if count == 0 || !thunderbird && date.IsZero() {
			return nil
		}
		if discard {
			if expunged != nil {
				expunged()
			}
			return nil
		}
		// Remove only mbox's framing blank line, preserving the message's
		// own header/body separator, line endings and final body newline.
		boundary, width := bytes.Index(raw, []byte("\r\n\r\n")), 4
		if boundary < 0 {
			boundary, width = bytes.Index(raw, []byte("\n\n")), 2
		}
		if boundary >= 0 && boundary+width < len(raw) {
			if bytes.HasSuffix(raw, []byte("\r\n\r\n")) {
				raw = raw[:len(raw)-2]
			} else if bytes.HasSuffix(raw, []byte("\n\n")) {
				raw = raw[:len(raw)-1]
			}
		}
		if int64(len(raw)) > maxBytes {
			return fmt.Errorf("mbox message %d exceeds the size limit", count)
		}
		message, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("mbox message %d has invalid headers", count)
		}
		if thunderbird {
			flags, deleted, err := mozillaFlags(message.Header)
			if err != nil {
				return fmt.Errorf("mbox message %d: %w", count, err)
			}
			if deleted {
				if expunged != nil {
					expunged()
				}
				return nil
			}
			internalDate := date
			if internalDate.IsZero() {
				internalDate, err = mail.ParseDate(message.Header.Get("Date"))
				if err != nil {
					return fmt.Errorf("mbox message %d has no valid separator or Date timestamp", count)
				}
			}
			return visit(fmt.Sprintf("message %d", count), Message{RawMessage: withoutMetadata(raw, append([]string{"x-mozilla-status", "x-mozilla-status2", "x-mozilla-keys"}, mboxMetadataFields...)), Folder: folder, Flags: flags, InternalDate: internalDate})
		}
		if _, present := message.Header["Content-Length"]; present {
			return errors.New("Content-Length-framed mbox is unsupported; export as From-framed mboxrd or RFC4155")
		}
		if _, present := message.Header["X-Imap"]; present {
			return errors.New("a Dovecot metadata-only mbox pseudo-message requires an IMAP export; it was not imported as mail")
		}
		var keywords []string
		for _, value := range message.Header["X-Keywords"] {
			keywords = append(keywords, strings.Fields(value)...)
		}
		if _, err := goimap.NormalizeImportFlags(keywords); err != nil {
			return fmt.Errorf("mbox keywords: %w", err)
		}
		for _, keyword := range keywords {
			if strings.Contains(keyword, `\`) {
				return errors.New("mbox keywords must not contain a backslash")
			}
		}
		// Header.Get sees only the first occurrence. Preserve supported flags
		// from every occurrence, and refuse unsupported metadata wherever it is.
		status := strings.Join(message.Header["Status"], "")
		xStatus := strings.Join(message.Header["X-Status"], "")
		for _, metadata := range []struct{ name, value, allowed string }{{"Status", status, "RO"}, {"X-Status", xStatus, "AFTD"}} {
			for _, flag := range metadata.value {
				if !strings.ContainsRune(metadata.allowed, flag) {
					return fmt.Errorf("mbox %s flag %q is unsupported; it was not silently discarded", metadata.name, flag)
				}
			}
		}
		flags := make([]string, 0, 5)
		if strings.Contains(status, "R") {
			flags = append(flags, `\Seen`)
		}
		for _, pair := range []struct{ letter, flag string }{{"D", `\Deleted`}, {"A", `\Answered`}, {"F", `\Flagged`}, {"T", `\Draft`}} {
			if strings.Contains(xStatus, pair.letter) {
				flags = append(flags, pair.flag)
			}
		}
		flags, err = goimap.NormalizeImportFlags(append(flags, keywords...))
		if err != nil {
			return err
		}
		// The metadata is now in flags. Like Dovecot serving the message over
		// IMAP, store it without them: a later flag edit on the source then
		// leaves the same octets, so a rerun skips it instead of copying it.
		return visit(fmt.Sprintf("message %d", count), apitypes.ImportMessageBody{RawMessage: withoutMboxMetadata(raw), Folder: folder, Flags: flags, InternalDate: date})
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The From separator is framing, not message content. Even a tiny
		// message limit needs room to read it; raw message bytes stay bounded below.
		var line []byte
		var err error
		if discard {
			line, err = discardedLine(ctx, r)
		} else {
			line, err = boundedLine(r, max(maxBytes+2, 4096))
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) != 0 {
			next, separator := mboxDate(line)
			if separator || thunderbird && bytes.HasPrefix(line, []byte("From ")) {
				if err := flush(); err != nil {
					return err
				}
				date, raw, inBody = next, nil, false
				discard = false
				count++
			} else {
				if count == 0 || !thunderbird && date.IsZero() {
					return errors.New("mbox must begin with a complete From separator and timestamp")
				}
				if discard {
					if errors.Is(err, io.EOF) {
						return flush()
					}
					continue
				}
				if inBody && format != "rfc4155" && len(line) > 0 && line[0] == '>' {
					unquoted := bytes.TrimLeft(line, ">")
					if bytes.HasPrefix(unquoted, []byte("From ")) && (format == "mboxrd" || bytes.HasPrefix(line, []byte(">From "))) {
						line = line[1:]
					}
				}
				if bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
					if thunderbird && !inBody {
						header, headerErr := mail.ReadMessage(bytes.NewReader(append(raw, line...)))
						if headerErr == nil {
							_, skip, statusErr := mozillaFlags(header.Header)
							if statusErr == nil && skip {
								discard = true
								raw = nil
							}
						}
					}
					inBody = true
				}
				if int64(len(raw)+len(line)) > maxBytes+2 {
					return fmt.Errorf("mbox message %d exceeds the size limit", count)
				}
				raw = append(raw, line...)
			}
		}
		if errors.Is(err, io.EOF) {
			return flush()
		}
	}
}

func discardedLine(ctx context.Context, reader *bufio.Reader) ([]byte, error) {
	var prefix []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := reader.ReadSlice('\n')
		if prefix == nil {
			prefix = append([]byte{}, part[:min(len(part), 4096)]...)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return prefix, err
		}
	}
}

// mboxMetadataFields are the header fields Dovecot keeps a message's mailbox
// state in, not part of the message itself.
var mboxMetadataFields = []string{"status", "x-status", "x-keywords", "x-uid", "x-imapbase"}

// withoutMboxMetadata is raw with every mboxMetadataFields field removed from
// its header, continuation lines included. Every other octet is kept.
func withoutMboxMetadata(raw []byte) []byte {
	return withoutMetadata(raw, mboxMetadataFields)
}

func withoutMetadata(raw []byte, fields []string) []byte {
	out := make([]byte, 0, len(raw))
	dropping := false
	for rest := raw; len(rest) > 0; {
		end := bytes.IndexByte(rest, '\n') + 1
		if end == 0 {
			end = len(rest)
		}
		line := rest[:end]
		if bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
			return append(out, rest...)
		}
		if line[0] != ' ' && line[0] != '\t' {
			name, _, _ := bytes.Cut(line, []byte(":"))
			dropping = slices.Contains(fields, strings.ToLower(string(bytes.TrimSpace(name))))
		}
		if !dropping {
			out = append(out, line...)
		}
		rest = rest[end:]
	}
	return out
}

func mozillaFlags(header mail.Header) ([]string, bool, error) {
	var status uint64
	for _, field := range []struct {
		name string
		mask uint64
	}{{"X-Mozilla-Status", 0xffff}, {"X-Mozilla-Status2", 0xffff0000}} {
		for _, text := range header[field.name] {
			value, err := strconv.ParseUint(strings.TrimSpace(text), 16, 32)
			if err != nil {
				return nil, false, errors.New("invalid Thunderbird status flags")
			}
			status |= value & field.mask
		}
	}
	if status&8 != 0 {
		return nil, true, nil
	}
	if status&0x400 != 0 {
		return nil, false, errors.New("partial Thunderbird message; download the complete message first")
	}
	flags := []string{}
	for _, pair := range []struct {
		bit  uint64
		flag string
	}{{1, `\Seen`}, {2, `\Answered`}, {4, `\Flagged`}, {0x00200000, `\Deleted`}} {
		if status&pair.bit != 0 {
			flags = append(flags, pair.flag)
		}
	}
	for _, text := range header["X-Mozilla-Keys"] {
		flags = append(flags, strings.Fields(text)...)
	}
	flags, err := goimap.NormalizeImportFlags(flags)
	if err != nil {
		return nil, false, errors.New("unsupported Thunderbird keywords")
	}
	return flags, false, nil
}

func boundedLine(r *bufio.Reader, limit int64) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if int64(len(line)+len(part)) > limit {
			return nil, errors.New("mbox line exceeds the message size limit")
		}
		line = append(line, part...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func mboxDate(line []byte) (time.Time, bool) {
	text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
	if !strings.HasPrefix(text, "From ") {
		return time.Time{}, false
	}
	sender, stamp, found := strings.Cut(text[len("From "):], " ")
	if !found || sender == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.ANSIC, "Mon Jan _2 15:04:05 -0700 2006", "Mon Jan _2 15:04:05 2006 -0700"} {
		if date, err := time.ParseInLocation(layout, stamp, time.UTC); err == nil {
			return date, true
		}
	}
	return time.Time{}, false
}
