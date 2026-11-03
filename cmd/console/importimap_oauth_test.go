package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/mailimport"
)

const fakeIMAPAccessToken = "FAKE-import-oauth-marker-NEVER-LOG"

type oauthImportFixture struct {
	mechanisms            []string
	mode                  string
	auth, login, examined atomic.Int32
}

type oauthImportSession struct {
	*imapmemserver.UserSession
	fixture *oauthImportFixture
}

func (session *oauthImportSession) Login(string, string) error {
	session.fixture.login.Add(1)
	return imapserver.ErrAuthFailed
}

func (session *oauthImportSession) AuthenticateMechanisms() []string {
	return session.fixture.mechanisms
}

func (session *oauthImportSession) Authenticate(mechanism string) (sasl.Server, error) {
	session.fixture.auth.Add(1)
	if mechanism != session.fixture.mechanisms[0] {
		return nil, imapserver.ErrAuthFailed
	}
	return &oauthImportSASL{mechanism: mechanism, mode: session.fixture.mode}, nil
}

func (session *oauthImportSession) Select(name string, options *imap.SelectOptions) (*imap.SelectData, error) {
	if session.fixture.examined.Add(1) > 1 && session.fixture.mode == "expired" {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "expired " + fakeIMAPAccessToken}
	}
	return session.UserSession.Select(name, options)
}

type oauthImportSASL struct {
	mechanism, mode string
	challenged      bool
}

func (auth *oauthImportSASL) Next(response []byte) ([]byte, bool, error) {
	if response == nil {
		return []byte{}, false, nil
	}
	if auth.challenged {
		if len(response) != 0 {
			return nil, true, imapserver.ErrAuthFailed
		}
		return nil, true, &imap.Error{Type: imap.StatusResponseTypeNo, Text: fakeIMAPAccessToken}
	}
	expected := "user=source-user\x01auth=Bearer " + fakeIMAPAccessToken + "\x01\x01"
	if auth.mechanism == sasl.OAuthBearer {
		expected = "n,a=source-user,\x01auth=Bearer " + fakeIMAPAccessToken + "\x01\x01"
	}
	if string(response) != expected {
		return nil, true, imapserver.ErrAuthFailed
	}
	switch auth.mode {
	case "reject":
		return nil, true, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "refused " + fakeIMAPAccessToken}
	case "challenge":
		auth.challenged = true
		return []byte(`{"status":"` + fakeIMAPAccessToken + `"}`), false, nil
	}
	return nil, true, nil
}

type oauthImportLiteral struct{ *bytes.Reader }

func (literal oauthImportLiteral) Size() int64 { return literal.Reader.Size() }

func newOAuthImportFixture(t *testing.T, fixture *oauthImportFixture, transport string) mailimport.IMAPOptions {
	t.Helper()
	user := imapmemserver.NewUser("source-user", "FAKE-unused-password")
	for _, message := range imapImportMessages() {
		if err := user.Create(message.Folder, nil); err != nil {
			t.Fatal(err)
		}
		var flags []imap.Flag
		for _, flag := range message.Flags {
			flags = append(flags, imap.Flag(flag))
		}
		_, err := user.Append(message.Folder, oauthImportLiteral{bytes.NewReader(message.RawMessage)}, &imap.AppendOptions{
			Flags: flags, Time: message.InternalDate,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	certificate := httptest.NewTLSServer(http.NotFoundHandler())
	pool := x509.NewCertPool()
	pool.AddCert(certificate.Certificate())
	serverTLS := certificate.TLS.Clone()
	certificate.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if transport == "implicit" {
		listener = tls.NewListener(listener, serverTLS)
	}
	options := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &oauthImportSession{UserSession: imapmemserver.NewUserSession(user), fixture: fixture}, nil, nil
		},
		Logger:       log.New(io.Discard, "", 0),
		InsecureAuth: true,
	}
	if transport == "starttls" {
		options.TLSConfig = serverTLS
	}
	server := imapserver.New(options)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("OAuth IMAP fixture did not stop")
		}
	})
	return mailimport.IMAPOptions{Address: listener.Addr().String(), TLSConfig: &tls.Config{RootCAs: pool}, StartTLS: transport != "implicit"}
}

func TestImportIMAPOAuthCopiesFolderAndKeepsFailuresPrivate(t *testing.T) {
	for _, mechanism := range []string{sasl.OAuthBearer, "XOAUTH2"} {
		for _, mode := range []string{"success", "reject", "challenge", "expired", "no OAuth", "no TLS", "untrusted TLS"} {
			for _, asJSON := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/json=%t", mechanism, mode, asJSON), func(t *testing.T) {
					transport := "implicit"
					if !asJSON {
						transport = "starttls"
					}
					if mode == "no TLS" {
						transport = "plaintext"
					}
					fixture := &oauthImportFixture{mechanisms: []string{mechanism}, mode: mode}
					if mode == "no OAuth" {
						fixture.mechanisms = []string{"PLAIN"}
					}
					connection := newOAuthImportFixture(t, fixture, transport)
					if mode == "untrusted TLS" {
						connection.TLSConfig = &tls.Config{RootCAs: x509.NewCertPool()}
					}
					var received []apitypes.ImportMessageBody
					destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						if request.URL.Path != "/api/v1/accounts/12/messages/import" || request.Header.Get("Authorization") != "Bearer FAKE-destination-key" {
							t.Error("wrong destination operation or credential")
						}
						var body apitypes.ImportMessageBody
						if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						received = append(received, body)
						_ = json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{"id": len(received), "message_id": "<history@example.test>", "folder": body.Folder, "imported": true}})
					}))
					defer destination.Close()
					scheme := "imaps://"
					if connection.StartTLS {
						scheme = "imap://"
					}
					args := []string{"--source", scheme + connection.Address, "--source-user", "source-user", "--account-id", "12", "--server", destination.URL}
					if connection.StartTLS {
						args = append(args, "--starttls")
					}
					if asJSON {
						args = append(args, "--json")
					}
					reader := func(ctx context.Context, options mailimport.IMAPOptions, limit int64, visit mailimport.Visit) error {
						if options.AccessToken != fakeIMAPAccessToken || options.Password != "" {
							t.Fatal("source OAuth credentials were not forwarded exclusively")
						}
						options.TLSConfig = connection.TLSConfig
						return mailimport.IMAP(ctx, options, limit, visit)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					var stdout, stderr bytes.Buffer
					code := runImportIMAPSource(ctx, args, "FAKE-destination-key", "", fakeIMAPAccessToken, destination.Client(), &stdout, &stderr, reader)
					if strings.Contains(stdout.String()+stderr.String(), fakeIMAPAccessToken) {
						t.Fatal("source access token leaked to stdout or stderr")
					}
					if fixture.login.Load() != 0 {
						t.Fatal("OAuth source received password sign-in")
					}
					if mode == "success" {
						if code != exitOK || stderr.Len() != 0 || len(received) != 2 {
							t.Fatalf("OAuth copy failed: code=%d copies=%d stderr=%s", code, len(received), &stderr)
						}
						for _, message := range received {
							want := imapImportMessages()[0]
							if message.Folder == "Archive/2020" {
								want = imapImportMessages()[1]
							}
							slices.Sort(message.Flags)
							slices.Sort(want.Flags)
							if !bytes.Equal(message.RawMessage, want.RawMessage) || !slices.Equal(message.Flags, want.Flags) || !message.InternalDate.Equal(want.InternalDate) {
								t.Fatal("OAuth copy changed message octets, flags or date")
							}
						}
					} else {
						if code != exitFailed || stdout.Len() != 0 || stderr.Len() == 0 {
							t.Fatalf("unsafe source did not fail privately: code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
						}
						wantCopies := 0
						if mode == "expired" {
							wantCopies = 1
							if !strings.Contains(stderr.String(), "completed 1 imports") || !strings.Contains(stderr.String(), "obtain a valid RESTMAIL_IMAP_ACCESS_TOKEN and rerun") {
								t.Fatal("expired token did not report progress and recovery")
							}
						}
						if len(received) != wantCopies {
							t.Fatalf("unexpected copies after failure: %d", len(received))
						}
					}
					wantAuth := int32(1)
					if mode == "no TLS" || mode == "untrusted TLS" || mode == "no OAuth" {
						wantAuth = 0
					}
					if fixture.auth.Load() != wantAuth {
						t.Fatalf("authentication attempts: got %d, want %d", fixture.auth.Load(), wantAuth)
					}
				})
			}
		}
	}
}

// OAuth sign-in and flag preservation meet only here: a source that refuses every
// password copies a Deleted message with keywords to the public import API, and the
// flags are read from the request's JSON rather than from the importer's own types.
func TestImportIMAPOAuthPreservesDeletedAndKeywords(t *testing.T) {
	for _, mechanism := range []string{sasl.OAuthBearer, "XOAUTH2"} {
		for _, transport := range []string{"implicit", "starttls"} {
			t.Run(mechanism+"/"+transport, func(t *testing.T) {
				fixture := &oauthImportFixture{mechanisms: []string{mechanism}, mode: "success"}
				connection := newOAuthImportFixture(t, fixture, transport)
				var mutex sync.Mutex
				flags := make(map[string][]string)
				destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					var wire struct {
						Folder string   `json:"folder"`
						Flags  []string `json:"flags"`
					}
					if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
						t.Error(err)
					}
					mutex.Lock()
					flags[wire.Folder] = wire.Flags
					id := len(flags)
					mutex.Unlock()
					_ = json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{"id": id, "message_id": "<history@example.test>", "folder": wire.Folder, "imported": true}})
				}))
				defer destination.Close()
				scheme := "imaps://"
				args := []string{"--account-id", "12", "--server", destination.URL, "--source-user", "source-user"}
				if connection.StartTLS {
					scheme = "imap://"
					args = append(args, "--starttls")
				}
				args = append(args, "--source", scheme+connection.Address)
				reader := func(ctx context.Context, options mailimport.IMAPOptions, limit int64, visit mailimport.Visit) error {
					options.TLSConfig = connection.TLSConfig
					return mailimport.IMAP(ctx, options, limit, visit)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var stdout, stderr bytes.Buffer
				code := runImportIMAPSource(ctx, args, "FAKE-destination-key", "", fakeIMAPAccessToken, destination.Client(), &stdout, &stderr, reader)
				if code != exitOK {
					t.Fatalf("OAuth copy failed: code=%d stderr=%s", code, &stderr)
				}
				if fixture.auth.Load() != 1 || fixture.login.Load() != 0 {
					t.Fatalf("sign-in attempts: OAuth %d, password %d", fixture.auth.Load(), fixture.login.Load())
				}
				want := map[string][]string{
					"INBOX":        {"$Forwarded", "$Junk", `\Answered`, `\Deleted`, `\Seen`},
					"Archive/2020": {`\Flagged`},
				}
				for folder, flagList := range flags {
					slices.Sort(flagList)
					if !slices.Equal(flagList, want[folder]) {
						t.Errorf("%s flags on the wire: got %q, want %q", folder, flagList, want[folder])
					}
				}
				if len(flags) != len(want) {
					t.Fatalf("copied folders: got %d, want %d", len(flags), len(want))
				}
			})
		}
	}
}

func TestImportIMAPOAuthUsageAndWrappedErrors(t *testing.T) {
	for _, mode := range []string{"both credentials", "missing credentials", "token argument", "plaintext URL", "wrapped error", "help"} {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", mode, asJSON), func(t *testing.T) {
				args := []string{"--source", "imaps://source.example.test", "--source-user", "source-user", "--account-id", "12", "--dry-run"}
				if asJSON {
					args = append(args, "--json")
				}
				password, accessToken := "", fakeIMAPAccessToken
				switch mode {
				case "both credentials":
					password = "FAKE-source-password"
				case "missing credentials":
					accessToken = ""
				case "token argument":
					args = append(args, "--access-token", fakeIMAPAccessToken)
				case "plaintext URL":
					args = append(args, "--source", "imap://source.example.test")
				case "help":
					args = append(args, "--help")
				}
				called := false
				reader := func(context.Context, mailimport.IMAPOptions, int64, mailimport.Visit) error {
					called = true
					return fmt.Errorf("wrapped IMAP error: %w", &imap.Error{Type: imap.StatusResponseTypeNo, Text: fakeIMAPAccessToken})
				}
				var stdout, stderr bytes.Buffer
				code := runImportIMAPSource(context.Background(), args, "", password, accessToken, http.DefaultClient, &stdout, &stderr, reader)
				if strings.Contains(stdout.String()+stderr.String(), fakeIMAPAccessToken) {
					t.Fatal("access token leaked to stdout or stderr")
				}
				wantCode := exitUsage
				if mode == "wrapped error" {
					wantCode = exitFailed
				} else if mode == "help" {
					wantCode = exitOK
					if !strings.Contains(stdout.String(), "RESTMAIL_IMAP_ACCESS_TOKEN") || !strings.Contains(stdout.String(), "XOAUTH2") || !strings.Contains(stdout.String(), "OAUTHBEARER") || strings.Contains(stdout.String(), "OAuth sources are not supported") {
						t.Fatal("help does not describe OAuth support")
					}
				}
				if code != wantCode || called != (mode == "wrapped error") {
					t.Fatalf("usage handling: code=%d called=%t", code, called)
				}
				if mode == "both credentials" && !strings.Contains(stderr.String(), "not both") {
					t.Fatal("conflicting credentials lack a clear usage error")
				}
			})
		}
	}
}

func TestImportIMAPSubcommandReadsOAuthEnvironment(t *testing.T) {
	t.Setenv("RESTMAIL_IMAP_PASSWORD", "FAKE-password")
	t.Setenv("RESTMAIL_IMAP_ACCESS_TOKEN", fakeIMAPAccessToken)
	var stdout, stderr bytes.Buffer
	code, handled := runSubcommand([]string{"restmail", "import-imap", "--source", "imaps://source.example.test", "--source-user", "source-user", "--account-id", "12", "--dry-run"}, &stdout, &stderr)
	if !handled || code != exitUsage || !strings.Contains(stderr.String(), "not both") || strings.Contains(stdout.String()+stderr.String(), fakeIMAPAccessToken) {
		t.Fatal("subcommand did not read the source token privately from the environment")
	}
}

func TestImportIMAPOAuthProcessOutput(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestImportIMAPOAuth(CopiesFolderAndKeepsFailuresPrivate|UsageAndWrappedErrors)$", "-test.count=1")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if strings.Contains(stdout.String()+stderr.String(), fakeIMAPAccessToken) {
		t.Fatal("access token leaked through process stdout, stderr or logging")
	}
	if err != nil {
		t.Fatalf("OAuth subprocess tests failed: %v\nstdout: %s\nstderr: %s", err, &stdout, &stderr)
	}
}
