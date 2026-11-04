package mime

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	gomessage "github.com/emersion/go-message"
)

// Defaults for Limits. Each is what an established implementation already applies to real mail:
//
//   - DefaultMaxReceived is Postfix's hopcount_limit (50): more Received: header fields than
//     that is taken as a mail loop (RFC 5321 §6.3).
//   - DefaultMaxNestingDepth is Postfix's mime_nesting_limit (100).
//   - DefaultMaxParts is Go's mime/multipart limit on the parts of one form (1000, GODEBUG
//     multipartmaxparts).
const (
	DefaultMaxReceived     = 50
	DefaultMaxNestingDepth = 100
	DefaultMaxParts        = 1000
)

// Limits bounds what a message may be made of before it is refused.
type Limits struct {
	// MaxReceived is the most Received: header fields a message may carry, this server's own
	// included, before it is taken as looping.
	MaxReceived int
	// MaxNestingDepth is how deep multiparts may nest: a multipart message is depth 1.
	MaxNestingDepth int
	// MaxParts is how many parts a message may hold, multipart containers included.
	MaxParts int
}

// DefaultLimits are the limits in force until SetLimits changes them.
func DefaultLimits() Limits {
	return Limits{MaxReceived: DefaultMaxReceived, MaxNestingDepth: DefaultMaxNestingDepth, MaxParts: DefaultMaxParts}
}

var limits atomic.Pointer[Limits]

func init() {
	l := DefaultLimits()
	limits.Store(&l)
}

// SetLimits replaces the limits every parse in this process applies. A value that is not
// positive keeps that limit's default: a limit always exists.
func SetLimits(l Limits) {
	d := DefaultLimits()
	if l.MaxReceived <= 0 {
		l.MaxReceived = d.MaxReceived
	}
	if l.MaxNestingDepth <= 0 {
		l.MaxNestingDepth = d.MaxNestingDepth
	}
	if l.MaxParts <= 0 {
		l.MaxParts = d.MaxParts
	}
	limits.Store(&l)
}

// CurrentLimits answers the limits in force.
func CurrentLimits() Limits { return *limits.Load() }

// ErrStructureLimit is what a parse answers, wrapped, for a message nested deeper or holding
// more parts than Limits allows. Nothing past the limit is read.
var ErrStructureLimit = errors.New("message structure over limit")

func structureLimit(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrStructureLimit}, args...)...)
}

// partBudget counts the parts and depth of one parse against the limits in force.
type partBudget struct {
	limits Limits
	parts  int
	// sourceSize is the size of the whole message, when it is known.
	sourceSize int
	// arena holds what's read of the parts. See readPart.
	arena []byte
	// fields reads the parts' headers, in the one buffer the message's header used.
	fields *fieldReader
}

// partArenaLeast is the least an arena holds, for a message whose size isn't known.
const partArenaLeast = 4 << 10

func newPartBudget() *partBudget { return &partBudget{limits: CurrentLimits()} }

// part counts one more part, and answers an error once there are too many.
func (b *partBudget) part() error {
	b.parts++
	if b.parts > b.limits.MaxParts {
		return structureLimit("more than %d MIME parts", b.limits.MaxParts)
	}
	return nil
}

// readPart reads a part's content onto the end of the arena, and answers it. It stays there until
// release, so a part that's kept is a view of the arena and costs no copy of its own, and a
// message's parts together cost one buffer of the message's size, which they nearly always fit in.
// When they don't, because a charset grew them, the part moves to a new arena and what's kept stays
// on the old one. Nothing is written over a part that wasn't released.
func (b *partBudget) readPart(r io.Reader) ([]byte, error) {
	start := len(b.arena)
	for {
		if len(b.arena) == cap(b.arena) {
			held := len(b.arena) - start
			next := b.fields.takeBuffer(max(2*held, fieldBufferDoubles))
			if next == nil {
				next = make([]byte, 0, max(2*held, b.sourceSize+bytes.MinRead, partArenaLeast))
			}
			next = next[:held]
			copy(next, b.arena[start:])
			b.arena, start = next, 0
		}
		n, err := r.Read(b.arena[len(b.arena):cap(b.arena)])
		b.arena = b.arena[:len(b.arena)+n]
		if err == io.EOF {
			return b.arena[start:], nil
		}
		if err != nil {
			b.arena = b.arena[:start]
			return nil, err
		}
	}
}

// release gives the arena back the part readPart last answered, for one nothing keeps.
func (b *partBudget) release(content []byte) {
	b.arena = b.arena[:len(b.arena)-len(content)]
}

// enter answers an error when a multipart at depth would nest too deep.
func (b *partBudget) enter(depth int) error {
	if depth > b.limits.MaxNestingDepth {
		return structureLimit("multipart nested deeper than %d", b.limits.MaxNestingDepth)
	}
	return nil
}

// CheckStructure reads a message from source and answers the ErrStructureLimit that Parse would,
// wrapped, or nil. It holds no part's content, for a caller that must refuse such a message before
// it does anything that needs the message in memory. A message Parse fails on for any other reason
// is nil here.
func CheckStructure(source io.Reader) error {
	entity, _ := gomessage.Read(source)
	if entity == nil {
		return nil
	}
	mr := entity.MultipartReader()
	if mr == nil {
		return nil
	}
	budget := newPartBudget()
	if err := budget.enter(1); err != nil {
		return fmt.Errorf("parse message: %w", err)
	}
	if err := checkParts(mr, 1, budget); err != nil {
		return fmt.Errorf("parse message: %w", err)
	}
	return nil
}

// checkParts counts the parts of a multipart at depth as parseMultipart counts them. Asking for the
// next part passes over what is left of the one before.
func checkParts(mr gomessage.MultipartReader, depth int, budget *partBudget) error {
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil && !gomessage.IsUnknownCharset(err) && !gomessage.IsUnknownEncoding(err) {
			return nil // a malformed boundary: nothing further can be read
		}
		if err := budget.part(); err != nil {
			return err
		}
		if nested := part.MultipartReader(); nested != nil {
			if err := budget.enter(depth + 1); err != nil {
				return err
			}
			if err := checkParts(nested, depth+1, budget); err != nil {
				return err
			}
		}
	}
}

// ReceivedCount counts the Received: header fields in raw's header block, which ends at the
// first empty line. A folded field counts once.
func ReceivedCount(raw []byte) int {
	count := 0
	for rest := raw; len(rest) != 0; {
		line := rest
		if end := bytes.IndexByte(rest, '\n'); end >= 0 {
			line, rest = rest[:end], rest[end+1:]
		} else {
			rest = nil
		}
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		name, _, found := bytes.Cut(line, []byte(":"))
		if found && bytes.EqualFold(bytes.TrimRight(name, " \t"), []byte("Received")) {
			count++
		}
	}
	return count
}

// CountReceived is ReceivedCount for a message read from src. It reads no further than the
// header block, and holds no more than a buffer of it. The only error is a failure reading src.
func CountReceived(src io.Reader) (int, error) {
	const name = "received"
	reader := bufio.NewReader(src)
	count := 0
	for {
		first, onlyCR, continuation, decided, matched := true, true, false, false, 0
		for {
			c, err := reader.ReadByte()
			if err == io.EOF {
				return count, nil
			}
			if err != nil {
				return count, err
			}
			if c == '\n' {
				break
			}
			if c != '\r' {
				onlyCR = false
			}
			if first {
				first, continuation = false, c == ' ' || c == '\t'
			}
			switch {
			case continuation || decided:
			case c == ':':
				decided = true
				if matched == len(name) {
					count++
				}
			case matched < len(name) && c|0x20 == name[matched]:
				matched++
			case matched < len(name) || (c != ' ' && c != '\t'):
				decided = true
			}
		}
		if onlyCR {
			return count, nil
		}
	}
}

// TooManyReceived reports whether raw carries more Received: header fields than the limit in
// force, which RFC 5321 §6.3 takes as a mail loop.
func TooManyReceived(raw []byte) bool {
	return ReceivedCount(raw) > CurrentLimits().MaxReceived
}
