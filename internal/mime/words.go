package mime

import (
	"bytes"
	"errors"
	"io"
	stdmime "mime"
	"strings"
	"sync"
	"unicode/utf8"

	gomessage "github.com/emersion/go-message"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"github.com/rest-mail/rest-mail-server/internal/mailaddr"
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

// errTransformLeftOver is a transform stopping short of the source with nothing to say why, as
// transform.Reader fails one.
var errTransformLeftOver = errors.New("mime: transform left part of its source unread")

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
			return written, errTransformLeftOver
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

// decodeWords is a header value with its RFC 2047 encoded-words decoded, or the value when it can't
// be, as go-message reads one.
func decodeWords(value string) string {
	if !strings.Contains(value, "=?") {
		return value
	}
	words, _ := headerWordBuffers.Get().(*headerWords)
	decoded, err := words.decodeHeader(value)
	// The pool would keep a long word's room for every word after it.
	if words.text.Held() > maxKeptWord {
		words.text = mailaddr.WordText{}
	}
	words.source.Reset(nil)
	headerWordBuffers.Put(words)
	if err != nil {
		return value
	}
	return decoded
}

var headerWordBuffers = sync.Pool{New: func() any { return new(headerWords) }}

// headerWords reads the words of a field with what it read the last one with.
type headerWords struct {
	text   mailaddr.WordText
	source bytes.Reader
}

// leastWordSpelling is how much longer an encoded-word is than what it spells, unless its charset
// grows it, as =?x?q??= spells nothing.
const leastWordSpelling = len("=?x?q??=")

// decodeHeader is mime.WordDecoder.DecodeHeader with wordCharsetReader, which builds a reader, a
// buffer and a string for each word: about 100 octets of a 26-octet word, and more growing what they
// write to (#1915). TestDecodeWordsReadsAsWordDecoderDoes holds the two together.
//
// Copyright 2015 The Go Authors. All rights reserved. Use of this source code is governed by a
// BSD-style license.
func (w *headerWords) decodeHeader(header string) (string, error) {
	var buf strings.Builder
	// What it writes is nearly always less than this, so it only grows for a charset that grows it.
	buf.Grow(max(len(header)-leastWordSpelling*strings.Count(header, "=?"), 0))
	i := strings.Index(header, "=?")
	buf.WriteString(header[:i])
	header = header[i:]

	betweenWords := false
	for {
		start := strings.Index(header, "=?")
		if start == -1 {
			break
		}
		cur := start + len("=?")

		i := strings.Index(header[cur:], "?")
		if i == -1 {
			break
		}
		charset := header[cur : cur+i]
		cur += i + len("?")

		if len(header) < cur+len("Q??=") {
			break
		}
		encoding := header[cur]
		cur++

		if header[cur] != '?' {
			break
		}
		cur++

		j := strings.Index(header[cur:], "?=")
		if j == -1 {
			break
		}
		text := header[cur : cur+j]
		end := cur + j + len("?=")

		content, err := w.text.Read(encoding, text)
		if err != nil {
			betweenWords = false
			buf.WriteString(header[:end])
			header = header[end:]
			continue
		}

		// White space between two encoded-words isn't part of what they say.
		if start > 0 && (!betweenWords || hasNonWhitespace(header[:start])) {
			buf.WriteString(header[:start])
		}

		if err := w.convert(&buf, charset, content); err != nil {
			return "", err
		}

		header = header[end:]
		betweenWords = true
	}

	if len(header) > 0 {
		buf.WriteString(header)
	}

	return buf.String(), nil
}

// convert is mime.WordDecoder.convert with wordCharsetReader, and a source used again.
func (w *headerWords) convert(buf *strings.Builder, charset string, content []byte) error {
	switch {
	case strings.EqualFold("utf-8", charset):
		buf.Write(content)
	case strings.EqualFold("iso-8859-1", charset):
		for _, c := range content {
			buf.WriteRune(rune(c))
		}
	case strings.EqualFold("us-ascii", charset):
		for _, c := range content {
			if c >= utf8.RuneSelf {
				buf.WriteRune(utf8.RuneError)
			} else {
				buf.WriteByte(c)
			}
		}
	default:
		w.source.Reset(content)
		r, err := wordCharsetReader(strings.ToLower(charset), &w.source)
		if err != nil {
			return err
		}
		if _, err = io.Copy(buf, r); err != nil {
			return err
		}
	}
	return nil
}

// hasNonWhitespace is mime's: encoded-words are only separated by linear white space, which doesn't
// include a vertical tab.
func hasNonWhitespace(s string) bool {
	for _, b := range s {
		switch b {
		case ' ', '\t', '\n', '\r':
		default:
			return true
		}
	}
	return false
}

// partCharsets is what one message's parts convert their charsets with, as go-message's reader does,
// and the parts of a message are read one after another, so one reader and one decoder for each charset
// do for all of them. go-message's reader costs 8 KiB for a part of however few octets, and a message
// can have as many of those as its part limit (#1915). A parse's fieldReader holds it.
type partCharsets struct {
	decoders map[string]*encoding.Decoder
	reader   *partCharsetReader
}

// open answers what reads input in charset, or nil when that isn't a charset wordEncoding finds. It's
// good until the next open.
func (c *partCharsets) open(charset string, input io.Reader) io.Reader {
	dec, ok := c.decoders[charset]
	if !ok {
		enc := wordEncoding(charset)
		if enc == nil {
			return nil
		}
		// The names a message can spell have no end, so what's kept has to.
		if c.decoders == nil || len(c.decoders) >= maxWordDecoders {
			c.decoders = make(map[string]*encoding.Decoder, maxWordDecoders)
		}
		dec = enc.NewDecoder()
		c.decoders[strings.Clone(charset)] = dec
	}
	if c.reader == nil {
		c.reader = new(partCharsetReader)
	}
	c.reader.start(dec, input)
	return c.reader
}

// partCharsetReader is transform.Reader, whose buffers a new reader doesn't have to make again.
// TestPartCharsetReaderReadsAsGoMessageDoes holds the two together.
//
// Copyright 2013 The Go Authors. All rights reserved. Use of this source code is governed by a
// BSD-style license.
type partCharsetReader struct {
	source io.Reader
	dec    *encoding.Decoder
	err    error
	// dst[dst0:dst1] is what's converted and not yet read, and src[src0:src1] what's read and not yet
	// converted. They're the sizes go-message's reader has, so a transform that wants more refuses the
	// same text.
	dst, src          [4096]byte
	dst0, dst1        int
	src0, src1        int
	transformComplete bool
}

func (r *partCharsetReader) start(dec *encoding.Decoder, source io.Reader) {
	dec.Reset()
	r.source, r.dec, r.err = source, dec, nil
	r.dst0, r.dst1, r.src0, r.src1 = 0, 0, 0, 0
	r.transformComplete = false
}

func (r *partCharsetReader) Read(p []byte) (int, error) {
	n, err := 0, error(nil)
	for {
		if r.dst0 != r.dst1 {
			n = copy(p, r.dst[r.dst0:r.dst1])
			r.dst0 += n
			if r.dst0 == r.dst1 && r.transformComplete {
				return n, r.err
			}
			return n, nil
		} else if r.transformComplete {
			return 0, r.err
		}

		// Transform what's here, even if reading failed: there may be octets to read before that
		// failure, and this is what flushes the transform.
		if r.src0 != r.src1 || r.err != nil {
			r.dst0 = 0
			r.dst1, n, err = r.dec.Transform(r.dst[:], r.src[r.src0:r.src1], r.err == io.EOF)
			r.src0 += n

			switch {
			case err == nil:
				if r.src0 != r.src1 {
					r.err = errTransformLeftOver
				}
				r.transformComplete = r.err != nil
				continue
			case err == transform.ErrShortDst && (r.dst1 != 0 || n != 0):
				continue
			case err == transform.ErrShortSrc && r.src1-r.src0 != len(r.src) && r.err == nil:
				// Read more of it, below, and try again.
			default:
				r.transformComplete = true
				// A failure to read comes before the transform's.
				if r.err == nil || r.err == io.EOF {
					r.err = err
				}
				continue
			}
		}

		if r.src0 != 0 {
			r.src0, r.src1 = 0, copy(r.src[:], r.src[r.src0:r.src1])
		}
		n, r.err = r.source.Read(r.src[r.src1:])
		r.src1 += n
	}
}
