package mailimport

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// MaildirDirectory is the Maildir++ directory holding a REST Mail folder, the
// layout Maildir and Dovecot read: INBOX is the root "." and every other folder
// a dot folder, each "/" level written in modified UTF-7 (RFC 3501 §5.1.3) and
// joined by ".". A level containing "." is refused, since Maildir++ would read
// it back as a subfolder.
func MaildirDirectory(folder string) (string, error) {
	if folder == "INBOX" {
		return ".", nil
	}
	if strings.Contains(folder, ".") {
		return "", fmt.Errorf("folder %q contains \".\", which Maildir++ reads as a subfolder; rename it before exporting", folder)
	}
	levels := strings.Split(folder, "/")
	for i, level := range levels {
		levels[i] = encodeMailboxName(level)
	}
	return "." + strings.Join(levels, "."), nil
}

// maildirFolder is the folder a Maildir++ directory holds, MaildirDirectory
// read back. Each "."-separated level is a modified UTF-7 name, as Dovecot
// writes it, and is decoded. A level with octets outside US-ASCII cannot be
// modified UTF-7, so it is taken as the UTF-8 name a Maildir written with
// UTF-8 names holds. A level that is neither keeps its literal text, and reason
// says why the folder's name is derived; the import never stops over a name.
func maildirFolder(directory string) (folder, reason string) {
	if directory == "." {
		return "INBOX", ""
	}
	levels := []string{directory}
	if strings.HasPrefix(directory, ".") {
		levels = strings.Split(strings.TrimPrefix(directory, "."), ".")
	}
	for i, level := range levels {
		switch {
		case !strings.HasPrefix(directory, "."):
		case !isASCII(level):
			if !utf8.ValidString(level) {
				reason = "neither modified UTF-7 nor UTF-8; imported under its literal text"
			}
		default:
			if decoded, ok := decodeMailboxName(level); ok {
				levels[i] = decoded
			} else if reason == "" {
				reason = "not valid modified UTF-7 (RFC 3501 §5.1.3); imported under its literal text"
			}
		}
	}
	folder = rmail.CanonicalFolder(strings.Join(levels, "/"))
	if problem := rmail.FolderNameProblem(folder); problem != "" {
		folder = safeFolder(folder)
		if reason == "" {
			reason = "not a valid folder name (" + problem + ")"
		}
	} else if reason != "" {
		folder = safeFolder(folder)
	}
	return folder, reason
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// mailboxBase64 is modified BASE64: base64 with "," for "/" and no padding.
var mailboxBase64 = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+,").WithPadding(base64.NoPadding).Strict()

// encodeMailboxName writes a UTF-8 name in modified UTF-7: printable US-ASCII
// other than "&" stands for itself, "&" is "&-", and each run of other
// characters is "&", its UTF-16 in modified BASE64, then "-".
func encodeMailboxName(name string) string {
	var b strings.Builder
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		units := utf16.Encode(run)
		buf := make([]byte, 0, 2*len(units))
		for _, u := range units {
			buf = append(buf, byte(u>>8), byte(u))
		}
		b.WriteByte('&')
		b.WriteString(mailboxBase64.EncodeToString(buf))
		b.WriteByte('-')
		run = run[:0]
	}
	for _, r := range name {
		switch {
		case r == '&':
			flush()
			b.WriteString("&-")
		case r >= 0x20 && r <= 0x7e:
			flush()
			b.WriteRune(r)
		default:
			run = append(run, r)
		}
	}
	flush()
	return b.String()
}

// decodeMailboxName reads a modified UTF-7 name. ok is false unless name is
// exactly what encodeMailboxName writes for its decoding, so an unclosed run, a
// run holding characters that represent themselves, a superfluous shift,
// nonzero padding bits or an unpaired surrogate is refused.
func decodeMailboxName(name string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(name); {
		c := name[i]
		if c < 0x20 || c > 0x7e {
			return "", false
		}
		if c != '&' {
			b.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(name[i+1:], '-')
		if end < 0 {
			return "", false
		}
		run := name[i+1 : i+1+end]
		i += end + 2
		if run == "" {
			b.WriteByte('&')
			continue
		}
		buf, err := mailboxBase64.DecodeString(run)
		if err != nil || len(buf)%2 != 0 {
			return "", false
		}
		units := make([]uint16, len(buf)/2)
		for j := range units {
			units[j] = uint16(buf[2*j])<<8 | uint16(buf[2*j+1])
		}
		for _, r := range utf16.Decode(units) {
			b.WriteRune(r)
		}
	}
	decoded := b.String()
	if encodeMailboxName(decoded) != name {
		return "", false
	}
	return decoded, true
}
