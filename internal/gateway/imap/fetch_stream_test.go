package imap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// spoolIn sends the gateway's fetch spool files to a directory of their own for the
// test, and returns it.
func spoolIn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	was := createFetchSpool
	createFetchSpool = func() (*os.File, error) { return os.CreateTemp(dir, "imap-fetch-*") }
	t.Cleanup(func() { createFetchSpool = was })
	return dir
}

func noSpoolLeft(t *testing.T, dir string) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("spool files left: %v", left)
	}
}

// readFetched is all of msg, read through ReadAt in pieces of every size up to 7
// octets and then whole, which must agree.
func readFetched(t *testing.T, msg goimap.FetchedMessage) string {
	t.Helper()
	whole, err := io.ReadAll(io.NewSectionReader(msg, 0, msg.Size()))
	if err != nil {
		t.Fatal(err)
	}
	for step := int64(1); step <= 7; step++ {
		var b strings.Builder
		p := make([]byte, step)
		for off := int64(0); off < msg.Size(); off += step {
			n, err := msg.ReadAt(p, off)
			if err != nil && !(errors.Is(err, io.EOF) && off+int64(n) == msg.Size()) {
				t.Fatalf("ReadAt(%d, %d): %d, %v", step, off, n, err)
			}
			b.Write(p[:n])
		}
		if b.String() != string(whole) {
			t.Fatalf("read in %d-octet pieces: %q, whole %q", step, b.String(), whole)
		}
	}
	return string(whole)
}

// fetchStreamShapes are stored sources, among them ones whose header Downgrade
// rewrites, ones whose header ends with bare line feeds, and ones whose blank line
// falls across the pieces the spool is searched in.
func fetchStreamShapes() map[string]string {
	shapes := map[string]string{
		"7-bit":                        "From: a@example.test\r\nSubject: hi\r\n\r\nbody\r\n",
		"8-bit header":                 "Subject: Gr\xc3\xbc\xc3\x9fe\r\nFrom: J\xc3\xb6rg <j@example.test>\r\n\r\nplain\r\n",
		"8-bit header and body":        sevenBitRaw,
		"bare LF header":               "Subject: caf\xc3\xa9\nX-A: b\n\nbody\nmore\n",
		"bare LF header, CRLF later":   "Subject: caf\xc3\xa9\n\nfirst\r\n\r\nsecond\r\n",
		"no blank line":                "Subject: caf\xc3\xa9\r\nX-B: c\r\n",
		"no blank line, 7-bit":         "Subject: plain\r\n",
		"starts with a blank line":     "\r\n\r\nbody only \xc3\xa9\r\n",
		"8-bit body only":              "Subject: x\r\n\r\ncaf\xe9\r\n",
		"8-bit after a CRLF CRLF body": "Subject: x\r\n\r\nSubject: caf\xc3\xa9\r\n\r\n",
		"8-bit body without charset":   "Subject: x\r\n\r\ncafé\x00\r\n",
		"mixed charsets and NUL":       "Subject: Grüße\x00\r\nContent-Type: text/plain; charset=iso-8859-1\r\n\r\ncaf\xe9\x00\r\n",
		"wide text with NUL as data":   "Content-Type: text/plain; charset=utf-16le\r\n\r\na\x00\xe9\x00\r\n",
		"bare LF with 8-bit and NUL":   "Subject: Grüße\n\ncafé\x00\n",
		"nested 8-bit parts": "Subject: Grüße\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
			"--B\r\nContent-Type: message/rfc822\r\n\r\nSubject: Grüße\r\nContent-Type: text/plain; charset=iso-8859-1\r\n\r\ncaf\xe9\x00\r\n--B--\r\n",
	}
	for _, at := range []int{fetchSpoolChunk - 3, fetchSpoolChunk - 2, fetchSpoolChunk - 1, fetchSpoolChunk, fetchSpoolChunk + 1} {
		head := "Subject: caf\xc3\xa9\r\nX-Pad: "
		head += strings.Repeat("p", at-len(head)) // the blank line's CR is octet at
		shapes[fmt.Sprintf("blank line at %d", at)] = head + "\r\n\r\nbody\r\n"
		lf := "Subject: caf\xc3\xa9\nX-Pad: "
		lf += strings.Repeat("p", at-len(lf))
		shapes[fmt.Sprintf("LF blank line at %d", at)] = lf + "\n\nbody\n"
		// A blank line further on that a search missing the first would take for the header's end.
		shapes[fmt.Sprintf("blank line at %d, another later", at)] = head + "\r\n\r\nbody\r\n\r\nX-Later: caf\xc3\xa9\r\n"
		shapes[fmt.Sprintf("LF blank line at %d, another later", at)] = lf + "\n\nbody\n\nX-Later: caf\xc3\xa9\n"
	}
	return shapes
}

// FetchStream opens exactly what Fetch returns, in a session with UTF-8 enabled
// and one without, and leaves no spool file once closed (#2107).
func TestFetchStreamIsFetch(t *testing.T) {
	dir := spoolIn(t)
	for name, raw := range fetchStreamShapes() {
		for _, utf8 := range []bool{false, true} {
			m := &mailbox{api: storeAPI(t, raw), token: "tok", utf8: utf8}
			want, err := m.Fetch(7)
			if err != nil {
				t.Fatal(err)
			}
			msg, err := m.FetchStream(7)
			if err != nil {
				t.Fatalf("%s utf8=%v: %v", name, utf8, err)
			}
			if got := readFetched(t, msg); got != string(want) {
				t.Errorf("%s utf8=%v:\n got %q\nwant %q", name, utf8, got, want)
			}
			if err := msg.Close(); err != nil {
				t.Fatal(err)
			}
			noSpoolLeft(t, dir)
		}
	}
}

// A session without UTF-8 holds the downgraded header block and nothing of the body:
// the rest is read from the spool file, wherever the blank line falls.
func TestFetchStreamHoldsOnlyTheHeader(t *testing.T) {
	dir := spoolIn(t)
	body := strings.Repeat("0123456789abcdef\r\n", 1<<16)
	for _, at := range []int{100, fetchSpoolChunk - 2, fetchSpoolChunk, 3 * fetchSpoolChunk} {
		for _, nl := range []string{"\r\n", "\n"} {
			head := "Subject: caf\xc3\xa9" + nl + "X-Pad: "
			head += strings.Repeat("p", at-len(head)) + nl + nl
			raw := head + body
			m := &mailbox{api: storeAPI(t, raw), token: "tok"}
			msg, err := m.FetchStream(7)
			if err != nil {
				t.Fatal(err)
			}
			spool := msg.(*spooledMessage)
			if spool.file == nil || len(spool.head) > len(head)+64 || spool.bodyLen != int64(len(body)) {
				t.Errorf("blank line at %d (%q): holds %d octets, file %v, %d from the file", at, nl, len(spool.head), spool.file != nil, spool.bodyLen)
			}
			_ = msg.Close()
			noSpoolLeft(t, dir)
		}
	}
}

// detailAPI answers message 7's detail, and its raw route with raw.
func detailAPI(t *testing.T, raw http.HandlerFunc) *apiclient.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/messages/7", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": sampleDetailUTF8()})
	})
	mux.HandleFunc("/api/v1/messages/7/raw", raw)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return apiclient.New(srv.URL, apiclient.WithMaxRawMessageBytes(1<<20))
}

func sampleDetailUTF8() apiclient.MessageDetail {
	d := sampleDetail(7)
	d.Subject = "Gr\u00fc\u00dfe"
	d.Sender = "j\u00f6rg@ex\u00e4mple.test" // written as is, so Downgrade has work to do
	return d
}

// A message whose source the API does not send is rebuilt as Fetch rebuilds it; a
// source over the read ceiling fails as Fetch fails; a spool the gateway cannot
// write is Fetch's message in memory. None leaves a file.
func TestFetchStreamFallsBackAsFetchDoes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    http.HandlerFunc
		disk   bool // the spool refuses writes
		tooBig bool
	}{
		{name: "no stored source", raw: func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }},
		{name: "empty source", raw: func(w http.ResponseWriter, r *http.Request) {}},
		{name: "API error", raw: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) }},
		{name: "source cut short", raw: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, "Subject: cut\r\n\r\n")
		}},
		{name: "over the ceiling", tooBig: true, raw: func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", 1<<20+1))
		}},
		{name: "spool refuses writes", disk: true, raw: func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "Subject: caf\xc3\xa9\r\n\r\nbody\r\n")
		}},
	} {
		for _, utf8 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/utf8=%v", tc.name, utf8), func(t *testing.T) {
				dir := spoolIn(t)
				if tc.disk {
					createFetchSpool = func() (*os.File, error) {
						f, err := os.CreateTemp(dir, "imap-fetch-*")
						if err != nil {
							return nil, err
						}
						ro, err := os.Open(f.Name())
						_ = f.Close()
						return ro, err
					}
				}
				m := &mailbox{api: detailAPI(t, tc.raw), token: "tok", utf8: utf8}
				want, wantErr := m.Fetch(7)
				msg, err := m.FetchStream(7)
				if tc.tooBig {
					if !errors.Is(err, apiclient.ErrRawMessageTooLarge) || !errors.Is(wantErr, apiclient.ErrRawMessageTooLarge) {
						t.Fatalf("FetchStream %v, Fetch %v; want ErrRawMessageTooLarge", err, wantErr)
					}
					noSpoolLeft(t, dir)
					return
				}
				if err != nil || wantErr != nil {
					t.Fatalf("FetchStream %v, Fetch %v", err, wantErr)
				}
				if got := readFetched(t, msg); got != string(want) {
					t.Errorf("got %q, want %q", got, want)
				}
				_ = msg.Close()
				noSpoolLeft(t, dir)
			})
		}
	}
}

// bigSource is a message of about 9 MiB with a repeated body line.
func bigSource(header, line string) string {
	var b strings.Builder
	b.Grow(9<<20 + len(line))
	b.WriteString(header)
	for b.Len() < 9<<20 {
		b.WriteString(line)
	}
	return b.String()
}

// literalThrough reads the server's octets up to the line tagged tag, each literal
// through a small buffer, and returns the literals' total length.
func literalThrough(t *testing.T, r *bufio.Reader, tag string) int64 {
	t.Helper()
	var total int64
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if i := strings.LastIndex(line, "{"); i >= 0 && strings.HasSuffix(line, "}\r\n") {
			n, err := strconv.ParseInt(line[i+1:len(line)-3], 10, 64)
			if err == nil {
				if _, err := io.CopyN(io.Discard, r, n); err != nil {
					t.Fatal(err)
				}
				total += n
				continue
			}
		}
		if strings.HasPrefix(line, tag+" ") {
			if !strings.HasPrefix(line, tag+" OK") {
				t.Fatalf("%q", line)
			}
			return total
		}
	}
}

// FETCH BODY[] of a 9 MiB message through the gateway allocates a small fraction of
// it, API client and server included: the source goes from the API to a spool file
// and from the file to the client, and the file is gone after (#2107 AC01).
func TestGatewayFetchOfABigMessageHoldsLittleOfIt(t *testing.T) {
	for _, tc := range []struct {
		name, header, line string
		utf8               bool
	}{
		{"seven-bit", "Subject: big\r\n\r\n", strings.Repeat("0123456789abcdef", 4) + "\r\n", false},
		{"legacy-charset", "Subject: big\r\nContent-Type: text/plain; charset=iso-8859-1\r\n\r\n", "caf\xe9\x00\r\n", false},
		{"utf8-default", "Subject: Grüße\r\n\r\n", "café\x00\r\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBigGatewayFetch(t, bigSource(tc.header, tc.line), tc.utf8)
		})
	}
}

func checkBigGatewayFetch(t *testing.T, raw string, utf8 bool) {
	t.Helper()
	dir := spoolIn(t)
	client, server := net.Pipe()
	sess := imapserver.NewSession(server, NewBackend(storeAPI(t, raw), nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	_ = client.SetDeadline(time.Now().Add(60 * time.Second))
	r := bufio.NewReader(client)
	send := func(tag, cmd string) int64 {
		if _, err := fmt.Fprintf(client, "%s %s\r\n", tag, cmd); err != nil {
			t.Fatal(err)
		}
		return literalThrough(t, r, tag)
	}
	_, _ = r.ReadString('\n') // greeting
	send("a1", "LOGIN jorg@example.test pw")
	if utf8 {
		send("e1", "ENABLE UTF8=ACCEPT")
	}
	send("a2", "SELECT INBOX")
	for i, items := range []string{"BODY.PEEK[]", "BODY[]", "RFC822", "BINARY.PEEK[]"} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		n := send(fmt.Sprintf("f%d", i), "UID FETCH 7 "+items)
		runtime.ReadMemStats(&after)
		got := after.TotalAlloc - before.TotalAlloc
		t.Logf("FETCH %s of %d octets allocated %d", items, len(raw), got)
		if n != int64(len(raw)) {
			t.Fatalf("FETCH %s sent %d octets, want %d", items, n, len(raw))
		}
		if got > uint64(len(raw))/8 {
			t.Errorf("FETCH %s of %d octets allocated %d", items, len(raw), got)
		}
		noSpoolLeft(t, dir)
	}
}

// A client that leaves in the middle of a streamed literal leaves no spool file.
func TestGatewayFetchSpoolGoesWhenTheClientLeaves(t *testing.T) {
	dir := spoolIn(t)
	client, server := net.Pipe()
	raw := bigSource("Subject: big\r\n\r\n", strings.Repeat("0123456789abcdef", 4)+"\r\n")
	sess := imapserver.NewSession(server, NewBackend(storeAPI(t, raw), nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	_ = client.SetDeadline(time.Now().Add(60 * time.Second))
	r := bufio.NewReader(client)
	_, _ = r.ReadString('\n')
	for _, cmd := range []string{"a1 LOGIN jorg@example.test pw", "a2 SELECT INBOX"} {
		_, _ = fmt.Fprintf(client, "%s\r\n", cmd)
		literalThrough(t, r, strings.Fields(cmd)[0])
	}
	_, _ = fmt.Fprintf(client, "f UID FETCH 7 BODY.PEEK[]\r\n")
	if line, err := r.ReadString('\n'); err != nil || !strings.HasSuffix(line, "}\r\n") {
		t.Fatalf("%q, %v", line, err)
	}
	if _, err := io.CopyN(io.Discard, r, 1<<20); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	<-done
	noSpoolLeft(t, dir)
}
