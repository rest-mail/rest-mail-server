package mime

import (
	stdmime "mime"

	gomessage "github.com/emersion/go-message"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// addressDecoder reads the encoded-words of an address list as go-message's mail package does.
var addressDecoder = &stdmime.WordDecoder{CharsetReader: gomessage.CharsetReader}

// addressList is a header's address list, read when something asks for it.
func addressList(value string) pipeline.AddressList {
	return pipeline.ParsedAddresses(value, addressDecoder)
}
