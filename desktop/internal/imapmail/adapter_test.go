package imapmail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	keyring "github.com/zalando/go-keyring"
)

const testPassword = "imap-password-never-expose-7C4"

func testTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

type literal struct{ *bytes.Reader }

func (value literal) Size() int64 { return int64(value.Len()) }

type imapFixture struct {
	port      int
	user      *imapmemserver.User
	idleReady chan struct{}
}

type observedSession struct {
	imapserver.Session
	idleReady chan struct{}
}

func (observed *observedSession) Idle(writer *imapserver.UpdateWriter, stop <-chan struct{}) error {
	select {
	case observed.idleReady <- struct{}{}:
	default:
	}
	return observed.Session.Idle(writer, stop)
}
func (observed *observedSession) Move(writer *imapserver.MoveWriter, set imap.NumSet, destination string) error {
	return observed.Session.(imapserver.SessionMove).Move(writer, set, destination)
}

func startIMAP(t *testing.T, config *tls.Config, implicit bool, idle bool) *imapFixture {
	t.Helper()
	user := imapmemserver.NewUser("reader", testPassword)
	for _, name := range []string{"INBOX", "Archive", "Trash"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	backend := imapmemserver.New()
	backend.AddUser(user)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &imapFixture{port: listener.Addr().(*net.TCPAddr).Port, user: user, idleReady: make(chan struct{}, 5)}
	if implicit {
		listener = tls.NewListener(listener, config)
	}
	caps := imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}}
	if idle {
		caps[imap.CapIdle] = struct{}{}
	}
	server := imapserver.New(&imapserver.Options{TLSConfig: config, Caps: caps, NewSession: func(_ *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return &observedSession{Session: backend.NewSession(), idleReady: fixture.idleReady}, nil, nil
	}})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = listener.Close(); _ = server.Close() })
	return fixture
}

func appendMail(t *testing.T, fixture *imapFixture, subject string, attachment bool) {
	t.Helper()
	raw := "From: Sender <sender@example.test>\r\nTo: reader@example.test\r\nSubject: " + subject + "\r\nMessage-ID: <" + subject + "@example.test>\r\nDate: Sat, 21 Nov 2026 12:00:00 +0000\r\nMIME-Version: 1.0\r\n"
	if attachment {
		raw += "Content-Type: multipart/mixed; boundary=parts\r\n\r\n--parts\r\nContent-Type: multipart/alternative; boundary=alt\r\n\r\n--alt\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello plain\r\n--alt\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<b>Hello HTML</b>\r\n--alt--\r\n--parts\r\nContent-Type: text/plain; name=hello.txt\r\nContent-Disposition: attachment; filename=hello.txt\r\nContent-Transfer-Encoding: base64\r\n\r\nSGVsbG8gYXR0YWNobWVudA==\r\n--parts--\r\n"
	} else {
		raw += "Content-Type: text/plain\r\n\r\nBody " + subject + "\r\n"
	}
	if _, err := fixture.user.Append("INBOX", literal{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

type smtpFixture struct {
	port          int
	messages      chan []byte
	mu            sync.Mutex
	authenticated int
	plainAuth     bool
}

func startSMTP(t *testing.T, config *tls.Config, implicit, login bool) *smtpFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &smtpFixture{port: listener.Addr().(*net.TCPAddr).Port, messages: make(chan []byte, 10)}
	if implicit {
		listener = tls.NewListener(listener, config)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() { cancel(); _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go fixture.serve(ctx, conn, config, implicit, login)
		}
	}()
	return fixture
}

func (fixture *smtpFixture) serve(ctx context.Context, conn net.Conn, config *tls.Config, secure, login bool) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	protocol := textproto.NewConn(conn)
	_ = protocol.PrintfLine("220 local SMTP")
	authed := false
	for {
		line, err := protocol.ReadLine()
		if err != nil {
			return
		}
		command, argument, _ := strings.Cut(line, " ")
		switch strings.ToUpper(command) {
		case "EHLO":
			_ = protocol.PrintfLine("250-localhost")
			if !secure && config != nil {
				_ = protocol.PrintfLine("250-STARTTLS")
			}
			mechanism := "PLAIN"
			if login {
				mechanism = "LOGIN"
			}
			_ = protocol.PrintfLine("250 AUTH %s", mechanism)
		case "STARTTLS":
			if config == nil {
				_ = protocol.PrintfLine("454 TLS unavailable")
				continue
			}
			_ = protocol.PrintfLine("220 Begin TLS")
			upgraded := tls.Server(conn, config)
			if upgraded.Handshake() != nil {
				return
			}
			protocol = textproto.NewConn(upgraded)
			secure = true
		case "AUTH":
			fixture.mu.Lock()
			fixture.plainAuth = fixture.plainAuth || !secure
			fixture.mu.Unlock()
			if !secure {
				_ = protocol.PrintfLine("538 TLS required")
				continue
			}
			valid := false
			if login {
				_ = protocol.PrintfLine("334 VXNlcm5hbWU6")
				username, _ := protocol.ReadLine()
				_ = protocol.PrintfLine("334 UGFzc3dvcmQ6")
				password, _ := protocol.ReadLine()
				decodedUser, _ := base64.StdEncoding.DecodeString(username)
				decodedPassword, _ := base64.StdEncoding.DecodeString(password)
				valid = string(decodedUser) == "reader" && string(decodedPassword) == testPassword
			} else {
				_, encoded, _ := strings.Cut(argument, " ")
				decoded, _ := base64.StdEncoding.DecodeString(encoded)
				valid = string(decoded) == "\x00reader\x00"+testPassword
			}
			if !valid {
				_ = protocol.PrintfLine("535 authentication refused")
				continue
			}
			authed = true
			fixture.mu.Lock()
			fixture.authenticated++
			fixture.mu.Unlock()
			_ = protocol.PrintfLine("235 authenticated")
		case "MAIL", "RCPT":
			if !authed || !secure {
				_ = protocol.PrintfLine("530 authenticate first")
			} else {
				_ = protocol.PrintfLine("250 accepted")
			}
		case "DATA":
			if !authed || !secure {
				_ = protocol.PrintfLine("530 authenticate first")
				continue
			}
			_ = protocol.PrintfLine("354 send message")
			data, err := io.ReadAll(protocol.DotReader())
			if err != nil {
				return
			}
			fixture.messages <- data
			_ = protocol.PrintfLine("250 queued")
		case "QUIT":
			_ = protocol.PrintfLine("221 goodbye")
			return
		default:
			_ = protocol.PrintfLine("250 okay")
		}
	}
}

func setupStore(t *testing.T, implicit, login bool) (*Store, Account, *imapFixture, *smtpFixture) {
	t.Helper()
	keyring.MockInit()
	serverTLS, clientTLS := testTLS(t)
	imapServer := startIMAP(t, serverTLS, implicit, true)
	smtpServer := startSMTP(t, serverTLS, implicit, login)
	store, err := New()
	if err != nil {
		t.Fatal(err)
	}
	store.tlsConfig = clientTLS
	t.Cleanup(store.Close)
	security := "starttls"
	if implicit {
		security = "tls"
	}
	account, err := store.AddIMAPAccount(t.Context(), AccountInput{Host: "127.0.0.1", Port: imapServer.port, Username: "reader", Password: testPassword, SMTPHost: "127.0.0.1", SMTPPort: smtpServer.port, Address: "reader@example.test", IMAPSecurity: security, SMTPSecurity: security})
	if err != nil {
		t.Fatal(err)
	}
	return store, account, imapServer, smtpServer
}

func requestAdapter(t *testing.T, store *Store, method, path string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, "/api/v1"+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	response, handled, err := store.Handle(request)
	if err != nil || !handled {
		t.Fatalf("adapter handled=%v err=%v", handled, err)
	}
	return response
}

func responseData(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("HTTP %d: %s", response.StatusCode, data)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func TestIMAPAdapterTranslations(t *testing.T) {
	for _, implicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("implicit_TLS_%t", implicit), func(t *testing.T) {
			store, account, fixture, _ := setupStore(t, implicit, false)
			appendMail(t, fixture, "first", false)
			appendMail(t, fixture, "attachment", true)
			appendMail(t, fixture, "third", false)
			var folders struct {
				Data []apitypes.FolderSummary `json:"data"`
			}
			responseData(t, requestAdapter(t, store, "GET", fmt.Sprintf("/accounts/%d/folders", account.ID), nil), &folders)
			if len(folders.Data) != 3 {
				t.Fatalf("folders=%+v", folders.Data)
			}
			var page apiclient.MessagePage
			responseData(t, requestAdapter(t, store, "GET", fmt.Sprintf("/accounts/%d/folders/INBOX/messages?limit=2", account.ID), nil), &page)
			if len(page.Data) != 2 || page.Data[0].Subject != "third" || !page.Pagination.HasMore {
				t.Fatalf("page=%+v", page)
			}
			attachmentID := page.Data[1].ID
			if attachmentID < IDBase || attachmentID >= 2*IDBase {
				t.Fatalf("unsafe JS message ID %d", attachmentID)
			}
			var next apiclient.MessagePage
			responseData(t, requestAdapter(t, store, "GET", fmt.Sprintf("/accounts/%d/folders/INBOX/messages?limit=2&cursor=%s", account.ID, page.Pagination.Cursor), nil), &next)
			if len(next.Data) != 1 || next.Data[0].Subject != "first" || next.Pagination.HasMore {
				t.Fatalf("next=%+v", next)
			}
			var detail struct {
				Data apitypes.Message `json:"data"`
			}
			responseData(t, requestAdapter(t, store, "GET", fmt.Sprintf("/messages/%d", attachmentID), nil), &detail)
			if !strings.Contains(detail.Data.BodyText, "Hello plain") || !strings.Contains(detail.Data.BodyHTML, "Hello HTML") || !detail.Data.HasAttachments || detail.Data.IsRead || !strings.Contains(strings.ToLower(string(detail.Data.Headers)), "message-id") {
				t.Fatalf("detail=%+v", detail.Data)
			}
			var attachments struct {
				Data []Attachment `json:"data"`
			}
			responseData(t, requestAdapter(t, store, "GET", fmt.Sprintf("/messages/%d/attachments", attachmentID), nil), &attachments)
			if len(attachments.Data) != 1 || attachments.Data[0].Filename != "hello.txt" {
				t.Fatalf("attachments=%+v", attachments.Data)
			}
			download := requestAdapter(t, store, "GET", fmt.Sprintf("/attachments/%d", attachments.Data[0].ID), nil)
			data, err := io.ReadAll(download.Body)
			download.Body.Close()
			if err != nil || string(data) != "Hello attachment" {
				t.Fatalf("download=%q err=%v", data, err)
			}
			responseData(t, requestAdapter(t, store, "PATCH", fmt.Sprintf("/messages/%d", attachmentID), map[string]bool{"is_read": true, "is_flagged": true}), &map[string]any{})
			responseData(t, requestAdapter(t, store, "GET", fmt.Sprintf("/messages/%d", attachmentID), nil), &detail)
			if !detail.Data.IsRead || !detail.Data.IsFlagged || !detail.Data.IsStarred {
				t.Fatal("flags not translated")
			}
			var search apiclient.MessagePage
			responseData(t, requestAdapter(t, store, "GET", fmt.Sprintf("/accounts/%d/search?q=attachment", account.ID), nil), &search)
			if len(search.Data) != 1 || search.Data[0].ID != attachmentID {
				t.Fatalf("search=%+v", search)
			}
			responseData(t, requestAdapter(t, store, "PATCH", fmt.Sprintf("/messages/%d", attachmentID), map[string]string{"folder": "Archive"}), &map[string]any{})
			moved, err := store.ListMessages(t.Context(), account.ID, "Archive", apiclient.Page{}, "")
			if err != nil || len(moved.Data) != 1 {
				t.Fatalf("move=%+v err=%v", moved, err)
			}
			responseData(t, requestAdapter(t, store, "DELETE", fmt.Sprintf("/messages/%d", moved.Data[0].ID), nil), &map[string]any{})
			trash, err := store.ListMessages(t.Context(), account.ID, "Trash", apiclient.Page{}, "")
			if err != nil || len(trash.Data) != 1 {
				t.Fatalf("trash=%+v err=%v", trash, err)
			}
			responseData(t, requestAdapter(t, store, "DELETE", fmt.Sprintf("/messages/%d?permanent=true", trash.Data[0].ID), nil), &map[string]any{})
			trash, err = store.ListMessages(t.Context(), account.ID, "Trash", apiclient.Page{}, "")
			if err != nil || len(trash.Data) != 0 {
				t.Fatalf("delete=%+v err=%v", trash, err)
			}
		})
	}
}

func TestSMTPSendRequiresTLS(t *testing.T) {
	for _, implicit := range []bool{false, true} {
		for _, login := range []bool{false, true} {
			t.Run(fmt.Sprintf("implicit_%t_LOGIN_%t", implicit, login), func(t *testing.T) {
				store, account, _, fixture := setupStore(t, implicit, login)
				responseData(t, requestAdapter(t, store, "POST", "/messages/send", apitypes.SendMessageRequest{From: account.Address, To: []string{"recipient@example.test"}, Bcc: []string{"private@example.test"}, Subject: "TLS delivery", BodyText: "plain", BodyHTML: "<b>HTML</b>"}), &map[string]any{})
				select {
				case raw := <-fixture.messages:
					if !strings.Contains(string(raw), "Subject: TLS delivery") || strings.Contains(string(raw), testPassword) || strings.Contains(string(raw), "Bcc:") {
						t.Fatal("incorrect outgoing message")
					}
				case <-time.After(time.Second):
					t.Fatal("SMTP did not receive message")
				}
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				if fixture.plainAuth || fixture.authenticated != 2 {
					t.Fatalf("unsafe auth=%v authentications=%d", fixture.plainAuth, fixture.authenticated)
				}
			})
		}
	}
}

func TestIMAPRejectsPlaintextAndUntrustedCertificates(t *testing.T) {
	keyring.MockInit()
	store, err := New()
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, _ := testTLS(t)
	for _, test := range []struct {
		name     string
		config   *tls.Config
		implicit bool
	}{{"plaintext", nil, false}, {"untrusted_STARTTLS", serverTLS, false}, {"untrusted_implicit", serverTLS, true}} {
		t.Run(test.name, func(t *testing.T) {
			fixture := startIMAP(t, test.config, test.implicit, false)
			security := "starttls"
			if test.implicit {
				security = "tls"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			_, err := store.AddIMAPAccount(ctx, AccountInput{Host: "127.0.0.1", Port: fixture.port, Username: "reader", Password: testPassword, SMTPHost: "127.0.0.1", SMTPPort: 587, Address: "reader@example.test", IMAPSecurity: security})
			if err == nil || strings.Contains(err.Error(), testPassword) || len(store.ListAccounts()) != 0 {
				t.Fatalf("unsafe account acceptance: %v", err)
			}
		})
	}
	if _, err := validate(AccountInput{Host: "mail.test", Username: "reader", Password: testPassword, SMTPHost: "mail.test", Address: "reader@example.test", IMAPSecurity: "none"}); err == nil {
		t.Fatal("plaintext config accepted")
	}
}

func TestSMTPRejectsPlaintextAndUntrustedCertificates(t *testing.T) {
	keyring.MockInit()
	store, err := New()
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, clientTLS := testTLS(t)
	for _, test := range []struct {
		name            string
		config          *tls.Config
		implicit, trust bool
	}{{"plaintext", nil, false, true}, {"untrusted_STARTTLS", serverTLS, false, false}, {"untrusted_implicit", serverTLS, true, false}} {
		t.Run(test.name, func(t *testing.T) {
			fixture := startSMTP(t, test.config, test.implicit, false)
			store.tlsConfig = nil
			if test.trust {
				store.tlsConfig = clientTLS
			}
			security := "starttls"
			if test.implicit {
				security = "tls"
			}
			if store.checkSMTP(t.Context(), Account{SMTPHost: "127.0.0.1", SMTPPort: fixture.port, SMTPSecurity: security, Username: "reader"}, testPassword) == nil {
				t.Fatal("insecure SMTP accepted")
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.authenticated != 0 || fixture.plainAuth {
				t.Fatal("credentials sent before verified TLS")
			}
		})
	}
}

func TestAccountKeychainIdentityAndCredentialBoundary(t *testing.T) {
	store, account, fixture, _ := setupStore(t, false, false)
	appendMail(t, fixture, "first", false)
	page, err := store.ListMessages(t.Context(), account.ID, "INBOX", apiclient.Page{}, "")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := keyring.Get(keychainService, metadataKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, testPassword) || strings.ContainsAny(passwordKey(account.ID), "\r\n") {
		t.Fatal("unsafe credential metadata")
	}
	encoded, _ := json.Marshal(store.ListAccounts())
	if strings.Contains(string(encoded), "password") {
		t.Fatal("password field returned to JS")
	}
	restored, err := New()
	if err != nil {
		t.Fatal(err)
	}
	restored.tlsConfig = store.tlsConfig
	t.Cleanup(restored.Close)
	message, _, _, err := restored.GetMessage(t.Context(), page.Data[0].ID)
	if err != nil || message.ID != page.Data[0].ID {
		t.Fatalf("message identity not stable across restart: %v", err)
	}
	if strings.Contains(string(restored.Redact([]byte(testPassword))), testPassword) {
		t.Fatal("restored password not redacted")
	}
	response := store.jsonResponse(http.StatusOK, map[string]string{"subject": testPassword, "text": "a " + testPassword + " b"})
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(data), testPassword) {
		t.Fatal("password in proxy JSON")
	}
	if err := store.RemoveIMAPAccount(account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keychainService, passwordKey(account.ID)); err != keyring.ErrNotFound {
		t.Fatalf("password remains in keychain: %v", err)
	}
	if len(store.ListAccounts()) != 0 {
		t.Fatal("account remains")
	}
	if strings.Contains(string(store.Redact([]byte(testPassword))), testPassword) {
		t.Fatal("removed password no longer redacted")
	}
	if _, uid, ok := store.resolve(page.Data[0].ID); !ok || strconv.FormatUint(uint64(uid), 10) != "1" {
		t.Fatal("message identity malformed")
	}
}

func TestIMAPReconnectSerializesCommandsAndKeepsUIDIdentity(t *testing.T) {
	store, account, fixture, _ := setupStore(t, false, false)
	appendMail(t, fixture, "first", false)
	first, err := store.ListMessages(t.Context(), account.ID, "INBOX", apiclient.Page{}, "")
	if err != nil {
		t.Fatal(err)
	}
	state := store.connections[account.ID]
	state.mu.Lock()
	_ = state.conn.Close()
	state.mu.Unlock()
	var workers sync.WaitGroup
	errors := make(chan error, 12)
	for index := 0; index < 12; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, err := store.ListMessages(t.Context(), account.ID, "INBOX", apiclient.Page{}, "")
			if err == nil && (len(result.Data) != 1 || result.Data[0].ID != first.Data[0].ID) {
				err = fmt.Errorf("reconnected message ID changed")
			}
			errors <- err
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ListMessages(t.Context(), account.ID, "INBOX", apiclient.Page{Cursor: "not-a-cursor"}, ""); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	if err := store.Folder(t.Context(), account.ID, "POST", "Nested/Name", ""); err != nil {
		t.Fatal(err)
	}
	var page apiclient.MessagePage
	responseData(t, requestAdapter(t, store, "GET", fmt.Sprintf("/accounts/%d/folders/Nested%%2FName/messages", account.ID), nil), &page)
	if len(page.Data) != 0 {
		t.Fatal("wrong nested folder")
	}
}

func TestIMAPIDLEFeedsExistingSSEAndClosesOnSignOut(t *testing.T) {
	store, account, fixture, _ := setupStore(t, false, false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("/api/v1/accounts/%d/events", account.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, handled, err := store.Handle(request)
	if err != nil || !handled {
		t.Fatal("event route not handled")
	}
	defer response.Body.Close()
	lines := make(chan string, 20)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	select {
	case <-fixture.idleReady:
	case <-ctx.Done():
		t.Fatal("server did not receive IDLE")
	}
	appendMail(t, fixture, "notification", false)
	newMessage := false
	sender := false
	for !newMessage || !sender {
		select {
		case line := <-lines:
			if line == "event: new_message" {
				newMessage = true
			}
			if strings.Contains(line, "sender@example.test") && strings.Contains(line, "notification") {
				sender = true
			}
			if strings.Contains(line, testPassword) {
				t.Fatal("password in SSE")
			}
		case <-ctx.Done():
			t.Fatal("IMAP IDLE did not reach existing new_message SSE path")
		}
	}
	store.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("sign out left SSE open")
	}
}

func TestIMAPSearchPagingAndSafePermanentDelete(t *testing.T) {
	store, account, fixture, _ := setupStore(t, false, false)
	for index := 0; index < 5; index++ {
		appendMail(t, fixture, fmt.Sprintf("match-%d", index), false)
	}
	page, err := store.Search(t.Context(), account.ID, "match", "", apiclient.Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 2 || !page.Pagination.HasMore {
		t.Fatalf("first search page=%+v", page)
	}
	next, err := store.Search(t.Context(), account.ID, "match", "", apiclient.Page{Limit: 2, Cursor: page.Pagination.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Data) != 2 || page.Data[1].ID == next.Data[0].ID {
		t.Fatal("search pagination repeated or omitted page")
	}
	deleted := true
	if err := store.UpdateMessage(t.Context(), page.Data[0].ID, apitypes.UpdateMessageBody{IMAPDeleted: &deleted}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMessage(t.Context(), page.Data[1].ID, true); err != nil {
		t.Fatal(err)
	}
	message, _, _, err := store.GetMessage(t.Context(), page.Data[0].ID)
	if err != nil || message.ID != page.Data[0].ID {
		t.Fatal("UID EXPUNGE deleted an unrelated message")
	}
}

func TestReaddingAccountReconnectsWithoutChangingIdentity(t *testing.T) {
	store, account, _, _ := setupStore(t, false, false)
	store.Close()
	reconnected, err := store.AddIMAPAccount(t.Context(), AccountInput{Host: account.Host, Port: account.Port, Username: account.Username, Password: testPassword, SMTPHost: account.SMTPHost, SMTPPort: account.SMTPPort, Address: account.Address, IMAPSecurity: account.IMAPSecurity, SMTPSecurity: account.SMTPSecurity})
	if err != nil || reconnected.ID != account.ID || len(store.ListAccounts()) != 1 {
		t.Fatalf("reauth changed identity or duplicated account: %v", err)
	}
}
