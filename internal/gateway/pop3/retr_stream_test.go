package pop3

import (
	"bufio"
	"bytes"
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
	"strings"
	"testing"
	"time"

	gopop3 "github.com/rest-mail/go-pop3/pkg/pop3"
	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// spoolIn sends the gateway's RETR spool files to a directory of their own for the
// test, and returns it.
func spoolIn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	was := createRetrSpool
	createRetrSpool = func() (*os.File, error) { return os.CreateTemp(dir, "pop3-retr-*") }
	t.Cleanup(func() { createRetrSpool = was })
	return dir
}

func noSpoolLeft(t *testing.T, dir string) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("spool files left: %v", left)
	}
}

// readStored is all of msg, read through ReadAt in pieces of every size up to 7
// octets and then whole, which must agree.
func readStored(t *testing.T, msg gopop3.StoredMessage) string {
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
	if _, err := msg.ReadAt(make([]byte, 1), -1); err == nil {
		t.Fatal("ReadAt at a negative offset succeeded")
	}
	return string(whole)
}

// retrStreamShapes are stored sources: every shape TestSizesMatchWhatRetrSends sizes,
// ones whose header the downgrade rewrites or whose header ends with bare line feeds,
// ones that hold a NUL, and ones whose blank line falls across the pieces the spool is
// searched in.
func retrStreamShapes() map[string]string {
	shapes := map[string]string{
		"7-bit":                        "From: a@example.test\r\nSubject: hi\r\n\r\nbody\r\n",
		"8-bit header":                 "Subject: Gr\xc3\xbc\xc3\x9fe\r\nFrom: J\xc3\xb6rg <j@example.test>\r\n\r\nplain\r\n",
		"8-bit header and body":        utf8Raw,
		"bare LF header":               "Subject: caf\xc3\xa9\nX-A: b\n\nbody\nmore\n",
		"bare LF header, CRLF later":   "Subject: caf\xc3\xa9\n\nfirst\r\n\r\nsecond\r\n",
		"no blank line":                "Subject: caf\xc3\xa9\r\nX-B: c\r\n",
		"no blank line, 7-bit":         "Subject: plain\r\n",
		"starts with a blank line":     "\r\n\r\nbody only \xc3\xa9\r\n",
		"starts with a bare LF":        "\nSubject: caf\xc3\xa9\n\nbody\n",
		"8-bit body only":              "Subject: x\r\n\r\ncaf\xe9\r\n",
		"8-bit after a CRLF CRLF body": "Subject: x\r\n\r\nSubject: caf\xc3\xa9\r\n\r\n",
		"NUL in the body":              "Subject: x\r\n\r\na\x00b\r\n",
		"NUL under an 8-bit header":    "Subject: caf\xc3\xa9\r\nX-N: a\x00b\r\n\r\na\x00b\r\n",
		"NUL in a 7-bit header":        "Subject: x\x00y\r\n\r\nbody\r\n",
		"NUL in data": "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: application/octet-stream\r\n\r\n\x00\x01\r\n--b\r\n" +
			"Content-Type: text/plain\r\n\r\nt\x00t\r\n--b--\r\n",
		"8-bit boundary, 7-bit header": "Content-Type: multipart/mixed; boundary=\"J\xc3\xb6\"\r\n\r\n--J\xc3\xb6\r\n\r\nx\r\n--J\xc3\xb6--\r\n",
		"8-bit boundary under an 8-bit subject": "Subject: J\xc3\xb6\r\nContent-Type: multipart/mixed; boundary=\"\xc3\xb6\"\r\n\r\n" +
			"--\xc3\xb6\r\n\r\nx\r\n--\xc3\xb6--",
		"7-bit boundary under an 8-bit subject": "Subject: J\xc3\xb6\r\nContent-Type: multipart/mixed; boundary=\"b\"\r\n\r\n--b\r\n\r\nx\xc3\xb6\r\n--b--\r\n",
		"attachment":                            storedRawWithAttachment,
	}
	for _, tc := range sizeShapes {
		shapes["size shape: "+tc.name] = tc.raw
	}
	for _, at := range []int{retrSpoolChunk - 3, retrSpoolChunk - 2, retrSpoolChunk - 1, retrSpoolChunk, retrSpoolChunk + 1} {
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

// OpenMessage opens exactly what Retrieve returns, in a session with UTF-8 enabled and
// one without, and leaves no spool file once closed (#2107).
func TestOpenMessageIsRetrieve(t *testing.T) {
	dir := spoolIn(t)
	for name, raw := range retrStreamShapes() {
		for _, utf8 := range []bool{false, true} {
			m := &mailbox{api: storeAPI(t, raw), token: "tok", utf8: utf8}
			want, err := m.Retrieve("7")
			if err != nil {
				t.Fatal(err)
			}
			msg, err := m.OpenMessage("7")
			if err != nil {
				t.Fatalf("%s utf8=%v: %v", name, utf8, err)
			}
			if got := readStored(t, msg); got != string(want) {
				t.Errorf("%s utf8=%v:\n got %q\nwant %q", name, utf8, got, want)
			}
			if err := msg.Close(); err != nil {
				t.Fatal(err)
			}
			if err := msg.Close(); err != nil {
				t.Fatalf("a second Close: %v", err)
			}
			noSpoolLeft(t, dir)
		}
	}
}

// Only what Retrieve can make only from the whole message is held whole: a NUL, an
// 8-bit boundary or a message with no blank line for a session without UTF-8. Every
// other source is read from the spool file, with at most its downgraded header held,
// wherever the blank line falls.
func TestOpenMessageHoldsOnlyTheHeader(t *testing.T) {
	dir := spoolIn(t)
	body := strings.Repeat("0123456789abcdef\r\n", 1<<16)
	for _, at := range []int{100, retrSpoolChunk - 2, retrSpoolChunk, 3 * retrSpoolChunk} {
		for _, nl := range []string{"\r\n", "\n"} {
			for _, utf8 := range []bool{false, true} {
				head := "Subject: caf\xc3\xa9" + nl + "X-Pad: "
				head += strings.Repeat("p", at-len(head)) + nl + nl
				raw := head + body
				m := &mailbox{api: storeAPI(t, raw), token: "tok", utf8: utf8}
				msg, err := m.OpenMessage("7")
				if err != nil {
					t.Fatal(err)
				}
				spool := msg.(*spooledMessage)
				wantFromFile := int64(len(body))
				if utf8 {
					wantFromFile = int64(len(raw))
				}
				if spool.file == nil || len(spool.head) > len(head)+64 || spool.bodyLen != wantFromFile {
					t.Errorf("blank line at %d (%q) utf8=%v: holds %d octets, file %v, %d from the file", at, nl, utf8, len(spool.head), spool.file != nil, spool.bodyLen)
				}
				_ = msg.Close()
				noSpoolLeft(t, dir)
			}
		}
	}
	for name, raw := range map[string]string{
		"NUL":            "Subject: x\r\n\r\na\x00b\r\n",
		"8-bit boundary": "Subject: J\xc3\xb6\r\nContent-Type: multipart/mixed; boundary=\"\xc3\xb6\"\r\n\r\n--\xc3\xb6\r\n\r\nx\r\n--\xc3\xb6--\r\n",
		"no blank line":  "Subject: caf\xc3\xa9\r\n",
	} {
		m := &mailbox{api: storeAPI(t, raw), token: "tok"}
		msg, err := m.OpenMessage("7")
		if err != nil {
			t.Fatal(err)
		}
		if spool := msg.(*spooledMessage); spool.file != nil {
			t.Errorf("%s: streamed from the file, which Retrieve cannot do", name)
		}
		_ = msg.Close()
		noSpoolLeft(t, dir)
	}
}

// detailAPI answers message 7's detail, and its raw route with raw.
func detailAPI(t *testing.T, raw http.HandlerFunc) *apiclient.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/messages/7", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		d := sampleDetail(7)
		d.Subject = "Gr\u00fc\u00dfe"
		_ = json.NewEncoder(w).Encode(map[string]any{"data": d})
	})
	mux.HandleFunc("/api/v1/messages/7/raw", raw)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return apiclient.New(srv.URL, apiclient.WithMaxRawMessageBytes(1<<20))
}

// A message whose source the API does not send is rebuilt as Retrieve rebuilds it; a
// source over the read ceiling fails as Retrieve fails; a spool the gateway cannot
// create or write is Retrieve's message in memory. None leaves a file.
func TestOpenMessageFallsBackAsRetrieveDoes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     http.HandlerFunc
		disk    bool // the spool refuses writes
		noSpool bool // no spool can be made
		tooBig  bool
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
		{name: "no spool", noSpool: true, raw: func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "Subject: caf\xc3\xa9\r\n\r\nbody\r\n")
		}},
	} {
		for _, utf8 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/utf8=%v", tc.name, utf8), func(t *testing.T) {
				dir := spoolIn(t)
				switch {
				case tc.disk:
					createRetrSpool = func() (*os.File, error) {
						f, err := os.CreateTemp(dir, "pop3-retr-*")
						if err != nil {
							return nil, err
						}
						ro, err := os.Open(f.Name())
						_ = f.Close()
						return ro, err
					}
				case tc.noSpool:
					createRetrSpool = func() (*os.File, error) { return nil, errors.New("no temporary directory") }
				}
				m := &mailbox{api: detailAPI(t, tc.raw), token: "tok", utf8: utf8}
				want, wantErr := m.Retrieve("7")
				msg, err := m.OpenMessage("7")
				if tc.tooBig {
					if !errors.Is(err, apiclient.ErrRawMessageTooLarge) || !errors.Is(wantErr, apiclient.ErrRawMessageTooLarge) {
						t.Fatalf("OpenMessage %v, Retrieve %v; want ErrRawMessageTooLarge", err, wantErr)
					}
					noSpoolLeft(t, dir)
					return
				}
				if err != nil || wantErr != nil {
					t.Fatalf("OpenMessage %v, Retrieve %v", err, wantErr)
				}
				if got := readStored(t, msg); got != string(want) {
					t.Errorf("got %q, want %q", got, want)
				}
				_ = msg.Close()
				noSpoolLeft(t, dir)
			})
		}
	}
}

// A message the API cannot find, and a UID that is not one, fail as Retrieve fails.
func TestOpenMessageErrorsAreRetrieves(t *testing.T) {
	dir := spoolIn(t)
	m := &mailbox{api: storeAPI(t, "Subject: x\r\n\r\nbody\r\n"), token: "tok"}
	for _, uid := range []string{"8", "x"} {
		_, wantErr := m.Retrieve(uid)
		if _, err := m.OpenMessage(uid); err == nil || wantErr == nil {
			t.Fatalf("uid %q: OpenMessage %v, Retrieve %v; want both to fail", uid, err, wantErr)
		}
	}
	noSpoolLeft(t, dir)
}

// retrieveOnly hides OpenMessage, so the session is served from Retrieve as it was
// before the gateway streamed.
type retrieveOnly struct{ gopop3.Mailbox }

type retrieveOnlyBackend struct{ *Backend }

func (b retrieveOnlyBackend) Authenticate(user, pass string) (gopop3.Mailbox, error) {
	return b.AuthenticateUTF8(user, pass, false)
}

func (b retrieveOnlyBackend) AuthenticateUTF8(user, pass string, utf8 bool) (gopop3.Mailbox, error) {
	mb, err := b.Backend.AuthenticateUTF8(user, pass, utf8)
	if err != nil {
		return nil, err
	}
	return retrieveOnly{mb}, nil
}

// sessionTranscript is everything a session sends for LIST, RETR and TOP of message
// 7, as sent.
func sessionTranscript(t *testing.T, backend gopop3.Backend, utf8 bool) string {
	t.Helper()
	client, server := net.Pipe()
	sess := pop3server.NewSession(server, backend, nil, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	defer func() {
		_ = client.Close()
		<-done
	}()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReader(client)
	var b strings.Builder
	read := func(multi bool) {
		line, err := r.ReadString('\n')
		b.WriteString(line)
		if err != nil {
			t.Fatalf("read: %v (%q)", err, b.String())
		}
		if !multi || !strings.HasPrefix(line, "+OK") {
			return
		}
		for {
			next, err := r.ReadString('\n')
			b.WriteString(next)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if next == ".\r\n" {
				return
			}
		}
	}
	read(false) // greeting
	commands := []string{"USER jorg@example.test", "PASS pw", "STAT", "LIST", "RETR 1", "TOP 1 0", "TOP 1 1", "TOP 1 3", "TOP 1 1000", "RETR 1", "RETR 2"}
	if utf8 {
		commands = append([]string{"UTF8"}, commands...)
	}
	for _, command := range commands {
		fmt.Fprintf(client, "%s\r\n", command)
		fmt.Fprintf(&b, "C: %s\r\n", command)
		read(command == "LIST" || strings.HasPrefix(command, "RETR") || strings.HasPrefix(command, "TOP"))
	}
	return b.String()
}

// What a client sees of LIST, RETR and TOP, status lines and every octet of every
// line included, is the same streamed from the spool as served from Retrieve, for every
// shape, with UTF-8 and without (#2107).
func TestStreamedRetrSendsWhatRetrieveSent(t *testing.T) {
	dir := spoolIn(t)
	for name, raw := range retrStreamShapes() {
		for _, utf8 := range []bool{false, true} {
			api := storeAPI(t, raw)
			want := sessionTranscript(t, retrieveOnlyBackend{NewBackend(api, nil)}, utf8)
			got := sessionTranscript(t, NewBackend(api, nil), utf8)
			if got != want {
				t.Errorf("%s utf8=%v:\nstreamed %q\nwant     %q", name, utf8, got, want)
			}
			noSpoolLeft(t, dir)
		}
	}
}

// bigSource is a 7-bit message of about 9 MiB, with lines a RETR has to dot-stuff and
// bare LFs it has to canonicalize.
func bigSource() string {
	line := strings.Repeat("0123456789abcdef", 4) + "\r\n.dot-stuffed line\nbare LF line\n"
	var b strings.Builder
	b.WriteString("Subject: big\r\n\r\n")
	for b.Len() < 9<<20 {
		b.WriteString(line)
	}
	return b.String()
}

// retrThrough reads a RETR or TOP response through a small buffer and returns the octets
// of its lines, unstuffed, without the terminator.
func retrThrough(t *testing.T, r *bufio.Reader) int64 {
	t.Helper()
	var total int64
	for {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			total += int64(len(line))
			continue
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if bytes.Equal(line, []byte(".\r\n")) {
			return total
		}
		total += int64(len(bytes.TrimPrefix(line, []byte("."))))
	}
}

// RETR and TOP of a 9 MiB message through the gateway allocate a small fraction of it,
// API client and server included: the source goes from the API to a spool file and from
// the file to the client, and the file is gone after (#2107 AC01).
func TestGatewayRetrOfABigMessageHoldsLittleOfIt(t *testing.T) {
	dir := spoolIn(t)
	raw := bigSource()
	for _, utf8 := range []bool{false, true} {
		client, server := net.Pipe()
		sess := pop3server.NewSession(server, NewBackend(storeAPI(t, raw), nil), nil, nil)
		done := make(chan struct{})
		go func() {
			defer close(done)
			sess.Handle()
		}()
		_ = client.SetDeadline(time.Now().Add(60 * time.Second))
		r := bufio.NewReaderSize(client, 4096)
		c := &pop3Client{t: t, conn: client, r: r}
		c.line() // greeting
		if utf8 {
			c.do("UTF8")
		}
		c.do("USER jorg@example.test")
		c.do("PASS pw")
		var listed int64
		if _, err := fmt.Sscanf(c.do("LIST 1"), "+OK 1 %d", &listed); err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{"RETR 1", "TOP 1 10"} {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			c.do(command)
			n := retrThrough(t, r)
			runtime.ReadMemStats(&after)
			got := after.TotalAlloc - before.TotalAlloc
			t.Logf("utf8=%v: %s of %d octets sent %d and allocated %d", utf8, command, len(raw), n, got)
			if command == "RETR 1" && n != listed {
				t.Fatalf("RETR sent %d octets, LIST says %d", n, listed)
			}
			if got > uint64(len(raw))/8 {
				t.Errorf("utf8=%v: %s of %d octets allocated %d", utf8, command, len(raw), got)
			}
			c.do("NOOP") // the response is over, and the message closed
			noSpoolLeft(t, dir)
		}
		_ = client.Close()
		<-done
	}
}

// A client that leaves in the middle of a RETR leaves no spool file.
func TestGatewayRetrSpoolGoesWhenTheClientLeaves(t *testing.T) {
	dir := spoolIn(t)
	client, server := net.Pipe()
	sess := pop3server.NewSession(server, NewBackend(storeAPI(t, bigSource()), nil), nil, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	_ = client.SetDeadline(time.Now().Add(60 * time.Second))
	r := bufio.NewReader(client)
	c := &pop3Client{t: t, conn: client, r: r}
	c.line()
	c.do("USER jorg@example.test")
	c.do("PASS pw")
	c.do("RETR 1")
	if _, err := io.CopyN(io.Discard, r, 1<<20); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	<-done
	noSpoolLeft(t, dir)
}

// A RETR the API refuses, over the read ceiling, is answered as Retrieve's refusal was,
// and leaves no spool file.
func TestGatewayRetrOverTheCeilingLeavesNoSpool(t *testing.T) {
	dir := spoolIn(t)
	raw := "Subject: x\r\n\r\n" + strings.Repeat("x", 4096)
	for _, backend := range []func(*apiclient.Client) gopop3.Backend{
		func(api *apiclient.Client) gopop3.Backend { return NewBackend(api, nil) },
		func(api *apiclient.Client) gopop3.Backend { return retrieveOnlyBackend{NewBackend(api, nil)} },
	} {
		got := sessionTranscript(t, backend(storeAPI(t, raw, apiclient.WithMaxRawMessageBytes(100))), false)
		if !strings.Contains(got, "C: RETR 1\r\n-ERR Failed to retrieve message\r\n") {
			t.Fatalf("over the ceiling: %q", got)
		}
		noSpoolLeft(t, dir)
	}
}
