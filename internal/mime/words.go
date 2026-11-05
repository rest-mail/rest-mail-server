package mime

import (
	"bytes"
	"errors"
	"io"
	stdmime "mime"
	"strings"
	"sync"

	gomessage "github.com/emersion/go-message"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// wordDecoder reads a field's RFC 2047 encoded-words as go-message does, at no cost per word beyond the
// word.
var wordDecoder = &stdmime.WordDecoder{CharsetReader: wordCharsetReader}

const (
	// maxWordDecoders is how many charsets one wordReader keeps a decoder for. The names a field can
	// spell have no end, so what's kept has to.
	maxWordDecoders = 16
	// maxKeptWord is the longest word whose source a wordReader keeps room for.
	maxKeptWord = 1 << 16
)

// errWordLeftOver is a transform stopping short of the source with nothing to say why, as
// transform.Reader fails one.
var errWordLeftOver = errors.New("mime: transform left part of a word unread")

var wordReaders = sync.Pool{New: func() any {
	return &wordReader{decoders: make(map[string]*encoding.Decoder, maxWordDecoders)}
}}

// wordCharsetReader reads what gomessage.CharsetReader reads. A mime.WordDecoder asks for a reader for
// each word and copies from it, and that reader's buffers and the copy's came to about 40 KiB for a
// word of a few octets, so 1 MiB of them allocated 1.3 GB (#1915). io.Copy takes no buffer from a
// reader that writes itself out, so this one does, with a decoder and a buffer it uses again.
// TestWordCharsetReaderReadsAsGoMessageDoes holds the two together.
func wordCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	r, _ := wordReaders.Get().(*wordReader)
	r.dec = r.decoderFor(charset)
	if r.dec == nil {
		wordReaders.Put(r)
		// What refuses the charset says why.
		return gomessage.CharsetReader(charset, input)
	}
	r.src.Reset()
	if sized, ok := input.(interface{ Len() int }); ok {
		r.src.Grow(sized.Len() + bytes.MinRead)
	}
	if _, err := r.src.ReadFrom(input); err != nil {
		wordReaders.Put(r)
		return nil, err
	}
	return r, nil
}

// wordReader is one encoded-word's text, on its way to UTF-8.
type wordReader struct {
	decoders map[string]*encoding.Decoder
	dec      *encoding.Decoder
	src      bytes.Buffer
	// dst is the size go-message's reader has, so a transform that wants more refuses the same word.
	dst [4096]byte
	// decoded and failed are what Read has left to give.
	decoded *bytes.Buffer
	failed  error
}

func (r *wordReader) decoderFor(charset string) *encoding.Decoder {
	if dec, ok := r.decoders[charset]; ok {
		return dec
	}
	enc := wordEncoding(charset)
	if enc == nil {
		return nil
	}
	if len(r.decoders) >= maxWordDecoders {
		clear(r.decoders)
	}
	dec := enc.NewDecoder()
	r.decoders[strings.Clone(charset)] = dec
	return dec
}

// wordEncoding is the encoding go-message's charset.Reader finds for a charset, or nil when it refuses
// the charset.
func wordEncoding(charset string) encoding.Encoding {
	switch strings.ToLower(charset) {
	case "ansi_x3.110-1983":
		return charmap.ISO8859_1
	case "x-utf_8j":
		return unicode.UTF8
	}
	enc, err := ianaindex.MIME.Encoding(charset)
	if enc == nil {
		enc, err = ianaindex.MIME.Encoding("cs" + charset)
	}
	if enc == nil {
		enc, err = htmlindex.Get(charset)
	}
	if err != nil {
		return nil
	}
	return enc
}

// WriteTo is what io.Copy calls, the word's only use of this reader, which goes back to be used again.
func (r *wordReader) WriteTo(w io.Writer) (int64, error) {
	written, err := r.convert(w)
	if r.src.Cap() > maxKeptWord {
		r.src = bytes.Buffer{}
	}
	r.dec = nil
	wordReaders.Put(r)
	return written, err
}

// convert goes through the source as transform.Reader does, which has it all from the start.
func (r *wordReader) convert(w io.Writer) (written int64, err error) {
	r.dec.Reset()
	src := r.src.Bytes()
	for {
		nDst, nSrc, err := r.dec.Transform(r.dst[:], src, true)
		src = src[nSrc:]
		n, writeErr := w.Write(r.dst[:nDst])
		written += int64(n)
		switch {
		case writeErr != nil:
			return written, writeErr
		case err == nil && len(src) > 0:
			return written, errWordLeftOver
		case err == nil:
			return written, nil
		case err != transform.ErrShortDst || (nDst == 0 && nSrc == 0):
			return written, err
		}
	}
}

// Read is for anything that doesn't copy the word out with io.Copy. It isn't put back to be used again.
func (r *wordReader) Read(p []byte) (int, error) {
	if r.decoded == nil {
		r.decoded = new(bytes.Buffer)
		_, r.failed = r.convert(r.decoded)
	}
	if r.decoded.Len() == 0 {
		if r.failed != nil {
			return 0, r.failed
		}
		return 0, io.EOF
	}
	return r.decoded.Read(p)
}
