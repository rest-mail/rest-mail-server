package message

import (
	"strings"
	"sync"
	"unsafe"
)

// FieldScratch holds what reading a field builds and then drops: its text unfolded, or what's kept of
// it. A sender chooses how long a field is, and storing a message reads the same one several times, so
// each reading would otherwise allocate about the field's size again. The next reading uses the same
// memory, for this message or another.
//
// What String answers is good only until Release. Nothing that outlives a reading may be a view of it,
// so what's kept of one is cloned on its way out.
type FieldScratch struct{ b []byte }

var fieldScratches = sync.Pool{New: func() any { return new(FieldScratch) }}

// TakeFieldScratch is an empty FieldScratch with room for size without growing.
func TakeFieldScratch(size int) *FieldScratch {
	s := fieldScratches.Get().(*FieldScratch)
	s.b = s.b[:0]
	s.room(size)
	return s
}

// Release gives the memory back. Nothing String answered can be read after it.
func (s *FieldScratch) Release() { fieldScratches.Put(s) }

// readingSlack is what an empty scratch is given over what it's asked for. Two readings of one field ask
// for a few bytes apart, by whether they count its first space and its line end, and each byte short costs
// another buffer of the field.
const readingSlack = 16

// room doubles what it's holding at the least, as appending grows a long field by a quarter at a time,
// which allocates five times what it ends up holding. An empty scratch has nothing to grow from: it needs
// what's asked for and no more, whatever it last held. Doubling that one spent 1.5 buffers of the field on
// one reading whenever the pool answered with a scratch a little too small, and none when it answered with
// a new one, so a message's cost came down to which processor its goroutine was on.
func (s *FieldScratch) room(need int) {
	if cap(s.b)-len(s.b) >= need {
		return
	}
	size := need + readingSlack
	if len(s.b) > 0 {
		size = max(2*cap(s.b), len(s.b)+need)
	}
	grown := make([]byte, len(s.b), size)
	copy(grown, s.b)
	s.b = grown
}

func (s *FieldScratch) Write(p []byte) (int, error) {
	s.room(len(p))
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *FieldScratch) WriteString(text string) (int, error) {
	s.room(len(text))
	s.b = append(s.b, text...)
	return len(text), nil
}

// String is what's been written, as a view of the memory.
func (s *FieldScratch) String() string { return unsafe.String(unsafe.SliceData(s.b), len(s.b)) }

// holds says text is a view of this memory.
func (s *FieldScratch) holds(text string) bool {
	_, in := viewOffset(s.String(), text)
	return in
}

// viewOffset is where text stands in whole, when it's a view of it.
func viewOffset(whole, text string) (int, bool) {
	if len(whole) == 0 || len(text) == 0 {
		return 0, false
	}
	start, at := uintptr(unsafe.Pointer(unsafe.StringData(whole))), uintptr(unsafe.Pointer(unsafe.StringData(text)))
	if at < start || at >= start+uintptr(len(whole)) {
		return 0, false
	}
	return int(at - start), true
}

// withoutCRLF is unfoldHeader in s: a bare LF stays. It answers value itself when there's no CRLF.
func (s *FieldScratch) withoutCRLF(value string) string {
	if !strings.Contains(value, "\r\n") {
		return value
	}
	s.b = s.b[:0]
	s.room(len(value))
	for {
		before, after, found := strings.Cut(value, "\r\n")
		s.b = append(s.b, before...)
		if !found {
			return s.String()
		}
		value = after
	}
}

// Unfolded is value with every line break taken out (RFC 5322 §2.2.3), a CRLF or a bare LF, in s, or
// value itself when it holds none.
func (s *FieldScratch) Unfolded(value string) string {
	if strings.IndexByte(value, '\n') < 0 {
		return value
	}
	s.b = s.b[:0]
	s.room(len(value))
	for {
		at := strings.IndexByte(value, '\n')
		if at < 0 {
			s.b = append(s.b, value...)
			return s.String()
		}
		s.b = append(s.b, strings.TrimSuffix(value[:at], "\r")...)
		value = value[at+1:]
	}
}
