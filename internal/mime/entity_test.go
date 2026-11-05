package mime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// What this package used to do through go-message, kept here so what replaced it is compared with
// it on real and mutated input.

func legacyParse(raw []byte) (*pipeline.EmailJSON, error) {
	return legacyParseFrom(bytes.NewReader(raw))
}

type legacyStream struct{ streamParse }

func legacyParseFrom(source io.Reader) (*pipeline.EmailJSON, error) {
	var sourceSize int
	if sized, ok := source.(interface{ Len() int }); ok {
		sourceSize = sized.Len()
	}
	// go-message returns a usable *Entity even when it reports an unknown charset
	// or transfer encoding, so only a nil entity is a hard failure.
	entity, err := gomessage.Read(source)
	if entity == nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}

	email := &pipeline.EmailJSON{
		Headers: legacyHeaders(entity.Header),
	}

	body, attachments, inline, calendarEvents, err := legacyEntity(entity, sourceSize, newPartBudget())
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	email.Body = body
	email.Attachments = attachments
	email.Inline = inline
	email.CalendarEvents = calendarEvents

	return email, nil
}

func legacyHeaders(h gomessage.Header) pipeline.Headers {
	// Wrap the raw header to reuse go-message's RFC 5322 address and
	// encoded-word aware helpers.
	mh := gomail.Header{Header: h}

	headers := pipeline.Headers{
		Date:      h.Get("Date"),
		MessageID: h.Get("Message-Id"),
		InReplyTo: h.Get("In-Reply-To"),
		Raw:       make(map[string][]string),
	}

	// Subject returns the RFC 2047-decoded value (or the raw value on error).
	// Sanitize it (and every other decoded scalar header) so an encoded-word that
	// decodes to bytes containing CR/LF cannot inject a header on re-serialization.
	subject, _ := mh.Subject()
	headers.Subject = sanitizeHeaderValue(subject)
	headers.Date = sanitizeHeaderValue(headers.Date)
	headers.MessageID = sanitizeHeaderValue(headers.MessageID)
	headers.InReplyTo = sanitizeHeaderValue(headers.InReplyTo)

	headers.From = legacyAddressList(&mh, "From")
	headers.To = legacyAddressList(&mh, "To")
	headers.Cc = legacyAddressList(&mh, "Cc")

	// References is kept as the raw whitespace-separated list of message-ids
	// (angle brackets preserved), matching the historical output contract.
	if refs := h.Get("References"); refs != "" {
		headers.References = append(headers.References, strings.Fields(refs)...)
	}

	// Preserve all raw headers under their canonical MIME keys. go-message
	// returns the undecoded (RFC 2047-encoded) values, exactly as net/mail did,
	// which downstream DKIM/ARC/Authentication-Results logic relies on.
	//
	// EXCEPT inbound Authentication-Results, which are rehomed to
	// X-Original-Authentication-Results: they are other servers' claims, so they
	// are kept for audit but never under the key this server's own
	// spf_check/dkim_verify/arc_verify entries are appended to. No filter reads
	// a verdict from either key; verdicts travel as pipeline.AuthResults, and
	// any field claiming our own authserv-id is removed from the raw message
	// before it is parsed (StripAuthResults). ARC-* headers are cryptographically
	// verified by arc_verify and are left untouched.
	fields := h.Fields()
	for fields.Next() {
		key := fields.Key()
		if strings.EqualFold(key, "Authentication-Results") {
			key = "X-Original-Authentication-Results"
		}
		headers.Raw[key] = append(headers.Raw[key], fields.Value())
	}

	return headers
}

// settledHeaders has its address lists read, so two parses compare by what they read as.
func settledHeaders(headers pipeline.Headers) pipeline.Headers {
	headers.From = pipeline.Addresses(headers.From.All()...)
	headers.To = pipeline.Addresses(headers.To.All()...)
	headers.Cc = pipeline.Addresses(headers.Cc.All()...)
	return headers
}

func legacyAddressList(h *gomail.Header, key string) pipeline.AddressList {
	list, err := h.AddressList(key)
	if err != nil {
		return pipeline.AddressList{}
	}
	var addrs []pipeline.Address
	for _, addr := range list {
		// addr.Name is RFC 2047-decoded; strip any embedded CR/LF so a crafted
		// display name cannot inject a header when the address is re-serialized.
		addrs = append(addrs, pipeline.Address{
			Name:    sanitizeHeaderValue(addr.Name),
			Address: sanitizeHeaderValue(addr.Address),
		})
	}
	return pipeline.Addresses(addrs...)
}

// legacyEntity converts a go-message entity (whole message or a single part)
// into the body tree, attachments, inline parts, and calendar events.
func legacyEntity(e *gomessage.Entity, sourceSize int, budget *partBudget) (pipeline.Body, []pipeline.Attachment, []pipeline.Attachment, []pipeline.CalendarEvent, error) {
	mediaType, _, _ := e.Header.ContentType()
	mediaType = strings.ToLower(mediaType)

	if mr := e.MultipartReader(); mr != nil {
		if err := budget.enter(1); err != nil {
			return pipeline.Body{}, nil, nil, nil, err
		}
		budget.sourceSize = sourceSize
		return legacyMultipart(mr, mediaType, 1, budget)
	}

	// Single-part body. go-message has already decoded the transfer encoding
	// and (for text/*) converted the charset to UTF-8.
	// The buffer is the source's size, which the decoded body fits unless a charset grows it, and
	// it's the body's own from here on, so the text can be the same octets.
	buffer := bytes.NewBuffer(make([]byte, 0, sourceSize+bytes.MinRead))
	_, _ = buffer.ReadFrom(e.Body)
	content := buffer.Bytes()
	body := pipeline.Body{
		ContentType: mediaType,
		Content:     unsafe.String(unsafe.SliceData(content), len(content)),
	}

	var calEvents []pipeline.CalendarEvent
	if mediaType == "text/calendar" {
		if events, err := ParseCalendar(string(content)); err == nil && len(events) > 0 {
			calEvents = events
		}
	}

	return body, nil, nil, calEvents, nil
}

// legacyMultipart reads the parts of a multipart at depth. Each nested multipart wraps every
// reader above it, so reading a byte at depth d costs d boundary scans: the depth limit is what
// keeps a deeply nested message from costing the square of its depth.
func legacyMultipart(mr gomessage.MultipartReader, mediaType string, depth int, budget *partBudget) (pipeline.Body, []pipeline.Attachment, []pipeline.Attachment, []pipeline.CalendarEvent, error) {
	body := pipeline.Body{ContentType: mediaType}
	var attachments, inline []pipeline.Attachment
	var calendarEvents []pipeline.CalendarEvent

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Unknown charset/encoding still yields a readable part; anything
			// else is a malformed boundary — stop reading further parts.
			if !gomessage.IsUnknownCharset(err) && !gomessage.IsUnknownEncoding(err) {
				break
			}
		}

		if err := budget.part(); err != nil {
			return body, attachments, inline, calendarEvents, err
		}

		partMediaType, _, _ := part.Header.ContentType()
		partMediaType = strings.ToLower(partMediaType)

		// Recurse into nested multiparts (e.g. multipart/alternative inside
		// multipart/mixed), preserving the body tree.
		if pmr := part.MultipartReader(); pmr != nil {
			if err := budget.enter(depth + 1); err != nil {
				return body, attachments, inline, calendarEvents, err
			}
			subBody, subAtt, subInl, subCal, err := legacyMultipart(pmr, partMediaType, depth+1, budget)
			if err != nil {
				return body, attachments, inline, calendarEvents, err
			}
			body.Parts = append(body.Parts, subBody)
			attachments = append(attachments, subAtt...)
			inline = append(inline, subInl...)
			calendarEvents = append(calendarEvents, subCal...)
			continue
		}

		// Every part reads into one buffer. A part that's kept stays there, and one that's encoded
		// gives the room back.
		content, err := budget.readPart(part.Body)
		if err != nil {
			continue
		}
		kept := unsafe.String(unsafe.SliceData(content), len(content))

		disposition, _, _ := part.Header.ContentDisposition()
		disposition = strings.ToLower(disposition)
		filename := legacyFilename(part.Header)

		// Calendar invite: parse events regardless of disposition, and also
		// keep the raw .ics content available as a body part.
		if partMediaType == "text/calendar" {
			if events, parseErr := ParseCalendar(kept); parseErr == nil && len(events) > 0 {
				calendarEvents = append(calendarEvents, events...)
			}
			body.Parts = append(body.Parts, pipeline.Body{
				ContentType: partMediaType,
				Content:     kept,
			})
			continue
		}

		// Inline part (e.g. an embedded image referenced by Content-ID). This is
		// checked before the attachment case so an explicitly inline part with a
		// Content-ID is not misfiled as an attachment when it also has a filename
		// (which would drop the Content-ID needed for cid: rendering).
		contentID := part.Header.Get("Content-Id")
		if disposition == "inline" && contentID != "" {
			inline = append(inline, pipeline.Attachment{
				Filename:    filename,
				ContentType: partMediaType,
				Size:        int64(len(content)),
				Disposition: "inline",
				ContentID:   contentID,
				Content:     base64.StdEncoding.EncodeToString(content),
			})
			budget.release(content)
			continue
		}

		// Attachment.
		if disposition == "attachment" || (filename != "" && !strings.HasPrefix(partMediaType, "text/")) {
			attachments = append(attachments, pipeline.Attachment{
				Filename:    filename,
				ContentType: partMediaType,
				Size:        int64(len(content)),
				Disposition: "attachment",
				Content:     base64.StdEncoding.EncodeToString(content),
			})
			budget.release(content)
			continue
		}

		// Regular body part.
		body.Parts = append(body.Parts, pipeline.Body{
			ContentType: partMediaType,
			Content:     kept,
		})
	}

	return body, attachments, inline, calendarEvents, nil
}

// legacyFilename returns the part's filename, preferring the Content-Disposition
// "filename" parameter and falling back to the Content-Type "name" parameter.
// go-message decodes any RFC 2047 encoded-words in the parameter values.
func legacyFilename(h gomessage.Header) string {
	// Both the Content-Disposition "filename" and Content-Type "name" parameters are
	// RFC 2047-decoded by go-message, so sanitize them: a filename that decodes to a
	// value containing CR/LF must not be able to inject a header when the part is
	// re-serialized. (OSI-23)
	if _, params, err := h.ContentDisposition(); err == nil {
		if fn := params["filename"]; fn != "" {
			return sanitizeHeaderValue(fn)
		}
	}
	if _, params, err := h.ContentType(); err == nil {
		if fn := params["name"]; fn != "" {
			return sanitizeHeaderValue(fn)
		}
	}
	return ""
}

func legacyParseStream(ctx context.Context, source io.Reader, options StreamOptions) (*pipeline.EmailJSON, error) {
	if options.Store == nil || options.MaxText <= 0 {
		return nil, errors.New("parse message: a store and a text limit are required")
	}
	entity, err := gomessage.Read(source)
	if entity == nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	s := &legacyStream{streamParse: streamParse{ctx: ctx, options: options, budget: newPartBudget(), email: &pipeline.EmailJSON{Headers: legacyHeaders(entity.Header)}}}
	mediaType, _, _ := entity.Header.ContentType()
	mediaType = strings.ToLower(mediaType)
	if mr := entity.MultipartReader(); mr != nil {
		if s.failed = s.budget.enter(1); s.failed == nil {
			s.email.Body = s.walk(mr, mediaType, 1)
		}
	} else {
		content, _ := s.readSized(entity.Body, options.SourceSize)
		s.email.Body = pipeline.Body{ContentType: mediaType, Content: content}
		if mediaType == "text/calendar" {
			s.calendar(content)
		}
	}
	if s.failed != nil {
		if errors.Is(s.failed, ErrStructureLimit) {
			return nil, fmt.Errorf("parse message: %w", s.failed)
		}
		return nil, s.failed
	}
	return s.email, nil
}

// walk reads the parts of a multipart at depth, which the Limits in force bound as Parse's
// parseMultipart bounds them.
func (s *legacyStream) walk(mr gomessage.MultipartReader, mediaType string, depth int) pipeline.Body {
	body := pipeline.Body{ContentType: mediaType}
	for s.failed == nil {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !gomessage.IsUnknownCharset(err) && !gomessage.IsUnknownEncoding(err) {
			break // a malformed boundary: nothing further can be read
		}
		if s.failed = s.budget.part(); s.failed != nil {
			break
		}
		partType, _, _ := part.Header.ContentType()
		partType = strings.ToLower(partType)
		if nested := part.MultipartReader(); nested != nil {
			if s.failed = s.budget.enter(depth + 1); s.failed != nil {
				break
			}
			body.Parts = append(body.Parts, s.walk(nested, partType, depth+1))
			continue
		}
		disposition, _, _ := part.Header.ContentDisposition()
		disposition = strings.ToLower(disposition)
		filename := legacyFilename(part.Header)
		contentID := part.Header.Get("Content-Id")
		switch {
		case partType == "text/calendar":
			content, err := s.readText(part.Body)
			if err != nil {
				continue
			}
			s.calendar(content)
			body.Parts = append(body.Parts, pipeline.Body{ContentType: partType, Content: content})
		case disposition == "inline" && contentID != "":
			if stored, ok := s.store(part.Body); ok {
				s.email.Inline = append(s.email.Inline, attachmentOf(filename, partType, "inline", contentID, stored))
			}
		case disposition == "attachment" || (filename != "" && !strings.HasPrefix(partType, "text/")):
			if stored, ok := s.store(part.Body); ok {
				s.email.Attachments = append(s.email.Attachments, attachmentOf(filename, partType, "attachment", "", stored))
			}
		default:
			content, err := s.readText(part.Body)
			if err != nil {
				continue
			}
			body.Parts = append(body.Parts, pipeline.Body{ContentType: partType, Content: content})
		}
	}
	return body
}

// entitySeeds are messages with something for each thing reading a message decides.
func entitySeeds() []string {
	head := "From: \"A, B\" <a@example.test>\r\nTo: b@example.test, C D <c@example.test>\r\nCc: =?utf-8?q?=C3=A9?= <e@example.test>\r\nSubject: =?utf-8?q?=C3=A9?= x\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nMessage-ID: <m@example.test>\r\nReferences: <a@b>  <c@d>\r\nAuthentication-Results: x; none\r\nX-Folded: a\r\n  b\r\n\t c\r\n"
	many := func(field string, n int) string {
		var b strings.Builder
		b.WriteString(field)
		for i := range n {
			fmt.Fprintf(&b, ";\r\n p%d=v%d", i, i)
		}
		return b.String() + "\r\n"
	}
	return []string{
		head + "\r\nplain\r\n",
		head + "Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=E9\r\n",
		head + "Content-Type: text/plain; charset=unknown-x\r\n\r\nbody\r\n",
		head + "Content-Transfer-Encoding: base64\r\n\r\n  aGVsbG8=\r\n",
		head + "Content-Transfer-Encoding: x-unknown\r\n\r\nbody\r\n",
		head + "Content-Type: text/calendar\r\n\r\nBEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:1\r\nSUMMARY:s\r\nDTSTART:20200101T000000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		head + "Content-Type: multipart/mixed; boundary=B\r\n\r\npreamble\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\none\r\n--B\r\nContent-Type: text/html\r\nContent-Disposition: inline\r\n\r\n<p>two</p>\r\n--B\r\nContent-Type: image/png; name=\"a.png\"\r\nContent-Id: <i@x>\r\nContent-Disposition: inline; filename=b.png\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8=\r\n--B\r\nContent-Type: application/pdf; name*=utf-8''a%20b.pdf\r\nContent-Disposition: attachment; filename*0=\"x\"; filename*1=\"y.pdf\"\r\n\r\nPDF\r\n--B--\r\nepilogue\r\n",
		head + "Content-Type: multipart/mixed; boundary=\"O\"\r\n\r\n--O\r\nContent-Type: multipart/alternative; boundary=I\r\n\r\n--I\r\n\r\ntext\r\n--I\r\nContent-Type: text/html\r\n\r\n<p>h</p>\r\n--I--\r\n--O\r\nContent-Type: message/rfc822\r\n\r\nSubject: inner\r\n\r\nb\r\n--O--\r\n",
		head + "Content-Type: multipart/mixed; boundary=N\n\n--N\nContent-Type: text/plain\n\nunix\n--N--\n",
		head + many("Content-Type: text/plain; charset=iso-8859-1", 40) + "\r\nbody =E9\r\n",
		head + many("Content-Type: multipart/mixed; boundary=M", 40) + "\r\n--M\r\n" + many("Content-Type: text/plain; charset=utf-8; name=n", 40) + many("Content-Disposition: attachment; filename=f.txt", 40) + "\r\nkept\r\n--M--\r\n",
		head + "Content-Type: a/b; p=1; p=2\r\n\r\nrepeat\r\n",
		head + many("Content-Type: a/b; q=\"1\"; q=1", 40) + "\r\nsame\r\n",
		head + many("Content-Type: a/b; q=1; q=2", 40) + "\r\ndifferent\r\n",
		head + many("Content-Type: a/b; q=\"x\\\"y\"; q=\"x\\\"z\"", 40) + "\r\nquoted\r\n",
		head + many("Content-Type: text/plain; charset*=utf-8''utf-8", 40) + ";\r\n\r\ntrailing\r\n",
		head + many("Content-Type: text/plain", 40) + "; bad\r\n\r\nbroken\r\n",
		head + "Content-Type:\r\nContent-Type: text/html\r\n\r\nfirst is empty\r\n",
		"Subject: no blank line",
		" leading: space\r\n\r\nb",
		"no colon here\r\n\r\nb",
		"Bad Key: x\r\n\r\nb",
		": no key\r\nSubject: s\r\n\r\nb",
		"",
	}
}

func mutateEntity(random *rand.Rand, seed string) []byte {
	data := []byte(seed)
	alphabet := []byte(";=\"\\:,<>@ \t\r\n-*'%aB0.?()\xc3\xa9\xff")
	for range 1 + random.Intn(3) {
		if len(data) == 0 {
			break
		}
		at := random.Intn(len(data))
		c := alphabet[random.Intn(len(alphabet))]
		switch random.Intn(4) {
		case 0:
			data = append(data[:at:at], append([]byte{c}, data[at:]...)...)
		case 1:
			data = append(data[:at:at], data[at+1:]...)
		case 2:
			data[at] = c
		default:
			end := min(len(data), at+random.Intn(40))
			data = append(data[:at:at], data[end:]...)
		}
	}
	return data
}

func entityStore(_ context.Context, part io.Reader) (StoredPart, error) {
	content, _ := io.ReadAll(part)
	return StoredPart{Storage: "test", Ref: string(content), Size: int64(len(content))}, nil
}

// Parse and ParseStream answer what they answered through go-message, on every seed and on what
// mutating them makes. A malformed header fails the same way and a part left out is left out.
func TestEntityReadsAsGoMessageDoes(t *testing.T) {
	check := func(raw []byte) {
		t.Helper()
		want, wantErr := legacyParse(raw)
		got, gotErr := Parse(raw)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("%q: Parse error %v, go-message %v", raw, gotErr, wantErr)
		}
		if got != nil && want != nil {
			got.Headers, want.Headers = settledHeaders(got.Headers), settledHeaders(want.Headers)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%q:\nParse     %+v\ngo-message %+v", raw, got, want)
		}
		options := StreamOptions{Store: entityStore, MaxText: math.MaxInt64}
		wantStream, wantErr := legacyParseStream(context.Background(), bytes.NewReader(raw), options)
		gotStream, gotErr := ParseStream(context.Background(), bytes.NewReader(raw), options)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("%q: ParseStream error %v, go-message %v", raw, gotErr, wantErr)
		}
		if gotStream != nil && wantStream != nil {
			gotStream.Headers, wantStream.Headers = settledHeaders(gotStream.Headers), settledHeaders(wantStream.Headers)
		}
		if !reflect.DeepEqual(gotStream, wantStream) {
			t.Fatalf("%q:\nParseStream %+v\ngo-message  %+v", raw, gotStream, wantStream)
		}
		options.HeadersRead = true
		noHeaders, gotErr := ParseStream(context.Background(), bytes.NewReader(raw), options)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("%q: ParseStream with headers read: error %v, go-message %v", raw, gotErr, wantErr)
		}
		if wantStream != nil {
			noHeaders.Headers = wantStream.Headers
			if !reflect.DeepEqual(noHeaders, wantStream) {
				t.Fatalf("%q:\nheaders read %+v\ngo-message   %+v", raw, noHeaders, wantStream)
			}
		}
		wantHeaders, wantErr := legacyParseHeaders(raw)
		gotHeaders, gotErr := ParseHeaders(bytes.NewReader(raw))
		if (wantErr == nil) != (gotErr == nil) || !reflect.DeepEqual(settledHeaders(gotHeaders), settledHeaders(wantHeaders)) {
			t.Fatalf("%q: ParseHeaders %+v %v, go-message %+v %v", raw, gotHeaders, gotErr, wantHeaders, wantErr)
		}
	}
	seeds := entitySeeds()
	for _, seed := range seeds {
		check([]byte(seed))
	}
	// Up to go-message's limit on a header block, the two read the same. Past it, go-message fails:
	// TestAHeaderBlockHasNoLimit is what happens here.
	for _, size := range []int{1 << 19, 1<<20 - 40} {
		check([]byte("X-Long: " + strings.Repeat("y", size) + "\r\n\r\nb\r\n"))
		check([]byte("X-Long: " + strings.Repeat("y ", size/2) + "\r\n\r\nb\r\n"))
	}
	random := rand.New(rand.NewSource(1915))
	for range entityMutations() {
		check(mutateEntity(random, seeds[random.Intn(len(seeds))]))
	}
}

// A long field doesn't stop a message being read, by anything that reads it: a message not read is
// one whose attachments and threading fields no filter sees.
func TestAHeaderBlockHasNoLimit(t *testing.T) {
	for _, size := range []int{1<<20 - 12, 1<<20 - 11, 1 << 20, 1<<20 + 40, 5 << 20} {
		long := strings.Repeat("y", size)
		for _, value := range []string{long, strings.Repeat("y ", size/2)} {
			want := strings.TrimSpace(value)
			message := "X-Long: " + value + "\r\nIn-Reply-To: <p@x>\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\ntext\r\n--B\r\nContent-Type: application/pdf; name=a.pdf\r\n\r\nPDF\r\n--B--\r\n"
			parsed, err := ParseReader(strings.NewReader(message))
			if err != nil {
				t.Fatalf("%d: %v", size, err)
			}
			if got := parsed.Headers.Raw["X-Long"]; len(got) != 1 || strings.TrimSpace(got[0]) != want {
				t.Fatalf("%d: X-Long of %d values", size, len(got))
			}
			if parsed.Headers.InReplyTo != "<p@x>" || len(parsed.Attachments) != 1 || parsed.Attachments[0].Filename != "a.pdf" {
				t.Fatalf("%d: in-reply-to %q, attachments %+v", size, parsed.Headers.InReplyTo, parsed.Attachments)
			}
			headers, err := ParseSizedHeaders(strings.NewReader(message), int64(len(message)))
			if err != nil || headers.InReplyTo != "<p@x>" {
				t.Fatalf("%d: headers %q (%v)", size, headers.InReplyTo, err)
			}
			store := &memoryStore{}
			streamed, err := ParseStream(context.Background(), strings.NewReader(message), StreamOptions{Store: store.put, MaxText: 1 << 20, HeadersRead: true, SourceSize: int64(len(message))})
			if err != nil || len(streamed.Attachments) != 1 {
				t.Fatalf("%d: streamed %+v (%v)", size, streamed, err)
			}
		}
	}
}

func entityMutations() int {
	if testing.Short() {
		return 2000
	}
	return 20000
}

func legacyParseHeaders(raw []byte) (pipeline.Headers, error) {
	entity, err := gomessage.Read(bytes.NewReader(raw))
	if entity == nil {
		return pipeline.Headers{}, err
	}
	return legacyHeaders(entity.Header), nil
}

// Every list reads as net/mail reads it, because that's what every reader of one was given before.
func TestAddressesMatchNetMail(t *testing.T) {
	seeds := []string{
		"=?utf-8?q??= <a@b.test>", "=?utf-8?q?x?= (c) <a@b.test>", "=?utf-8?q?=zz?= <a@b.test>", "=?utf-8?q?=aB?= <a@b.test>", "=?bogus?q?x?= <a@b.test>",
		"=?utf-8?b?eA==?= <a@b.test>", "a@b.test (=?bogus?q?x?=)", "a@b.test (=?utf-8?q?x?= y)", "=?utf-8?x?y?= <a@b.test>", "=??q?x?= <a@b.test>", "=?us-ascii?Q?x?= =?ISO-8859-1?q?y?= Z <a@b.test>",
		"=?utf-8?q?x?= =?utf-8?q??= <a@b.test>", "=?utf-8?q?x?y?= <a@b.test>", "=?utf-8?q?x\x7f?= <a@b.test>", "g: =?utf-8?q?x?= <a@b.test>, c@d.test (e);, f@g.test", "a@b.test, \"\" <c@d.test>", "=?utf-8?q?\xc3\xa9?= <a@b.test>",
		"a@b.test", "<a@b.test>", "Name <a@b.test>", "First Last <a@b.test>", "\"Last, First\" <a@b.test>", "\"\" <a@b.test>",
		"a@b.test, c@d.test", "a@b.test,c@d.test,\tE F <e@f.test>", " a@b.test ", "a.b.c@d.e.f", "Mr. X <a@b.test>", "a.b <c@d>",
		"a@b.test (comment)", "=?utf-8?q?x?= <a@b.test>", "x=?y <a@b.test>", "a@b.test,", ",a@b.test", "a@b.test,,c@d.test", "group: a@b.test;", "a b@c.test",
		"a@b.test c@d.test", "<a@b.test> x", "Name <a@b.test> <c@d.test>", "Name", "\"a\"@b.test", "a@[1.2.3.4]", "a..b@c.test", ".a@b.test", "a.@b.test", "a@b..test",
		"a @b.test", "a@ b.test", "< a@b.test>", "<a@b.test >", "Name<a@b.test>", "A  B <a@b.test>", "A\tB <a@b.test>", "\"A\\\"B\" <a@b.test>", "\"\xc3\xa9\" <a@b.test>", "\xc3\xa9 <a@b.test>", "a@\xc3\xa9.test",
		"A.B. C <a@b.test>", "!#$%&'*+-/=?^_`{|}~@x.test", "\"a\tb\" <a@b.test>", "\"a\rb\" <a@b.test>", "A . B <a@b.test>", "A. <a@b.test>", ".A <a@b.test>", "A..B <a@b.test>", "a@b.test\r",
	}
	check := func(value string) {
		t.Helper()
		got := addressList(value).All()
		count := addressList(value).Len()
		want, err := gomail.ParseAddressList(value)
		if err != nil {
			if got != nil || count != 0 {
				t.Fatalf("%q: read as %+v, net/mail fails: %v", value, got, err)
			}
			return
		}
		if len(got) != len(want) || count != len(want) {
			t.Fatalf("%q: read %+v (%d), net/mail %v", value, got, count, want)
		}
		at := 0
		ok := addressList(value).VisitAddresses(func(address string) {
			if at >= len(want) || address != sanitizeHeaderValue(want[at].Address) {
				t.Fatalf("%q: address %d is %q, net/mail %+v", value, at, address, want)
			}
			at++
		})
		if !ok || at != len(want) {
			t.Fatalf("%q: %v, %d addresses, net/mail %+v", value, ok, at, want)
		}
		for i := range got {
			if got[i].Name != sanitizeHeaderValue(want[i].Name) || got[i].Address != sanitizeHeaderValue(want[i].Address) {
				t.Fatalf("%q: read %+v, net/mail %+v", value, got[i], *want[i])
			}
		}
	}
	for _, seed := range seeds {
		check(seed)
	}
	random := rand.New(rand.NewSource(1915))
	alphabet := []byte("<>@,.;:\"\\() \tab=?\xc3")
	for range 20 * entityMutations() {
		data := []byte(seeds[random.Intn(len(seeds))])
		for range 1 + random.Intn(3) {
			if len(data) == 0 {
				break
			}
			at := random.Intn(len(data))
			switch random.Intn(3) {
			case 0:
				data = append(data[:at:at], append([]byte{alphabet[random.Intn(len(alphabet))]}, data[at:]...)...)
			case 1:
				data = append(data[:at:at], data[at+1:]...)
			default:
				data[at] = alphabet[random.Intn(len(alphabet))]
			}
		}
		check(string(data))
	}
}

// Long fields are read once: each of these shapes costs a small multiple of the message to parse.
func TestParsingLongFieldsCostsLittle(t *testing.T) {
	const size = 3 << 18 // 3/4 MiB
	field := func(name, first, each string) string {
		var b strings.Builder
		b.WriteString(name + first)
		for i := 0; b.Len() < size; i++ {
			fmt.Fprintf(&b, each, i, i)
		}
		return b.String() + "\r\n"
	}
	for name, message := range map[string]string{
		"address list":        field("To: ", "to@example.test", ",\r\n \"Name %d\" <a%d@example.test>") + "\r\nb\r\n",
		"parameters":          field("Content-Type: ", "text/plain; charset=utf-8", ";\r\n p%d=v%d") + "\r\nb\r\n",
		"parameters, a line":  field("Content-Type: ", "text/plain; charset=utf-8", "; p%d=v%d") + "\r\nb\r\n",
		"parameters on parts": "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + field("Content-Type: ", "text/plain; charset=utf-8", ";\r\n p%d=v%d") + "\r\nb\r\n--B--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			var email *pipeline.EmailJSON
			got := bytesAllocated(func() {
				var err error
				email, err = ParseReader(strings.NewReader(message))
				if err != nil {
					t.Fatal(err)
				}
			})
			t.Logf("%.1f times the message", float64(got)/float64(len(message)))
			if got > 3*uint64(len(message)) {
				t.Fatalf("parsing %d bytes allocated %d", len(message), got)
			}
			if !strings.Contains(email.Body.Content, "b") && len(email.Body.Parts) == 0 {
				t.Fatalf("nothing read: %+v", email.Body)
			}
		})
	}
}

func bytesAllocated(run func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	run()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// RawHeadersJSON and StringsJSON write what json.Marshal writes.
func TestRawJSONMatchesEncodingJSON(t *testing.T) {
	values := []string{"", "plain", `"quoted" <a@b>`, "back\\slash", "<>&", "\x00\x01\x1f\x7f\b\f\n\r\t", "\xc3\xa9", "\xff", "a\xc3", "\xe2\x80\xa8\xe2\x80\xa9", "\xef\xbf\xbd", "\U0001F600", "/"}
	random := rand.New(rand.NewSource(1915))
	check := func(raw map[string][]string, list []string) {
		t.Helper()
		want, _ := json.Marshal(raw)
		if got := RawHeadersJSON(raw); !bytes.Equal(got, want) {
			t.Fatalf("%q: wrote %q, encoding/json %q", raw, got, want)
		}
		want, _ = json.Marshal(list)
		if got := StringsJSON(list); !bytes.Equal(got, want) {
			t.Fatalf("%q: wrote %q, encoding/json %q", list, got, want)
		}
	}
	check(nil, nil)
	check(map[string][]string{}, []string{})
	check(map[string][]string{"A": nil, "B": {}}, values)
	for range 20000 {
		var list []string
		raw := map[string][]string{}
		for range random.Intn(5) {
			var value strings.Builder
			for range random.Intn(4) {
				piece := values[random.Intn(len(values))]
				if random.Intn(4) == 0 && piece != "" {
					piece = piece[:random.Intn(len(piece))]
				}
				value.WriteString(piece)
			}
			list = append(list, value.String())
			raw[value.String()+"k"] = append(raw[value.String()+"k"], value.String())
		}
		check(raw, list)
	}
}
