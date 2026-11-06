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
	"math"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

// An import's request and the message in it go to files, and are read from there (#1437), as a
// delivery's do (delivery_spool.go). The binder keeps every rule and every word of every refusal:
// it is given the request with raw_message replaced by a small value that it judges the same way
// (importBodyScan), while the message itself decodes to a file of its own.
//
// Some forms of request are still held whole, within the route's cap, because only the binder
// can answer them and it reads from memory: raw_message sent more than once, or as an array of
// numbers. Neither is a form any client of this API sends.

// newImportSpool makes a temporary file, and a function that removes it, safe to call twice.
func newImportSpool() (*os.File, func(), error) {
	file, err := os.CreateTemp("", "restmail-spool-*")
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
	source, err := newImportSource(h.maxBytes)
	if err != nil {
		return result, err
	}
	defer source.remove()

	scanned, err := readSpooledBody(r, &spooledBodySpec{source: source, declared: importDeclaredFields})
	if err != nil {
		return result, err
	}

	var in importMessageRequest
	if err := endpoint.BindRequest(r, &in, scanned.standIn); err != nil {
		return result, err
	}
	if !scanned.streamed {
		if err := source.replace(in.Body.RawMessage); err != nil {
			return result, err
		}
	}
	in.Body.RawMessage = nil
	result, err = h.storeImportedMessage(r, in.ID, in.Body, source, in.ImportSession)
	if err != nil {
		h.auditRefusal(r, in.ID, in.Body.Folder, in.ImportSession, err)
	}
	return result, err
}

// readSpooledBody copies the request's body to a file and reads it once from there. The size caps
// refuse here, first, as they do for a bound body. Once this returns, what's left to read is in
// the spec's files, so the request isn't taking up disk beside them.
func readSpooledBody(r *http.Request, spec *spooledBodySpec) (importBodyScan, error) {
	request, removeRequest, err := newImportSpool()
	if err != nil {
		return importBodyScan{}, err
	}
	defer removeRequest()
	if r.Body != nil {
		if _, err := io.Copy(request, r.Body); err != nil {
			return importBodyScan{}, importSpoolFailure(err)
		}
		if _, err := request.Seek(0, io.SeekStart); err != nil {
			return importBodyScan{}, err
		}
	}
	scanned, err := scanSpooledBody(request, spec)
	if err != nil || !scanned.bindWhole {
		return scanned, err
	}
	if _, err := request.Seek(0, io.SeekStart); err != nil {
		return scanned, err
	}
	scanned.standIn, err = io.ReadAll(request)
	return scanned, err
}

// importSource is a decoded message in a file. Past the limit it counts and keeps nothing, for
// the refusal that comes only after the rest of the request has been judged.
type importSource struct {
	file     *os.File
	remove   func()
	limit    int64
	size     int64
	nul      bool      // a NUL byte was written
	hash     hash.Hash // when set, finish records the checksum of what was written
	checksum [sha256.Size]byte
	// head holds the message's first line until it is whole, to tell an mbox From_
	// separator from a header; headDone is set once it has been judged and written.
	head     []byte
	headDone bool
	// mboxSeparator is set when a leading From_ line was dropped (#1498).
	mboxSeparator bool
	// mbox says a leading From_ line is dropped: set for an imported message only.
	mbox bool
}

// newSpooledText is a source with no limit and no checksum: nothing compares a delivery with what
// is stored, and the route's body cap bounds it. It keeps a leading From_ line, which only an
// import drops.
func newSpooledText() (*importSource, error) {
	source, err := newImportSource(math.MaxInt64)
	if err != nil {
		return nil, err
	}
	source.hash = nil
	source.mbox = false
	return source, nil
}

// maxImportFirstLine is RFC 5322's line limit with its CRLF: a longer first line is
// no From_ separator, and is written as it came.
const maxImportFirstLine = 1000

// mboxSeparatorLine is the mbox "From_" line (RFC 4155): "From ", the envelope
// sender as one token, and the asctime(3) date some writers give a zone or
// year-last offset. A header has a colon before its first space; this has none.
var mboxSeparatorLine = regexp.MustCompile(`^From [^\s:]\S* +(Mon|Tue|Wed|Thu|Fri|Sat|Sun) +` +
	`(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) +\d{1,2} +\d{1,2}:\d{2}(:\d{2})?` +
	`( +([A-Z]{1,5}|[+-]\d{4}))? +\d{4}( +([A-Z]{1,5}|[+-]\d{4}))? *\r?\n$`)

func newImportSource(limit int64) (*importSource, error) {
	file, remove, err := newImportSpool()
	if err != nil {
		return nil, err
	}
	return &importSource{file: file, remove: remove, limit: limit, hash: sha256.New(), mbox: true}, nil
}

// write adds decoded octets. The first line is held until it is whole: an mbox From_
// separator is dropped so the message starts at its first header, and anything
// else is kept as it came.
func (s *importSource) write(data []byte) error {
	if s.headDone || !s.mbox {
		return s.spool(data)
	}
	s.head = append(s.head, data...)
	end := bytes.IndexByte(s.head, '\n')
	if end < 0 && len(s.head) < maxImportFirstLine {
		return nil
	}
	return s.judgeHead(end)
}

// judgeHead writes the held first line, without it when it is a From_ separator, and
// whatever came after it. end is the index of its LF, or -1 when there is none.
func (s *importSource) judgeHead(end int) error {
	head := s.head
	s.head, s.headDone = nil, true
	if s.mbox && end >= 0 && end < maxImportFirstLine && mboxSeparatorLine.Match(head[:end+1]) {
		s.mboxSeparator = true
		head = head[end+1:]
	}
	return s.spool(head)
}

func (s *importSource) spool(data []byte) error {
	s.size += int64(len(data))
	s.nul = s.nul || bytes.IndexByte(data, 0) >= 0
	if s.tooLarge() {
		return nil
	}
	if s.hash != nil {
		s.hash.Write(data)
	}
	if _, err := s.file.Write(data); err != nil {
		return fmt.Errorf("spool an imported message: %w", err)
	}
	return nil
}

func (s *importSource) tooLarge() bool { return s.size > s.limit }

// finish writes a first line still held, for a message with no line end, and
// records the checksum of what was written.
func (s *importSource) finish() error {
	if err := s.flushHead(); err != nil {
		return err
	}
	if s.hash != nil {
		copy(s.checksum[:], s.hash.Sum(nil))
	}
	return nil
}

// flushHead writes a first line still held: the message has ended without a line end.
func (s *importSource) flushHead() error {
	if s.headDone {
		return nil
	}
	return s.judgeHead(bytes.IndexByte(s.head, '\n'))
}

// replace makes the file hold data, for a message the binder decoded.
func (s *importSource) replace(data []byte) error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := s.file.Truncate(0); err != nil {
		return err
	}
	s.size, s.nul = 0, false
	if s.hash != nil {
		s.hash.Reset()
	}
	s.head, s.headDone, s.mboxSeparator = nil, false, false
	if err := s.write(data); err != nil {
		return err
	}
	return s.finish()
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
	text, err := s.text()
	if err != nil {
		return "", nil, err
	}
	if utf8.ValidString(text) && !strings.ContainsRune(text, 0) {
		return text, nil, nil
	}
	return "", []byte(text), nil
}

// text reads the whole file into memory, for the step that can't do without it.
func (s *importSource) text() (string, error) {
	reader, err := s.reader()
	if err != nil {
		return "", err
	}
	var whole strings.Builder
	whole.Grow(int(s.size))
	if _, err := io.Copy(&whole, io.LimitReader(reader, s.limit)); err != nil {
		return "", fmt.Errorf("read a spooled message: %w", err)
	}
	return whole.String(), nil
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
	// What the binder needs to know of a string held in a file.
	importTextValue = `"x"`
	importTextNUL   = `"\u0000"`
	// A valid body whose top level isn't an object reads the same to the binder as any other.
	importNotAnObject = `[]`
	importNull        = `null`
	// A body that isn't JSON, and one that is only whitespace.
	importNotJSON = `{`
	importBlank   = ` `
)

// importDeclaredFields are the fields of ImportMessageBody the binder reads, other than the
// message.
var importDeclaredFields = declaredFieldsOf(apitypes.ImportMessageBody{})

// declaredFieldsOf names the JSON fields of a body type, other than the message. They are read
// from the type, so they can't come apart from it.
func declaredFieldsOf(body any) map[string]bool {
	fields := map[string]bool{}
	t := reflect.TypeOf(body)
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != importRawField {
			fields[name] = true
		}
	}
	return fields
}

// spooledBodySpec says how a route's request is read from its file.
type spooledBodySpec struct {
	// source receives raw_message.
	source *importSource
	// rawIsText says raw_message is a JSON string holding the message itself, not base64.
	rawIsText bool
	// declared are the fields the binder reads as they came.
	declared map[string]bool
	// texts are string fields that can be as large as the message. A string in one goes to a file
	// of its own, here, under its name, and the binder is given a small value in its place.
	texts map[string]*importSource
}

// scanSpooledBody reads a spooled request once, keeping nothing of raw_message. It says what
// json.Valid says about the whole request, because only a body that passes reaches any other
// rule. An error here is the server's own, never the request's.
func scanSpooledBody(file io.Reader, spec *spooledBodySpec) (importBodyScan, error) {
	s := &jsonScanner{src: bufio.NewReaderSize(file, 64<<10)}
	scanned, err := s.importBody(spec)
	if errors.Is(err, errNotJSON) {
		return importBodyScan{standIn: []byte(importNotJSON)}, nil
	}
	if finished := spec.source.finish(); err == nil {
		err = finished
	}
	return scanned, err
}

func (s *jsonScanner) importBody(spec *spooledBodySpec) (importBodyScan, error) {
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
		scanned, err = s.importFields(spec)
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
func (s *jsonScanner) importFields(spec *spooledBodySpec) (importBodyScan, error) {
	source := spec.source
	var scanned importBodyScan
	var standIn bytes.Buffer
	standIn.WriteByte('{')
	seenRaw := false
	seenText := map[string]bool{}
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
		_, isText := spec.texts[name]
		switch {
		case name == importRawField && seenRaw, isText && seenText[name]:
			// Sent twice, the last one wins, and every earlier one can still be refused, in
			// words that name a place in it. Only the binder on the whole request can say.
			return importBodyScan{bindWhole: true}, nil
		case name == importRawField && char == '"' && spec.rawIsText:
			seenRaw, scanned.streamed = true, true
			if err := s.textRest(source.write); err != nil {
				return scanned, err
			}
			standIn.WriteString(importTextStandIn(source))
		case isText && char == '"':
			seenText[name] = true
			field, err := newSpooledText()
			if err != nil {
				return scanned, err
			}
			spec.texts[name] = field
			if err := s.textRest(field.write); err != nil {
				return scanned, err
			}
			standIn.WriteString(importTextStandIn(field))
		case name == importRawField && char == '"':
			seenRaw, scanned.streamed = true, true
			decoder := newBase64Stream(base64Chunk, source.write)
			if err := s.stringRest(decoder); err != nil {
				return scanned, err
			}
			if err := decoder.finish(); err != nil {
				return scanned, err
			}
			if err := source.flushHead(); err != nil {
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
		case name == importRawField || isText || spec.declared[name]:
			seenRaw = seenRaw || name == importRawField
			seenText[name] = isText
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

// importTextStandIn is what the binder is given for a string held in a file: empty or not, with
// a NUL in it or not. Those are all a rule on a text field can ask of it.
func importTextStandIn(text *importSource) string {
	switch {
	case text.nul:
		return importTextNUL
	case text.size == 0:
		return importRawEmpty
	}
	return importTextValue
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

// jsonStringSpecial are the bytes a string can't simply pass over.
const jsonStringSpecial = "\"\\\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f"

// plainRun is what's buffered up to the next special byte, which may be nothing. It stays good
// until the next read, and takeRun passes over it. A message is nearly all one long string, and
// going through that a byte at a time is slow.
func (s *jsonScanner) plainRun() ([]byte, error) {
	if _, err := s.src.Peek(1); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errNotJSON
		}
		return nil, err
	}
	ahead, err := s.src.Peek(s.src.Buffered())
	if err != nil {
		return nil, err
	}
	if end := bytes.IndexAny(ahead, jsonStringSpecial); end >= 0 {
		ahead = ahead[:end]
	}
	return ahead, nil
}

func (s *jsonScanner) takeRun(run []byte) {
	// Discarding what is buffered can't fail.
	_, _ = s.src.Discard(len(run))
	s.read += int64(len(run))
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
		if s.keep == nil {
			run, err := s.plainRun()
			if err != nil {
				return err
			}
			if content != nil {
				if err := content.addRun(run); err != nil {
					return err
				}
			}
			s.takeRun(run)
		}
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
	code, err := s.hex4()
	if err != nil {
		return 0, err
	}
	if code >= 0x80 {
		return 0xFF, nil
	}
	return byte(code), nil
}

// hex4 reads the four hex digits of \uXXXX.
func (s *jsonScanner) hex4() (rune, error) {
	var code rune
	for range 4 {
		char, err := s.next()
		if err != nil {
			return 0, err
		}
		digit := hexDigit(char)
		if digit < 0 {
			return 0, errNotJSON
		}
		code = code<<4 | digit
	}
	return code, nil
}

func hexDigit(char byte) rune {
	switch {
	case char >= '0' && char <= '9':
		return rune(char - '0')
	case char >= 'a' && char <= 'f':
		return rune(char-'a') + 10
	case char >= 'A' && char <= 'F':
		return rune(char-'A') + 10
	}
	return -1
}

// jsonTextChunk is how much decoded text is held before it is passed on.
const jsonTextChunk = 32 << 10

// textRest reads a string whose opening quote has been read, through its closing one, and passes
// on its text in pieces, as encoding/json would decode it: an escape that isn't half of a pair of
// surrogates is its character, and anything else that isn't UTF-8 is U+FFFD.
func (s *jsonScanner) textRest(write func([]byte) error) error {
	out := make([]byte, 0, jsonTextChunk+utf8.UTFMax)
	// bytewise is how far the input is to be read a byte at a time, for text that isn't UTF-8.
	var bytewise int64
	for {
		if len(out) >= jsonTextChunk {
			if err := write(out); err != nil {
				return err
			}
			out = out[:0]
		}
		if s.read >= bytewise {
			run, err := s.plainRun()
			if err != nil {
				return err
			}
			switch {
			case len(run) == 0:
			case utf8.Valid(run):
				if err := write(out); err != nil {
					return err
				}
				out = out[:0]
				if err := write(run); err != nil {
					return err
				}
				s.takeRun(run)
			default:
				bytewise = s.read + int64(len(run))
			}
		}
		char, err := s.next()
		if err != nil {
			return err
		}
		switch {
		case char == '"':
			return write(out)
		case char < 0x20:
			return errNotJSON
		case char == '\\':
			if out, err = s.textEscape(out); err != nil {
				return err
			}
		case char < utf8.RuneSelf:
			out = append(out, char)
		default:
			// A sequence cut short ends at the closing quote, which is read next as itself.
			ahead, _ := s.src.Peek(utf8.UTFMax - 1)
			held := [utf8.UTFMax]byte{char}
			copy(held[1:], ahead)
			r, size := utf8.DecodeRune(held[:1+len(ahead)])
			if _, err := s.src.Discard(size - 1); err != nil {
				return err
			}
			s.read += int64(size - 1)
			out = utf8.AppendRune(out, r)
		}
	}
}

// textEscape reads an escape whose backslash has been read, and appends what it stands for.
func (s *jsonScanner) textEscape(out []byte) ([]byte, error) {
	char, err := s.next()
	if err != nil {
		return out, err
	}
	switch char {
	case '"', '\\', '/':
		return append(out, char), nil
	case 'b':
		return append(out, '\b'), nil
	case 'f':
		return append(out, '\f'), nil
	case 'n':
		return append(out, '\n'), nil
	case 'r':
		return append(out, '\r'), nil
	case 't':
		return append(out, '\t'), nil
	case 'u':
	default:
		return out, errNotJSON
	}
	first, err := s.hex4()
	if err != nil {
		return out, err
	}
	if utf16.IsSurrogate(first) {
		if pair := utf16.DecodeRune(first, s.peekEscape()); pair != unicode.ReplacementChar {
			_, err := s.src.Discard(6)
			s.read += 6
			return utf8.AppendRune(out, pair), err
		}
		first = unicode.ReplacementChar
	}
	return utf8.AppendRune(out, first), nil
}

// peekEscape is the \uXXXX coming next, read without being taken, or -1 when none is.
func (s *jsonScanner) peekEscape() rune {
	ahead, _ := s.src.Peek(6)
	if len(ahead) != 6 || ahead[0] != '\\' || ahead[1] != 'u' {
		return -1
	}
	var code rune
	for _, char := range ahead[2:] {
		digit := hexDigit(char)
		if digit < 0 {
			return -1
		}
		code = code<<4 | digit
	}
	return code
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

// addRun adds symbols that hold neither a line ending nor anything else add would pass over.
func (b *base64Stream) addRun(run []byte) error {
	for len(run) > 0 {
		if b.invalid {
			return nil
		}
		if b.padded {
			b.invalid = true
			return nil
		}
		taken := min(cap(b.symbols)-len(b.symbols), len(run))
		b.symbols = append(b.symbols, run[:taken]...)
		run = run[taken:]
		if len(b.symbols) == cap(b.symbols) {
			if err := b.flush(); err != nil {
				return err
			}
		}
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
