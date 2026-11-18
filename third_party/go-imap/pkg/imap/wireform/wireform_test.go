package wireform_test

import (
	"io"
	"mime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
)

func TestSizeMatchesConvertedOctets(t *testing.T) {
	cases := map[string]struct {
		block string
		body  string
	}{
		"unchanged ASCII": {
			block: "Subject: hello\r\n\r\n",
			body:  "plain body\r\n",
		},
		"encoded header": {
			block: "Subject: café\r\n\r\n",
			body:  "plain body\r\n",
		},
		"text without charset": {
			block: "Content-Type: text/plain\r\n\r\n",
			body:  "café\r\n",
		},
		"text with charset": {
			block: "Content-Type: text/plain; charset=utf-8\r\n\r\n",
			body:  "café\r\n",
		},
		"binary body": {
			block: "Content-Type: application/octet-stream\r\n\r\n",
			body:  "\x00\xff\xfe\r\n",
		},
		"multipart": {
			block: "Content-Type: multipart/mixed; boundary=part\r\n\r\n",
			body:  "--part\r\nContent-Type: application/octet-stream\r\n\r\n\xff\r\n--part--\r\n",
		},
		"missing header separator": {
			block: "Subject: café\r\n",
			body:  "\r\nbody\r\n",
		},
		"body already in block": {
			block: "Subject: café\r\n\r\nfirst ",
			body:  "second\r\n",
		},
		"empty message": {},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			for _, sessionUTF8 := range []bool{false, true} {
				want := len(wireform.Convert(test.block+test.body, sessionUTF8))
				if got := wireform.Size(test.block+test.body, sessionUTF8); got != want {
					t.Errorf("Size(utf8=%t) = %d, want %d", sessionUTF8, got, want)
				}
				if got := wireform.JoinedSize(test.block, test.body, sessionUTF8); got != want {
					t.Errorf("JoinedSize(utf8=%t) = %d, want %d", sessionUTF8, got, want)
				}
			}
		})
	}
}

func TestEncodedWordsPreserveOctets(t *testing.T) {
	cases := map[string]struct {
		text string
		want string
	}{
		"empty":          {text: "", want: "=?UTF-8?Q??="},
		"ASCII":          {text: "hello world", want: "=?UTF-8?Q?hello_world?="},
		"UTF-8":          {text: "café", want: "=?UTF-8?Q?caf=C3=A9?="},
		"unknown octets": {text: "\xff\x00", want: "=?UNKNOWN-8BIT?Q?=FF=00?="},
		"long UTF-8":     {text: strings.Repeat("日本語 café ", 30)},
		"long unknown":   {text: strings.Repeat("\xff\xfe ", 80)},
	}
	decoder := mime.WordDecoder{
		CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
			if charset != "unknown-8bit" {
				t.Errorf("unexpected charset %q", charset)
			}
			return input, nil
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			encoded := wireform.EncodeWords(test.text)
			if test.want != "" && encoded != test.want {
				t.Errorf("EncodeWords = %q, want %q", encoded, test.want)
			}
			var output strings.Builder
			wireform.WriteEncodedWords(&output, test.text)
			if output.String() != encoded {
				t.Errorf("WriteEncodedWords = %q, want %q", output.String(), encoded)
			}
			var decoded strings.Builder
			for _, word := range strings.Split(encoded, "\r\n ") {
				if len(word) > 75 {
					t.Errorf("encoded word has %d octets, want at most 75", len(word))
				}
				text, err := decoder.Decode(word)
				if err != nil {
					t.Fatalf("decode %q: %v", word, err)
				}
				if utf8.ValidString(test.text) && !utf8.ValidString(text) {
					t.Errorf("encoded word splits a UTF-8 character: %q", word)
				}
				decoded.WriteString(text)
			}
			if decoded.String() != test.text {
				t.Errorf("decoded octets = %q, want %q", decoded.String(), test.text)
			}
			if strings.HasPrefix(name, "long ") && !strings.Contains(encoded, "\r\n ") {
				t.Error("long encoded text has no fold")
			}
		})
	}
}

func TestParameterFieldPreservesReadableParameters(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{name: "Content-Type", value: "text/plain; charset=utf-8", want: "Content-Type: text/plain;\r\n charset=utf-8", ok: true},
		{name: "Content-Disposition", value: `attachment; filename="café.txt"`, want: "Content-Disposition: attachment;\r\n filename*=UTF-8''caf%C3%A9.txt", ok: true},
		{name: "Content-Type", value: "text/plain (plain) (café)", want: "Content-Type: text/plain (plain) (=?UTF-8?Q?caf=C3=A9?=)", ok: true},
		{name: "Content-Disposition", value: "attachment; filename*=iso-8859-1''caf\xe9.txt", want: "Content-Disposition: attachment;\r\n filename*=iso-8859-1''caf%E9.txt", ok: true},
		{name: "Content-Type", value: "invalid/type/extra"},
		{name: "Content-Type", value: "têxt/plain"},
	}
	for _, test := range cases {
		t.Run(test.value, func(t *testing.T) {
			got, ok := wireform.ParameterField(test.name, test.value)
			if got != test.want || ok != test.ok {
				t.Errorf("ParameterField = (%q, %t), want (%q, %t)", got, ok, test.want, test.ok)
			}
		})
	}
}
