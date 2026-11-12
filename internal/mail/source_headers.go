package mail

import (
	"bufio"
	"io"
	"net/textproto"
)

func SourceHeaders(source io.Reader) map[string][]string {
	fields, _ := textproto.NewReader(bufio.NewReader(source)).ReadMIMEHeader()
	return fields
}
