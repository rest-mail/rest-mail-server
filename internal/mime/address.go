package mime

import (
	"bytes"
	stdmime "mime"
	"strings"

	gomessage "github.com/emersion/go-message"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"

	"github.com/rest-mail/rest-mail-server/internal/mailaddr"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// addressDecoder reads the encoded-words of an address list as go-message's mail package does.
var addressDecoder = &stdmime.WordDecoder{CharsetReader: gomessage.CharsetReader}

// unreadCharsets is what every list from a parse is told, one value so that two lists of a field compare
// equal.
var unreadCharsets mailaddr.Unread = charsetUnread

// charsetUnread reports whether go-message's charset reader refuses a charset, which is what a word
// sits in that a mail client is sent as it was written (#1968). A list can hold a word for every few
// octets of its field, and asking that reader costs an error each time, so this asks what it asks with
// nothing allocated. TestCharsetUnreadAgreesWithTheReader holds the two together.
func charsetUnread(charset string) bool {
	if len(charset) > maxCharsetName {
		return true
	}
	var lower [maxCharsetName + 2]byte
	copy(lower[:], "cs")
	named := lower[2:][:len(charset)]
	for i := range len(charset) {
		named[i] = charset[i]
		if 'A' <= named[i] && named[i] <= 'Z' {
			named[i] += 'a' - 'A'
		}
	}
	switch {
	case bytes.Equal(named, []byte("ansi_x3.110-1983")), bytes.Equal(named, []byte("x-utf_8j")):
		return false
	case charset == "" || strings.TrimSpace(charset) != charset || !isASCII(charset):
		_, err := gomessage.CharsetReader(charset, strings.NewReader(""))
		return err != nil
	}
	if enc, err := ianaindex.MIME.Encoding(string(named)); err == nil && enc != nil {
		return false
	}
	if enc, err := ianaindex.MIME.Encoding(string(lower[:2+len(charset)])); err == nil && enc != nil {
		return false
	}
	_, err := htmlindex.Get(string(named))
	return err != nil
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// addressList is a header's address list, read when something asks for it.
func addressList(value string) pipeline.AddressList {
	return pipeline.ParsedAddressesKeeping(value, addressDecoder, &unreadCharsets)
}
