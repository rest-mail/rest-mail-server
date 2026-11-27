package messagestore

import (
	"io"
	"strings"
	"testing"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

func TestReferencedWireSizes(t *testing.T) {
	s := Sources{spoolDir: t.TempDir()}
	for name, source := range map[string]string{
		"empty": "",
		"mixed line endings and no final newline": "Subject: ASCII\r\n\nfirst\r\n.second\nthird",
		"8-bit with bare LF":                      "Subject: café\n\nraw \xff",
		"multipart boundary conversion": "Content-Type: multipart/mixed; boundary=\"Jörg\"\r\n\r\n" +
			"--Jörg\r\nContent-Type: application/octet-stream\r\n\r\n\x00\xff\r\n--Jörg--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			var count sourceCounter
			// A CR and LF may arrive in different reads.
			for i := range len(source) {
				_, _ = count.Write([]byte{source[i]})
			}
			got := count.wireSizes(len(source))
			if count.needsConversion {
				var err error
				got, err = s.measureConverted(func() (io.ReadCloser, int64, error) {
					return io.NopCloser(strings.NewReader(source)), int64(len(source)), nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if want := rmail.MeasureWireSizes(source); got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}
