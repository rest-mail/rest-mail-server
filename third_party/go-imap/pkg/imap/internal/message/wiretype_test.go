package message

import (
	"bufio"
	"fmt"
	"math/rand"
	"net/textproto"
	"strings"
	"testing"
)

func wireTypeSeeds() []string {
	var p strings.Builder
	for i := range 30 {
		fmt.Fprintf(&p, "; p%d=v%d", i, i)
	}
	many := p.String()
	return []string{
		"Content-Type: text/plain; charset=x\r\n\r\n",
		"content-type:\r\n  text/plain;\r\n\tcharset=x\r\nCONTENT-TRANSFER-ENCODING: 8bit\r\n\r\n",
		"Content-Type:\r\nContent-Type: a/b\r\nContent-Transfer-Encoding:\r\n \r\n 7bit\r\n\r\n",
		"Content-Type : a/b\r\nContent-Type: c/d\r\n\r\n",
		" Content-Type: a/b\r\n\r\n",
		"X y: 1\r\nContent-Type: a/b\r\n",
		"X\xf6: 1\r\nContent-Type: a/b\r\n\r\n",
		"X: a\rb\r\nContent-Type: a/b\r\n\r\n",
		"NoColon\r\nContent-Type: a/b\r\n\r\n",
		"Content-Type: a/b\r\n ",
		"Content-Type: multipart/mixed" + many + "; boundary=B\r\nX: \xc3\xbc\r\n\r\n",
		"Content-Type: multipart/mixed" + many + "; boundary*0=A; boundary*1=B; p3=v3; P4=\"v4\"\r\n\r\n",
		"Content-Type: text/plain" + many + " (c\xc3\xb6); q=\"(\"; r=J\xc3\xb6rg (1).pdf; CHARSET=y\r\n\r\n",
		"Content-Type: text/plain" + many + "; p1=other; charset=y\r\n\r\n",
		"Content-Type: text/plain" + many + "; p1=\"v1\"; charset=y;\r\n\r\n",
		"Content-Type: text/plain" + many + "; broken; charset=y\r\n\r\n",
		"Content-Type: multipart/mixed" + many + "; boundary=\"\xc3\xb6b\"; BOUNDARY=x\r\n\r\n",
		"Content-Type: text/plain" + many + "; charset*=utf-8''x; a*0=b; a*1*=%C3%BC\r\nContent-Transfer-Encoding: x-nope\r\n\r\n",
	}
}

func mutateHeader(r *rand.Rand, s string) string {
	const alphabet = "\r\n;=\"()*'%,:<>@\\ \t\xc3\xbc\xf6-aB0"
	b := []byte(s)
	for range 1 + r.Intn(4) {
		if len(b) == 0 {
			break
		}
		at := r.Intn(len(b))
		switch r.Intn(3) {
		case 0:
			b[at] = alphabet[r.Intn(len(alphabet))]
		case 1:
			b = append(b[:at], append([]byte{alphabet[r.Intn(len(alphabet))]}, b[at:]...)...)
		case 2:
			b = append(b[:at], b[at+1:]...)
		}
	}
	return string(b)
}

func eachMutatedHeader(each func(header string)) {
	r := rand.New(rand.NewSource(1915))
	// A last line with no line break, on and around the reader's buffer.
	for _, size := range []int{4094, 4095, 4096, 4097, 8192, 8193} {
		each("Content-Type: " + strings.Repeat("a", size-len("Content-Type: ")))
		each("X: y\r\nContent-Type: " + strings.Repeat("a", size-len("Content-Type: ")))
		each("X: y\r\nContent-Type: a/b\r\n " + strings.Repeat("a", size))
	}
	for _, seed := range wireTypeSeeds() {
		each(seed)
		for range 3000 {
			each(mutateHeader(r, seed))
		}
	}
}

func TestContentFieldsMatchTheLibrary(t *testing.T) {
	eachMutatedHeader(func(header string) {
		h, _ := textproto.NewReader(bufio.NewReaderSize(strings.NewReader(header), min(len(header), 4096))).ReadMIMEHeader()
		contentType, encoding := contentFields(header)
		if contentType != h.Get("Content-Type") || encoding != h.Get("Content-Transfer-Encoding") {
			t.Fatalf("%q: read %q and %q, the library %q and %q", header, contentType, encoding, h.Get("Content-Type"), h.Get("Content-Transfer-Encoding"))
		}
	})
}

func TestWireTypeMatchesMediaType(t *testing.T) {
	eachMutatedHeader(func(header string) {
		for _, node := range []mimeNode{{header: header, defaultType: "text/plain"}, {header: header, defaultType: "message/rfc822", depth: maxMIMEDepth}} {
			mt, params := node.mediaType()
			got := node.wireType()
			if got.mt != mt || got.boundary != params["boundary"] || got.charset != params["charset"] {
				t.Fatalf("%q: read %+v, want %q %q %q", header, got, mt, params["boundary"], params["charset"])
			}
		}
	})
}

// A field of many parameters drops the ones nothing reads, and a name repeated among those
// must read as it does in the whole field, whatever the whole field makes of a repeat.
func TestManyParametersReadARepeatedNameAsTheWholeField(t *testing.T) {
	var p strings.Builder
	for i := range 30 {
		fmt.Fprintf(&p, "; p%d=v%d", i, i)
	}
	many := p.String()
	for _, value := range []string{
		"multipart/mixed" + many + "; p1=other; boundary=B",
		"multipart/mixed; boundary=B" + many + "; P1=\"other\"",
		"multipart/mixed" + many + "; p1=other; p2=other; p1=another; boundary=B",
		"multipart/mixed" + many + "; p1=\"v1\"; boundary=B",
		"text/plain" + many + "; p1=other; charset=y",
		"text/plain" + many + "; p1=other; broken; charset=y",
	} {
		node := mimeNode{header: "Content-Type: " + value + "\r\n\r\n", defaultType: "application/x-default"}
		mt, params := node.mediaType()
		if got := node.wireType(); got.mt != mt || got.boundary != params["boundary"] || got.charset != params["charset"] {
			t.Errorf("%q: read %+v, want %q %q %q", value, got, mt, params["boundary"], params["charset"])
		}
		want, wantOK := parameterField("Content-Type", value, "")
		var b strings.Builder
		if ok := WriteParameterField(&b, "Content-Type", value); ok != wantOK || b.String() != want {
			t.Errorf("%q: wrote %t %q, want %t %q", value, ok, b.String(), wantOK, want)
		}
	}
}

// parameterField holds the whole field and WriteParameterField holds none of it, so each
// checks the other.
func TestWriteParameterFieldMatchesBuilt(t *testing.T) {
	eachMutatedHeader(func(header string) {
		_, value, _ := strings.Cut(strings.TrimSuffix(header, "\r\n\r\n"), ":")
		value = unfoldHeader(value)
		want, wantOK := parameterField("Content-Type", value, "")
		var b strings.Builder
		ok := WriteParameterField(&b, "Content-Type", value)
		if ok != wantOK || b.String() != want {
			t.Fatalf("%q: wrote %t %q, want %t %q", value, ok, b.String(), wantOK, want)
		}
	})
}
