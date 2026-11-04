package handlers

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// An import's request and the message in it go to files, and are read from there (#1437). The
// binder keeps every rule and every word of every refusal: it is given the request with
// raw_message replaced by a small value that it judges the same way (importBodyScan), while the
// message itself decodes to a file of its own.
//
// Some forms of request are still held whole, within the route's cap, because only the binder
// can answer them and it reads from memory: raw_message sent more than once, or as an array of
// numbers. Neither is a form any client of this API sends.

// newImportSpool makes a temporary file, and a function that removes it, safe to call twice.
func newImportSpool() (*os.File, func(), error) {
	file, err := os.CreateTemp("", "restmail-import-*")
	if err != nil {
		return nil, nil, err
	}
	return file, sync.OnceFunc(func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}), nil
}

// importSpoolFailure is the refusal a bound body gets for what failed copying it. The server's
// own disk failing is not a refusal, and stays the error it was.
func importSpoolFailure(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return endpoint.Refuse(http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
	}
	var disk *fs.PathError
	if errors.As(err, &disk) {
		return err
	}
	return endpoint.Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
}

// importSpooledMessage is the route's handler. The body goes to a file before any of it is read,
// so the size caps refuse first, as they do for a bound body. Every file is gone when it answers,
// however it answers, a panic included.
func (h *MessageImportHandler) importSpooledMessage(r *http.Request, _ struct{}) (apitypes.ImportMessageResult, error) {
	var result apitypes.ImportMessageResult
	request, removeRequest, err := newImportSpool()
	if err != nil {
		return result, err
	}
	defer removeRequest()
	if r.Body != nil {
		if _, err := io.Copy(request, r.Body); err != nil {
			return result, importSpoolFailure(err)
		}
		if _, err := request.Seek(0, io.SeekStart); err != nil {
			return result, err
		}
	}

	source, err := newImportSource(h.maxBytes)
	if err != nil {
		return result, err
	}
	defer source.remove()

	scanned, err := scanImportBody(request, source)
	if err != nil {
		return result, err
	}
	standIn := scanned.standIn
	if scanned.bindWhole {
		if _, err := request.Seek(0, io.SeekStart); err != nil {
			return result, err
		}
		if standIn, err = io.ReadAll(request); err != nil {
			return result, err
		}
	}
	// What's left to read is the message, so the request needn't take up disk beside it.
	removeRequest()

	var in importMessageRequest
	if err := endpoint.BindRequest(r, &in, standIn); err != nil {
		return result, err
	}
	if !scanned.streamed {
		if err := source.replace(in.Body.RawMessage); err != nil {
			return result, err
		}
	}
	in.Body.RawMessage = nil
	return h.storeImportedMessage(r, in.ID, in.Body, source)
}

// importSource is a decoded message in a file. Past the limit it counts and keeps nothing, for
// the refusal that comes only after the rest of the request has been judged.
type importSource struct {
	file     *os.File
	remove   func()
	limit    int64
	size     int64
	hash     hash.Hash
	checksum [sha256.Size]byte
}

func newImportSource(limit int64) (*importSource, error) {
	file, remove, err := newImportSpool()
	if err != nil {
		return nil, err
	}
	return &importSource{file: file, remove: remove, limit: limit, hash: sha256.New()}, nil
}

func (s *importSource) write(data []byte) error {
	s.size += int64(len(data))
	if s.tooLarge() {
		return nil
	}
	s.hash.Write(data)
	if _, err := s.file.Write(data); err != nil {
		return fmt.Errorf("spool an imported message: %w", err)
	}
	return nil
}

func (s *importSource) tooLarge() bool { return s.size > s.limit }

// finish records the checksum of what was written.
func (s *importSource) finish() { copy(s.checksum[:], s.hash.Sum(nil)) }

// replace makes the file hold data, for a message the binder decoded.
func (s *importSource) replace(data []byte) error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := s.file.Truncate(0); err != nil {
		return err
	}
	s.size = 0
	s.hash.Reset()
	if err := s.write(data); err != nil {
		return err
	}
	s.finish()
	return nil
}

// reader reads the message from its start, and stops being good when reader is called again.
func (s *importSource) reader() (io.Reader, error) {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return s.file, nil
}

// columns reads the whole message for the row, which holds it as text, or as octets when text
// can't: a message that isn't UTF-8, or has a NUL in it. The limit bounds it. Keeping a source
// out of the row is what would remove this read.
func (s *importSource) columns() (string, []byte, error) {
	reader, err := s.reader()
	if err != nil {
		return "", nil, err
	}
	var whole strings.Builder
	whole.Grow(int(s.size))
	if _, err := io.Copy(&whole, io.LimitReader(reader, s.limit)); err != nil {
		return "", nil, fmt.Errorf("read a spooled import: %w", err)
	}
	text, octets := models.SourceColumns(whole.String())
	return text, octets, nil
}

// importBodyScan is what reading a spooled request found.
type importBodyScan struct {
	// standIn is a body the binder answers exactly as it would the whole request. It keeps
	// every field but raw_message as it came, and the value of a field the type doesn't
	// declare, which is refused whatever it holds, is left out.
	standIn []byte
	// streamed says raw_message was decoded to the source. Otherwise the binder will decode it.
	streamed bool
	// bindWhole says standIn can't speak for the request, so the binder needs all of it.
	bindWhole bool
}

const (
	importRawField = "raw_message"
	// What the binder needs to know of a raw_message: empty, valid, or not valid.
	importRawEmpty   = `""`
	importRawValid   = `"QQ=="`
	importRawInvalid = `"!"`
	// A valid body whose top level isn't an object reads the same to the binder as any other.
	importNotAnObject = `[]`
	importNull        = `null`
	// A body that isn't JSON, and one that is only whitespace.
	importNotJSON = `{`
	importBlank   = ` `
)

// importDeclaredFields are the fields of ImportMessageBody the binder reads, other than the
// message. They are read from the type, so they can't come apart from it.
var importDeclaredFields = func() map[string]bool {
	fields := map[string]bool{}
	t := reflect.TypeOf(apitypes.ImportMessageBody{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != importRawField {
			fields[name] = true
		}
	}
	return fields
}()

// scanImportBody reads a spooled request once, keeping nothing of raw_message. It says what
// json.Valid says about the whole request, because only a body that passes reaches any other
// rule. An error here is the server's own, never the request's.
func scanImportBody(file io.Reader, source *importSource) (importBodyScan, error) {
	s := &jsonScanner{src: bufio.NewReaderSize(file, 64<<10)}
	scanned, err := s.importBody(source)
	if errors.Is(err, errNotJSON) {
		return importBodyScan{standIn: []byte(importNotJSON)}, nil
	}
	source.finish()
	return scanned, err
}

func (s *jsonScanner) importBody(source *importSource) (importBodyScan, error) {
	first, err := s.skipSpace()
	if errors.Is(err, errNotJSON) {
		if s.read == 0 {
			return importBodyScan{}, nil
		}
		return importBodyScan{standIn: []byte(importBlank)}, nil
	}
	if err != nil {
		return importBodyScan{}, err
	}
	var scanned importBodyScan
	if first == '{' {
		s.depth = 1
		scanned, err = s.importFields(source)
		if err != nil || scanned.bindWhole {
			return scanned, err
		}
	} else {
		if err := s.value(first); err != nil {
			return importBodyScan{}, err
		}
		scanned.standIn = []byte(importNotAnObject)
		if first == 'n' {
			scanned.standIn = []byte(importNull)
		}
	}
	if _, err := s.skipSpace(); !errors.Is(err, errNotJSON) {
		if err != nil {
			return importBodyScan{}, err
		}
		return importBodyScan{}, errNotJSON
	}
	return scanned, nil
}

// importFields reads an object whose opening brace has been read, through its close.
func (s *jsonScanner) importFields(source *importSource) (importBodyScan, error) {
	var scanned importBodyScan
	var standIn bytes.Buffer
	standIn.WriteByte('{')
	seenRaw := false
	char, err := s.skipSpace()
	if err != nil {
		return scanned, err
	}
	if char == '}' {
		standIn.WriteByte('}')
		scanned.standIn = standIn.Bytes()
		return scanned, nil
	}
	for {
		if char != '"' {
			return scanned, errNotJSON
		}
		start := standIn.Len()
		standIn.WriteByte('"')
		s.keep = &standIn
		err := s.stringRest(nil)
		s.keep = nil
		if err != nil {
			return scanned, err
		}
		var name string
		if err := json.Unmarshal(standIn.Bytes()[start:], &name); err != nil {
			return scanned, errNotJSON
		}
		if char, err = s.skipSpace(); err != nil {
			return scanned, err
		}
		if char != ':' {
			return scanned, errNotJSON
		}
		standIn.WriteByte(':')
		if char, err = s.skipSpace(); err != nil {
			return scanned, err
		}
		switch {
		case name == importRawField && seenRaw:
			// Sent twice, the last one wins, and every earlier one can still be refused, in
			// words that name a place in it. Only the binder on the whole request can say.
			return importBodyScan{bindWhole: true}, nil
		case name == importRawField && char == '"':
			seenRaw, scanned.streamed = true, true
			decoder := newBase64Stream(base64Chunk, source.write)
			if err := s.stringRest(decoder); err != nil {
				return scanned, err
			}
			if err := decoder.finish(); err != nil {
				return scanned, err
			}
			switch {
			case decoder.invalid:
				standIn.WriteString(importRawInvalid)
			case source.size == 0:
				standIn.WriteString(importRawEmpty)
			default:
				standIn.WriteString(importRawValid)
			}
		case name == importRawField || importDeclaredFields[name]:
			seenRaw = seenRaw || name == importRawField
			standIn.WriteByte(char)
			s.keep = &standIn
			err := s.value(char)
			s.keep = nil
			if err != nil {
				return scanned, err
			}
		default:
			if err := s.value(char); err != nil {
				return scanned, err
			}
			standIn.WriteString(importNull)
		}
		if char, err = s.skipSpace(); err != nil {
			return scanned, err
		}
		switch char {
		case '}':
			standIn.WriteByte('}')
			scanned.standIn = standIn.Bytes()
			return scanned, nil
		case ',':
			standIn.WriteByte(',')
			if char, err = s.skipSpace(); err != nil {
				return scanned, err
			}
		default:
			return scanned, errNotJSON
		}
	}
}

// errNotJSON says the input is not one JSON value, or ended before the value did.
var errNotJSON = errors.New("not JSON")

// jsonMaxDepth is the nesting encoding/json's scanner refuses past.
const jsonMaxDepth = 10000

// jsonScanner checks JSON from a reader by the rules of json.Valid, keeping none of it unless
// asked to. encoding/json can't do this: its decoder holds a whole string to read it, and this
// route's string is the message.
type jsonScanner struct {
	src   *bufio.Reader
	keep  *bytes.Buffer // receives each byte read, when set
	depth int
	read  int64
}

// next reads a byte. The input ending is errNotJSON, which is the question a caller is asking.
func (s *jsonScanner) next() (byte, error) {
	char, err := s.src.ReadByte()
	if errors.Is(err, io.EOF) {
		return 0, errNotJSON
	}
	if err != nil {
		return 0, err
	}
	s.read++
	if s.keep != nil {
		s.keep.WriteByte(char)
	}
	return char, nil
}

func (s *jsonScanner) skipSpace() (byte, error) {
	for {
		char, err := s.next()
		if err != nil {
			return 0, err
		}
		switch char {
		case ' ', '\t', '\r', '\n':
		default:
			return char, nil
		}
	}
}

// value reads a value whose first byte has been read.
func (s *jsonScanner) value(first byte) error {
	switch {
	case first == '{':
		return s.container('}', true)
	case first == '[':
		return s.container(']', false)
	case first == '"':
		return s.stringRest(nil)
	case first == 't':
		return s.literal("rue")
	case first == 'f':
		return s.literal("alse")
	case first == 'n':
		return s.literal("ull")
	case first == '-' || (first >= '0' && first <= '9'):
		return s.number(first)
	}
	return errNotJSON
}

func (s *jsonScanner) literal(rest string) error {
	for i := range len(rest) {
		char, err := s.next()
		if err != nil {
			return err
		}
		if char != rest[i] {
			return errNotJSON
		}
	}
	return nil
}

// container reads an object or an array whose opening byte has been read, through its close.
func (s *jsonScanner) container(closing byte, object bool) error {
	s.depth++
	defer func() { s.depth-- }()
	if s.depth > jsonMaxDepth {
		return errNotJSON
	}
	char, err := s.skipSpace()
	if err != nil {
		return err
	}
	if char == closing {
		return nil
	}
	for {
		if object {
			if char != '"' {
				return errNotJSON
			}
			if err := s.stringRest(nil); err != nil {
				return err
			}
			if char, err = s.skipSpace(); err != nil {
				return err
			}
			if char != ':' {
				return errNotJSON
			}
			if char, err = s.skipSpace(); err != nil {
				return err
			}
		}
		if err := s.value(char); err != nil {
			return err
		}
		if char, err = s.skipSpace(); err != nil {
			return err
		}
		switch char {
		case closing:
			return nil
		case ',':
			if char, err = s.skipSpace(); err != nil {
				return err
			}
		default:
			return errNotJSON
		}
	}
}

// peekIs reads the next byte if it matches, and otherwise leaves it unread.
func (s *jsonScanner) peekIs(match func(byte) bool) (bool, error) {
	ahead, err := s.src.Peek(1)
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !match(ahead[0]) {
		return false, nil
	}
	_, err = s.next()
	return true, err
}

func isJSONDigit(char byte) bool { return char >= '0' && char <= '9' }

// digits reads one or more digits, which the input must have, and what follows them stays unread.
func (s *jsonScanner) digits() error {
	found, err := s.peekIs(isJSONDigit)
	if err != nil {
		return err
	}
	if !found {
		return errNotJSON
	}
	return s.moreDigits()
}

func (s *jsonScanner) moreDigits() error {
	for {
		found, err := s.peekIs(isJSONDigit)
		if err != nil || !found {
			return err
		}
	}
}

func (s *jsonScanner) number(first byte) error {
	if first == '-' {
		var err error
		if first, err = s.next(); err != nil {
			return err
		}
		if !isJSONDigit(first) {
			return errNotJSON
		}
	}
	if first != '0' {
		if err := s.moreDigits(); err != nil {
			return err
		}
	}
	point, err := s.peekIs(func(char byte) bool { return char == '.' })
	if err != nil {
		return err
	}
	if point {
		if err := s.digits(); err != nil {
			return err
		}
	}
	exponent, err := s.peekIs(func(char byte) bool { return char == 'e' || char == 'E' })
	if err != nil {
		return err
	}
	if !exponent {
		return nil
	}
	if _, err := s.peekIs(func(char byte) bool { return char == '+' || char == '-' }); err != nil {
		return err
	}
	return s.digits()
}

// stringRest reads a string whose opening quote has been read, through its closing one. When
// content is set it is given the string's bytes as encoding/json would unquote them, as far as
// base64 can tell: anything past ASCII is one byte that base64 refuses, as all of it is.
func (s *jsonScanner) stringRest(content *base64Stream) error {
	emit := func(char byte) error {
		if content == nil {
			return nil
		}
		return content.add(char)
	}
	for {
		char, err := s.next()
		if err != nil {
			return err
		}
		switch {
		case char == '"':
			return nil
		case char < 0x20:
			return errNotJSON
		case char != '\\':
			if err := emit(char); err != nil {
				return err
			}
			continue
		}
		if char, err = s.next(); err != nil {
			return err
		}
		switch char {
		case '"', '\\', '/':
		case 'b':
			char = '\b'
		case 'f':
			char = '\f'
		case 'n':
			char = '\n'
		case 'r':
			char = '\r'
		case 't':
			char = '\t'
		case 'u':
			if char, err = s.unicodeEscape(); err != nil {
				return err
			}
		default:
			return errNotJSON
		}
		if err := emit(char); err != nil {
			return err
		}
	}
}

// unicodeEscape reads the four hex digits of \uXXXX, and answers the byte it is if it is ASCII,
// and otherwise a byte base64 refuses, whichever character it turns out to be part of.
func (s *jsonScanner) unicodeEscape() (byte, error) {
	code := 0
	for range 4 {
		char, err := s.next()
		if err != nil {
			return 0, err
		}
		switch {
		case char >= '0' && char <= '9':
			code = code<<4 | int(char-'0')
		case char >= 'a' && char <= 'f':
			code = code<<4 | int(char-'a'+10)
		case char >= 'A' && char <= 'F':
			code = code<<4 | int(char-'A'+10)
		default:
			return 0, errNotJSON
		}
	}
	if code >= 0x80 {
		return 0xFF, nil
	}
	return byte(code), nil
}

// base64Chunk is how many symbols are decoded at a time, a whole number of quanta.
const base64Chunk = 48 << 10

// base64Stream decodes as base64.StdEncoding.DecodeString does, in pieces, passing on what it
// decodes. It says only whether the whole was valid, not where it wasn't, because nothing asks.
// base64.NewDecoder won't do: where its reads happen to fall decides what it makes of input that
// carries on after padding.
type base64Stream struct {
	symbols []byte
	decoded []byte
	sink    func([]byte) error
	padded  bool
	invalid bool
}

// newBase64Stream decodes chunk symbols at a time, which must be a multiple of 4.
func newBase64Stream(chunk int, sink func([]byte) error) *base64Stream {
	return &base64Stream{
		symbols: make([]byte, 0, chunk),
		decoded: make([]byte, base64.StdEncoding.DecodedLen(chunk)),
		sink:    sink,
	}
}

func (b *base64Stream) add(char byte) error {
	if b.invalid || char == '\n' || char == '\r' {
		return nil
	}
	if b.padded {
		b.invalid = true
		return nil
	}
	b.symbols = append(b.symbols, char)
	if len(b.symbols) == cap(b.symbols) {
		return b.flush()
	}
	return nil
}

func (b *base64Stream) flush() error {
	n, err := base64.StdEncoding.Decode(b.decoded, b.symbols)
	if err != nil {
		b.invalid = true
		return nil
	}
	b.padded = b.symbols[len(b.symbols)-1] == '='
	b.symbols = b.symbols[:0]
	return b.sink(b.decoded[:n])
}

// finish decodes what's left, once the string has ended.
func (b *base64Stream) finish() error {
	if b.invalid || len(b.symbols) == 0 {
		return nil
	}
	return b.flush()
}
