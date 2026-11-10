package mailaddr

import (
	"errors"
	"io"
	"math/rand/v2"
	"mime"
	"strings"
	"testing"

	gomessage "github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // sets gomessage.CharsetReader
)

type failedRead struct{}

func (failedRead) Read([]byte) (int, error) { return 0, errors.New("failed read") }

// A word reads here as mime.WordDecoder reads it, with whatever reader the caller has, for a name that
// is wanted and for one that only has to exist. The list of what can go wrong is mime's, and the random
// words are for what that list doesn't think of.
func TestWordsReadAsWordDecoderDoes(t *testing.T) {
	words := []string{"=?", "=??=", "=?a?=", "=?utf-8?q?a", "=?utf-8?q?a?", "=?utf-8?q?a?=", "=?UTF-8?Q?a?=", "=?utf-8?q??=", "=?utf-8?b??=", "=??q?a?=", "=?utf-8??a?=", "=?utf-8?qq?a?=", "=?utf-8?x?a?=",
		"=?utf-8?q?a?b?=", "=?utf-8?q?a=?=", "=?utf-8?q?a=4?=", "=?utf-8?q?a=4g?=", "=?utf-8?q?a=g4?=", "=?utf-8?q?=4a?=", "=?utf-8?q?=4A?=", "=?utf-8?q?a_b?=", "=?utf-8?q?a\tb?=", "=?utf-8?q?a\x7fb?=", "=?utf-8?q?\xc3\xa9?=",
		"=?utf-8?q?=FF?=", "=?utf-8?b?YQ==?=", "=?utf-8?B?YQ==?=", "=?utf-8?b?YQ?=", "=?utf-8?b?Y Q==?=", "=?utf-8?b?YQ=\r\n=?=", "=?utf-8?b?!!!!?=",
		"=?iso-8859-1?q?=E9?=", "=?ISO-8859-1?Q?=E9?=", "=?iso-8859-1?b?6Q==?=", "=?us-ascii?q?a=E9?=", "=?US-ASCII?b?6Q==?=",
		"=?windows-1252?q?=80?=", "=?WINDOWS-1252?Q?=80?=", "=?windows-1252?b?gA==?=", "=?windows-1252?q??=", "=?koi8-r?q?=C1?=", "=?shift_jis?b?gqA=?=", "=?shift_jis?q?=82?=", "=?iso-2022-jp?b?GyRCJCIbKEI=?=", "=?utf-16be?b?AGE=?=", "=?utf-16be?b?AA==?=",
		"=?utf-7?q?a?=", "=?x-none?q?a?=", "=?x-none?q?a=?=", "=?x-none?b?!?=", "=?hz-gb-2312?q?~{?="}
	random := rand.New(rand.NewPCG(1915, 2))
	const parts = "aA0_= =4A=FF\t\r\n\x7f\xc3YQ==utf-8windows-1252"
	for range 4000 {
		var text strings.Builder
		for range random.IntN(8) {
			at := random.IntN(len(parts))
			text.WriteString(parts[at : at+min(random.IntN(4), len(parts)-at)])
		}
		charset := []string{"utf-8", "US-ASCII", "iso-8859-1", "windows-1252", "shift_jis", "x-none", ""}[random.IntN(7)]
		encoding := []string{"q", "Q", "b", "B", "x", ""}[random.IntN(6)]
		words = append(words, "=?"+charset+"?"+encoding+"?"+text.String()+"?=")
	}
	for name, reader := range map[string]func(string, io.Reader) (io.Reader, error){
		"go-message": func(charset string, input io.Reader) (io.Reader, error) {
			return gomessage.CharsetReader(charset, input)
		},
		"unconverted":    func(_ string, input io.Reader) (io.Reader, error) { return input, nil },
		"refusing":       func(string, io.Reader) (io.Reader, error) { return nil, errors.New("refused") },
		"failing a read": func(string, io.Reader) (io.Reader, error) { return failedRead{}, nil },
		"none":           nil,
	} {
		t.Run(name, func(t *testing.T) {
			refused := false
			strict := &mime.WordDecoder{}
			if reader != nil {
				strict.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
					r, err := reader(charset, input)
					refused = err != nil
					return r, err
				}
			}
			named := parser{visit: func(string, string) {}}
			named.setDecoder(&mime.WordDecoder{CharsetReader: reader})
			counted := parser{count: new(int)}
			counted.setDecoder(&mime.WordDecoder{CharsetReader: reader})
			for _, word := range words {
				refused = false
				decoded, err := strict.Decode(word)
				if reader == nil && err != nil && strings.Contains(err.Error(), "unhandled charset") {
					refused = true
				}
				// What net/mail's decodeRFC2047Word makes of Decode's answer.
				want, wantEncoded, wantFailed := word, false, false
				switch {
				case err == nil:
					want, wantEncoded = decoded, true
				case refused:
					wantEncoded, wantFailed = true, true
				}
				got, encoded, gotErr := named.decodeWord(word)
				if got != want || encoded != wantEncoded || (gotErr != nil) != wantFailed {
					t.Errorf("%q reads as %q, %v, %v, mime reads %q, %v, %v (%v)", word, got, encoded, gotErr, want, wantEncoded, wantFailed, err)
				}
				// Without a name wanted, it's only known whether there's a word and if it's empty.
				if err == nil && decoded != "" {
					want = word
				}
				got, encoded, gotErr = counted.decodeWord(word)
				if got != want || encoded != wantEncoded || (gotErr != nil) != wantFailed {
					t.Errorf("%q counts as %q, %v, %v, mime reads %q, %v, %v (%v)", word, got, encoded, gotErr, want, wantEncoded, wantFailed, err)
				}
			}
		})
	}
}
