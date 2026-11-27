package message

import (
	"strings"
	"testing"
)

// builtHeaderWithBoundary is headerWithBoundary as it was before it wrote the field a parameter at a
// time: parameterField, holding every piece of the field and the field it builds (#2107).
func builtHeaderWithBoundary(header, boundary string) string {
	field, at := contentTypeField(header)
	if at < 0 {
		return header
	}
	text, eol := strings.CutSuffix(field, "\r\n")
	name, value, _ := strings.Cut(text, ":")
	rewritten, ok := parameterField(name, unfoldHeader(value), boundary)
	if !ok {
		return header
	}
	if eol {
		rewritten += "\r\n"
	}
	fields := headerBlockFields(header)
	fields[at] = rewritten
	return strings.Join(fields, "")
}

// builtReadableField is readableField as it was before it read a piece at a time: every piece in a
// list, and each name percent-encoded again.
func builtReadableField(value string) string {
	pieces := readField(value)
	var b strings.Builder
	b.WriteString(pieces[0].key)
	seen := map[string]bool{}
	for _, piece := range pieces[1:] {
		if piece.key == "" {
			continue
		}
		name := parameterName(piece.key)
		if seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		writeReadableParameter(&b, name, piece)
	}
	return b.String()
}

// malformedParameterFields is every field of a type and up to three of these pieces: each spelling of
// a boundary, repeated and in RFC 2231 form, with comments, 8-bit names, and delimiters that never
// close.
func malformedParameterFields(each func(value string)) {
	heads := []string{"multipart/mixed", "multipart/mixed (J\xc3\xb6)", "multipart/mixed \"x", "m\xc3\xbcltipart/mixed"}
	pieces := []string{
		"boundary=a", "boundary=\"J\xc3\xb6\"", "boundary=J\xc3\xb6", "BOUNDARY=b (c)", "boundary=\"x\" (J\xc3\xb6)",
		"(c) boundary=y", "boundary*0=a", "boundary*1=\"b\"", "boundary*=utf-8''J%C3%B6", "boundary*x=y", "boundary",
		"n\xc3\xa4me=x", "n\xc3\xa4me*=x", "N\xc3\x84ME=y", "name=a.pdf", "name*0=J\xc3\xb6", "name*1=x",
		"=x", "", "p (J\xc3\xb6)", "q=\"unclosed", "r=(open", "s=)", "name=J\xc3\xb6 (1).pdf",
	}
	var walk func(field string, left int)
	walk = func(field string, left int) {
		each(field)
		if left == 0 {
			return
		}
		for _, piece := range pieces {
			walk(field+"; "+piece, left-1)
		}
	}
	for _, head := range heads {
		walk(head, 3)
	}
}

// TestHeaderWithBoundaryMatchesBuilt holds headerWithBoundary, which writes the field one parameter at
// a time, to what parameterField built, byte for byte, over the malformed parameter spellings, folded
// and not, and the mutated headers the readers are held to.
func TestHeaderWithBoundaryMatchesBuilt(t *testing.T) {
	check := func(header string) {
		for _, boundary := range []string{"=_x", "J=C3=B6"} {
			if got, want := headerWithBoundary(header, boundary), builtHeaderWithBoundary(header, boundary); got != want {
				t.Fatalf("%q with %q:\nwrote %q\nbuilt %q", header, boundary, got, want)
			}
		}
	}
	fields := 0
	malformedParameterFields(func(value string) {
		fields++
		check("X: y\r\nContent-Type: " + value + "\r\nZ: w\r\n\r\n")
		check("Content-Type: " + strings.ReplaceAll(value, "; ", ";\r\n ") + "\r\n")
	})
	if fields < 50000 {
		t.Fatalf("only %d fields", fields)
	}
	eachMutatedHeader(check)
}

// TestReadableFieldMatchesBuilt holds readableField, which reads one piece at a time, to what it read
// from a list of every piece.
func TestReadableFieldMatchesBuilt(t *testing.T) {
	check := func(value string) {
		if got, want := readableField(value), builtReadableField(value); got != want {
			t.Fatalf("%q:\nread  %q\nbuilt %q", value, got, want)
		}
	}
	malformedParameterFields(check)
	eachMutatedHeader(func(header string) {
		_, value, _ := strings.Cut(strings.TrimSuffix(header, "\r\n\r\n"), ":")
		check(unfoldHeader(value))
	})
}

// growField is field with its parameters repeated, a line each, to about size, as the server's
// malformed parameter corpus grows a row (#2107).
func growField(field string, size int) string {
	at := strings.Index(field, ";")
	head, parameters := field[:at], strings.TrimSpace(field[at+1:])
	var b strings.Builder
	b.WriteString(head)
	for b.Len() < size {
		b.WriteString(";\r\n ")
		b.WriteString(parameters)
	}
	return b.String()
}

// threeParts is a multipart of three, the second with fields for its header.
func threeParts(top, delimiter, fields string) string {
	return "From: a@b.test\r\nSubject: s\r\n" + top + "\r\n\r\n" +
		"--" + delimiter + "\r\nContent-Type: text/plain\r\n\r\nvisible text\r\n" +
		"--" + delimiter + "\r\n" + fields + "\r\n\r\ncontent\r\n" +
		"--" + delimiter + "\r\nContent-Type: text/plain\r\n\r\nafter the part\r\n--" + delimiter + "--\r\n"
}

// TestMalformedParameterFieldsAllocateLittle holds what a message of about 1 MiB with one malformed
// field of 768 KiB allocates in each step a store and a FETCH take it through (#2107). An 8-bit
// boundary's field was built whole, as a list of every piece, for each measurement: 27x to 35x the
// message for each, and 20x for one whose repeats a host's downgrade made comments, whose every
// walk kept a new list of them. An 8-bit name was percent-encoded again for each piece by each
// reading: 16.6x, and BODYSTRUCTURE read a list of every piece, 6.2x what a well-formed field's
// costs. Each ceiling is about 1.5x the most a row measures now, with or without the race detector.
func TestMalformedParameterFieldsAllocateLittle(t *testing.T) {
	const size = 768 << 10
	control := threeParts("Content-Type: multipart/mixed; boundary=a", "a", growField("Content-Type: application/pdf; name=a.pdf; other=keep", size))
	structure := func(raw string) {
		BuildBodyStructure(raw, true, false)
		BuildBodyStructure(raw, true, true)
	}
	controlStructure := float64(bytesAllocated(control, structure)) / float64(len(control))
	rows := map[string]string{
		"a quoted 8-bit boundary": threeParts(growField(`Content-Type: multipart/mixed; boundary="J`+"\xc3\xb6"+`"`, size), "J\xc3\xb6", "Content-Type: application/pdf; name=a.pdf"),
		"a bare 8-bit boundary":   threeParts(growField("Content-Type: multipart/mixed; boundary=J\xc3\xb6", size), "J\xc3\xb6", "Content-Type: application/pdf; name=a.pdf"),
		"an 8-bit parameter name": threeParts("Content-Type: multipart/mixed; boundary=a", "a", growField("Content-Type: application/pdf; n\xc3\xa4me=x", size)),
		// What a host's downgrade makes of a repeated 8-bit boundary: one boundary, and each repeat a
		// comment after it.
		"an 8-bit boundary with very many comments": threeParts("Content-Type: multipart/mixed;\r\n boundary*=UTF-8''J%C3%B6"+
			strings.Repeat(" (=?UTF-8?Q?boundary=3D=22J=C3=B6=22?=)", size/40), "J\xc3\xb6", "Content-Type: application/pdf; name=a.pdf"),
	}
	for name, raw := range rows {
		t.Run(name, func(t *testing.T) {
			for _, step := range []struct {
				name  string
				work  func(string)
				times float64
			}{
				{"SevenBitBoundary", func(raw string) { SevenBitBoundary(raw) }, 14},
				{"WireFormSizes", func(raw string) { WireFormSizes(raw) }, 12},
				{"WireForm in a UTF8=ACCEPT session", func(raw string) { WireForm(raw, true) }, 6},
			} {
				times := float64(bytesAllocated(raw, step.work)) / float64(len(raw))
				t.Logf("%s: %.2fx the message", step.name, times)
				if times > step.times {
					t.Errorf("%s allocated %.2fx the message, want at most %gx", step.name, times, step.times)
				}
			}
			times := float64(bytesAllocated(raw, structure)) / float64(len(raw)) / controlStructure
			t.Logf("BODYSTRUCTURE: %.2fx a well-formed field's", times)
			if times > 2 {
				t.Errorf("BODYSTRUCTURE allocated %.2fx what a well-formed field's does, want at most 2x", times)
			}
		})
	}
}
