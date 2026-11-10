package mime

import (
	"strings"
	"testing"
	"unsafe"
)

// The first value of a repeated name stands, in each form of a name, and a later one has to read.
func TestParseMediaReadsTheFirstOfARepeatedName(t *testing.T) {
	long := strings.Repeat("; p=v", 17)
	for _, row := range []struct {
		field, name, want string
		failed            bool
	}{
		{field: "multipart/mixed; boundary=a; boundary=b", name: "boundary", want: "a"},
		{field: "multipart/mixed; BOUNDARY=a; boundary=b", name: "boundary", want: "a"},
		{field: "multipart/mixed; boundary=a; x=1; x=2", name: "boundary", want: "a"},
		{field: "multipart/mixed; x=1; boundary=a; x=2;", name: "boundary", want: "a"},
		{field: "a/b; filename=a.txt; filename=b.exe", name: "filename", want: "a.txt"},
		{field: "a/b; filename*=utf-8''a.txt; filename*=utf-8''b.exe", name: "filename", want: "a.txt"},
		{field: "a/b; filename*0=a; filename*0=x; filename*1=.txt", name: "filename", want: "a.txt"},
		{field: "a/b; filename=a.txt;; filename=b.exe", name: "filename", want: "a.txt"},
		{field: "text/plain; charset=iso-8859-1; charset=utf-8", name: "charset", want: "iso-8859-1"},
		{field: "a/b; name=a.txt; name=\"b", failed: true},
		{field: "a/b; name=a.txt; name=", failed: true},
		{field: "a/b; name=a.txt; x=1;; y=2", failed: true},
		{field: "a/b; name=a.txt; name=b.exe; bad", failed: true},
	} {
		for _, field := range []string{row.field, strings.Replace(row.field, ";", long+";", 1)} {
			_, params, failed := parseContentType(field)
			if failed != row.failed {
				t.Errorf("%q: failed %v", field, failed)
			}
			if !failed && params[row.name] != row.want {
				t.Errorf("%q: %s is %q, want %q", field, row.name, params[row.name], row.want)
			}
		}
	}
}

// A filename is text for a row and a filter whatever its octets, read as RFC 2231 and RFC 2047 say,
// with no charset guessed for what they don't read.
func TestAPartsNameIsText(t *testing.T) {
	for _, row := range []struct{ field, want string }{
		{"attachment; filename=a\xc3\xa9.exe", "a\u00e9.exe"},
		{"attachment; filename=a\xe9.exe", "a\uFFFD.exe"},
		{"attachment; filename*=iso-8859-1''a%E9.exe", "a\u00e9.exe"},
		{"attachment; filename*=windows-1252''a%80.exe", "a\u20ac.exe"},
		{"attachment; filename*=utf-8''a%E9.exe", "a\uFFFD.exe"},
		{"attachment; filename*=utf-8''a%00b.exe", "a b.exe"},
		{"attachment; filename=\"=?utf-8?Q?a=00b.exe?=\"", "a b.exe"},
		{"attachment; filename*=x-none''a%E9.exe", "=?x-none?Q?a=E9=2Eexe?="},
		{"attachment; filename=\"=?iso-8859-1?Q?a=E9.exe?=\"", "a\u00e9.exe"},
		{"attachment; filename=a.exe (comment)", "a.exe"},
	} {
		disposition, params := parseDisposition(row.field)
		if disposition != "attachment" || params["filename"] != row.want {
			t.Errorf("%q: %q %q, want the filename %q", row.field, disposition, params["filename"], row.want)
		}
	}
}

// A field a client is told has no disposition has none here either, which leaves what its part is
// called to the type's name.
func TestADispositionThatGivesNone(t *testing.T) {
	for _, field := range []string{"", "attachment; filename=\"a.exe", "attachment/x; filename=a.exe", "attachment; x=1;; y=2", "; filename=a.exe"} {
		if disposition, params := parseDisposition(field); disposition != "" || params != nil {
			t.Errorf("%q: %q %q", field, disposition, params)
		}
	}
}

// A boundary and a charset are octets, so an encoded word in one stays those octets, or a filter would
// look for delimiter lines no client does.
func TestABoundaryIsNotDecoded(t *testing.T) {
	_, params, failed := parseContentType("multipart/mixed; boundary=\"=?utf-8?Q?a?=\"; charset=\"=?utf-8?Q?b?=\"")
	if failed || params["boundary"] != "=?utf-8?Q?a?=" || params["charset"] != "=?utf-8?Q?b?=" {
		t.Fatalf("%q, failed %v", params, failed)
	}
}

// A field that's read may be a view of a buffer that's used again, and nothing read of it stays one.
func TestNothingReadIsAViewOfTheField(t *testing.T) {
	for _, field := range []string{
		"multipart/mixed; boundary=abc; charset*=utf-8''def; name*0=ghi; filename=jkl",
		"multipart/mixed; boundary*0=abc; charset=def; name=ghi; filename*=utf-8''jkl",
		"multipart/mixed; boundary=abc (c); charset=def; name=ghi; filename=jkl",
	} {
		buffer := []byte(field)
		mediaType, params, failed := parseContentType(unsafeView(buffer))
		if failed {
			t.Fatalf("%q failed", field)
		}
		disposition, dispParams := parseDisposition(unsafeView(buffer))
		before := mediaType + disposition + params["boundary"] + params["charset"] + params["name"] + params["filename"] + dispParams["filename"]
		for i := range buffer {
			buffer[i] = 'X'
		}
		if after := mediaType + disposition + params["boundary"] + params["charset"] + params["name"] + params["filename"] + dispParams["filename"]; after != before {
			t.Errorf("%q: read %q, which became %q when the buffer did", field, before, after)
		}
	}
}

func unsafeView(buffer []byte) string { return unsafe.String(unsafe.SliceData(buffer), len(buffer)) }
