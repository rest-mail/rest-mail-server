package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// seenByFilters is a filter that writes down what the pipeline hands every filter after it.
type seenByFilters struct {
	mu    sync.Mutex
	email *pipeline.EmailJSON
}

func (s *seenByFilters) Name() string              { return "seen_by_filters" }
func (s *seenByFilters) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (s *seenByFilters) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.email = email
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionContinue}, nil
}

func (s *seenByFilters) describe() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.email == nil {
		return "(no filter ran)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "tree=%s", describeBody(s.email.Body))
	for _, attachment := range s.email.Attachments {
		fmt.Fprintf(&b, " attachment=%q(%s)", attachment.Filename, attachment.ContentType)
	}
	return b.String()
}

func describeBody(body pipeline.Body) string {
	if len(body.Parts) == 0 {
		return fmt.Sprintf("%s:%q", body.ContentType, body.Content)
	}
	parts := make([]string, 0, len(body.Parts))
	for _, part := range body.Parts {
		parts = append(parts, describeBody(part))
	}
	return body.ContentType + "[" + strings.Join(parts, " ") + "]"
}

// imapSession is a client of the real IMAP gateway, signed in and with INBOX open.
type imapSession struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	next int
}

func openIMAPSession(t *testing.T, f *credentialFixture, api *apiclient.Client) *imapSession {
	t.Helper()
	client, server := net.Pipe()
	session := imapserver.NewSession(server, imapgw.NewBackend(api, nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	if err := client.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		t.Fatal(err)
	}
	s := &imapSession{t: t, conn: client, r: bufio.NewReader(client)}
	s.line() // the greeting
	if answer := s.do(fmt.Sprintf("LOGIN %s %s", f.address, f.password)); !strings.Contains(answer, " OK") {
		t.Fatalf("LOGIN: %s", answer)
	}
	if answer := s.do("SELECT INBOX"); !strings.Contains(answer, " OK") {
		t.Fatalf("SELECT: %s", answer)
	}
	return s
}

// line is one response line, with the literal it announces.
func (s *imapSession) line() string {
	s.t.Helper()
	line, err := s.r.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read: %v", err)
	}
	if at := strings.LastIndex(line, "{"); at >= 0 && strings.HasSuffix(line, "}\r\n") {
		if size, err := strconv.Atoi(line[at+1 : len(line)-3]); err == nil {
			literal := make([]byte, size)
			if _, err := io.ReadFull(s.r, literal); err != nil {
				s.t.Fatalf("literal: %v", err)
			}
			return line + string(literal) + s.line()
		}
	}
	return line
}

func (s *imapSession) do(command string) string {
	s.t.Helper()
	s.next++
	tag := fmt.Sprintf("t%d", s.next)
	if _, err := fmt.Fprintf(s.conn, "%s %s\r\n", tag, command); err != nil {
		s.t.Fatalf("send: %v", err)
	}
	var b strings.Builder
	for {
		line := s.line()
		b.WriteString(line)
		if strings.HasPrefix(line, tag+" ") {
			return b.String()
		}
	}
}

const repeatedHead = "From: sender@remote.test\r\nTo: chris@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nMIME-Version: 1.0\r\n"

// A sender can repeat a parameter's name, and RFC 2045 §5.1 has no rule against it. Whatever is read
// from that has to be one answer: the first value, for the row, the attachment list and the filters
// as for BODYSTRUCTURE and BODY[n], or a filter is shown one message and a mail client another.
func TestRepeatedParameterReadsTheSameEverywhere(t *testing.T) {
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize})
	var domain models.Domain
	if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	seen := &seenByFilters{}
	pipeline.DefaultRegistry.Register("seen_by_filters", func([]byte) (pipeline.Filter, error) { return seen, nil })
	filters := fmt.Sprintf(`[{"name":"seen_by_filters","type":"action","enabled":true},{"name":"extract_attachments","type":"transform","enabled":true,"config":{"storage_path":%q}}]`, t.TempDir())
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(filters), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	imap := openIMAPSession(t, f, apiclient.New(srv.URL))
	many := func(field string) string {
		var b strings.Builder
		for i := range 20 {
			fmt.Fprintf(&b, "; p%d=v", i)
		}
		return field + b.String()
	}
	mixed := func(top, part string) string {
		return repeatedHead + "Content-Type: multipart/mixed; " + top + "\r\n\r\n--a\r\nContent-Type: text/plain\r\n\r\nvisible text\r\n--a\r\n" + part + "\r\n\r\nATTACHED\r\n--a--\r\n"
	}
	const octets, attachedPart = "Content-Type: application/octet-stream", "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=a.txt"
	const attachedRow, attachedSeen = `"a.txt"(application/octet-stream)`, `tree=multipart/mixed[text/plain:"visible text"] attachment="a.txt"(application/octet-stream)`
	const attachedFilename = `("ATTACHMENT" ("FILENAME" "a.txt"`
	for _, row := range []struct {
		name, message string
		// What each reader has of it: the row's text, the attachment rows, the filters, a piece of
		// BODYSTRUCTURE, and the text of BODY[2], or BODY[1] when there's only that.
		text, attachments, filters, structure, section string
	}{
		{"boundary", mixed("boundary=a; boundary=b", attachedPart), "visible text", attachedRow, attachedSeen, `"MIXED" ("BOUNDARY" "a")`, "ATTACHED"},
		{"boundary, read a parameter at a time", mixed(many("boundary=a; boundary=b"), attachedPart), "visible text", attachedRow, attachedSeen, `"MIXED" ("BOUNDARY" "a" "P0"`, "ATTACHED"},
		{"boundary, spelled in capitals", mixed("BOUNDARY=a; boundary=b", attachedPart), "visible text", attachedRow, attachedSeen, `"MIXED" ("BOUNDARY" "a")`, "ATTACHED"},
		{"another name on the multipart", mixed("boundary=a; x=1; x=2", attachedPart), "visible text", attachedRow, attachedSeen, `"MIXED" ("BOUNDARY" "a" "X" "1")`, "ATTACHED"},
		{"filename", mixed("boundary=a", octets+"\r\nContent-Disposition: attachment; filename=a.txt; filename=b.exe"), "visible text", attachedRow, attachedSeen, attachedFilename, "ATTACHED"},
		{"filename, read a parameter at a time", mixed("boundary=a", octets+"\r\nContent-Disposition: "+many("attachment; filename=a.txt; filename=b.exe")), "visible text", attachedRow, attachedSeen, attachedFilename, "ATTACHED"},
		{"filename*", mixed("boundary=a", octets+"\r\nContent-Disposition: attachment; filename*=utf-8''a.txt; filename*=utf-8''b.exe"), "visible text", attachedRow, attachedSeen, attachedFilename, "ATTACHED"},
		{"filename*, read a parameter at a time", mixed("boundary=a", octets+"\r\nContent-Disposition: "+many("attachment; filename*=utf-8''a.txt; filename*=utf-8''b.exe")), "visible text", attachedRow, attachedSeen, attachedFilename, "ATTACHED"},
		{"filename*0", mixed("boundary=a", octets+"\r\nContent-Disposition: attachment; filename*0=a; filename*0=x; filename*1=.txt"), "visible text", attachedRow, attachedSeen, attachedFilename, "ATTACHED"},
		{"a blank, then only a repeat", mixed("boundary=a", octets+"\r\nContent-Disposition: attachment; filename=a.txt;; filename=b.exe"), "visible text", attachedRow, attachedSeen, attachedFilename, "ATTACHED"},
		{"name", mixed("boundary=a", octets+"; name=a.txt; name=b.exe"), "visible text", attachedRow, attachedSeen, `"OCTET-STREAM" ("NAME" "a.txt")`, "ATTACHED"},
		{"name on a text part", mixed("boundary=a", "Content-Type: text/plain; name=a.txt; name=b.exe"), "visible text", "", `tree=multipart/mixed[text/plain:"visible text" text/plain:"ATTACHED"]`, `"NAME" "a.txt")`, "ATTACHED"},
		{"another name on the disposition", mixed("boundary=a", octets+"\r\nContent-Disposition: attachment; x=1; x=2"), "visible text", `""(application/octet-stream)`, `tree=multipart/mixed[text/plain:"visible text"] attachment=""(application/octet-stream)`, `("ATTACHMENT" ("X" "1"))`, "ATTACHED"},
		{"charset", repeatedHead + "Content-Type: text/plain; charset=iso-8859-1; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\ncaf\xc3\xa9\r\n", "caf\u00c3\u00a9", "", "tree=text/plain:\"caf\u00c3\u00a9\\r\\n\"", `"CHARSET" "iso-8859-1"`, "caf\xc3\xa9"},
		{"charset on a part", mixed("boundary=a", "Content-Type: text/plain; charset=iso-8859-1; charset=utf-8\r\nContent-Transfer-Encoding: 8bit"), "visible text", "", `tree=multipart/mixed[text/plain:"visible text" text/plain:"ATTACHED"]`, `"CHARSET" "iso-8859-1"`, "ATTACHED"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.Message{}).Error; err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{"address": f.address, "sender": "sender@remote.test", "recipients_to": []string{f.address},
				"client_ip": "192.0.2.1", "helo_name": "mail.remote.test", "received_tls": true, "raw_message": base64.StdEncoding.EncodeToString([]byte(row.message))})
			if err != nil {
				t.Fatal(err)
			}
			if answer := f.do(http.MethodPost, "/api/v1/messages/deliver", "", string(body)); answer.Code != http.StatusCreated {
				t.Fatalf("refused: %d %s", answer.Code, answer.Body)
			}
			// The bytes are stored as they came, whatever a reader makes of them.
			stored := lastStored(t, f, row.message)
			var rows []models.Attachment
			if err := f.db.Where("message_id = ?", stored.ID).Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			var attachments []string
			for _, attachment := range rows {
				attachments = append(attachments, fmt.Sprintf("%q(%s)", attachment.Filename, attachment.ContentType))
			}
			if got := strings.TrimSpace(stored.BodyText); got != row.text {
				t.Errorf("row's text %q, want %q", got, row.text)
			}
			if got := strings.Join(attachments, " "); got != row.attachments {
				t.Errorf("attachment rows %s, want %s", got, row.attachments)
			}
			if got := seen.describe(); got != row.filters {
				t.Errorf("filters were handed %s, want %s", got, row.filters)
			}
			imap.do("SELECT INBOX")
			structure := imap.do("FETCH 1 (BODYSTRUCTURE)")
			if !strings.Contains(structure, row.structure) {
				t.Errorf("BODYSTRUCTURE %s, want %s in it", structure, row.structure)
			}
			section := "2"
			if !strings.Contains(row.message, "--a") {
				section = "1"
			}
			if got := imap.do("FETCH 1 (BODY.PEEK[" + section + "])"); !strings.Contains(got, row.section) {
				t.Errorf("BODY[%s] %s, want %q in it", section, got, row.section)
			}
		})
	}
}
