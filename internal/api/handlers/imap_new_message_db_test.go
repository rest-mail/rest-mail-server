package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	gatewayimap "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

// These tests are for issue #1553. They run a real IMAP session and the real
// gateway against the real import and message routes on PostgreSQL, because the
// bug lived in what the import answered and what the gateway made of the answer.
// No test double could show it without first being told about it.

const imapProofMessage = "Message-ID: <only-copy@example.test>\r\n" +
	"From: Original <old@example.test>\r\n" +
	"Subject: only copy\r\n" +
	"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"--B\r\nContent-Type: text/plain\r\n\r\nthe body\r\n" +
	"--B\r\nContent-Type: application/octet-stream; name=a.bin\r\n" +
	"Content-Disposition: attachment; filename=a.bin\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nQUJD\r\n--B--\r\n"

// imapProofAPI serves the routes the gateway uses, as account. Only signing in is
// a stand-in: it is not what these tests are about.
func imapProofAPI(t *testing.T, db *gorm.DB, account models.Mailbox) string {
	t.Helper()
	if err := db.Create(&models.QuotaUsage{MailboxID: account.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(db, &account); err != nil {
		t.Fatal(err)
	}
	importer := NewMessageImportHandler(db, 1<<20, nil, nil)
	importer.directory = t.TempDir()
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, withClaims(r, freshTwoFactorClaims(account.ID, account.Address)))
		})
	})
	router.Post("/api/v1/gateway-sessions", func(w http.ResponseWriter, _ *http.Request) {
		opened := apitypes.GatewaySessionOpened{Token: "rmgs_proof", Protocol: "imap", CredentialID: 1, CredentialKind: "password"}
		opened.User.ID = account.ID
		opened.User.Email = account.Address
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(endpoint.Data[apitypes.GatewaySessionOpened]{Data: opened})
	})
	router.Delete("/api/v1/gateway-sessions/current", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	routes := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	importer.Register(routes)
	messages := NewMessageHandler(db, nil, nil, "", nil)
	messages.RegisterMessages(routes)
	messages.RegisterFolders(routes)
	NewMessageTransferHandler(db, nil).Register(routes)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server.URL
}

type imapProofClient struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
	tag    int
}

func newIMAPProofClient(t *testing.T, apiURL string) *imapProofClient {
	t.Helper()
	near, far := net.Pipe()
	session := imapserver.NewSession(far, gatewayimap.NewBackend(apiclient.New(apiURL), nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	t.Cleanup(func() {
		_ = near.Close()
		<-done
	})
	client := &imapProofClient{t: t, conn: near, reader: bufio.NewReader(near)}
	if greeting := client.line(); !strings.HasPrefix(greeting, "* OK") {
		t.Fatalf("greeting: %q", greeting)
	}
	client.ok("LOGIN user@example.test secret")
	return client
}

func (c *imapProofClient) line() string {
	c.t.Helper()
	// The API is a real one on a loaded machine, so this deadline is a long one.
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	line, err := c.reader.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v (partial %q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

func (c *imapProofClient) write(text string) {
	c.t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
	if _, err := c.conn.Write([]byte(text)); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *imapProofClient) until(tag string) (untagged []string, tagged string) {
	c.t.Helper()
	for {
		line := c.line()
		if strings.HasPrefix(line, tag+" ") {
			return untagged, line
		}
		untagged = append(untagged, line)
	}
}

func (c *imapProofClient) nextTag() string {
	c.tag++
	return "p" + strconv.Itoa(c.tag)
}

// ok runs a command that must succeed.
func (c *imapProofClient) ok(command string) (untagged []string, tagged string) {
	c.t.Helper()
	tag := c.nextTag()
	c.write(tag + " " + command + "\r\n")
	untagged, tagged = c.until(tag)
	if !strings.HasPrefix(tagged, tag+" OK") {
		c.t.Fatalf("%s: %q", command, tagged)
	}
	return untagged, tagged
}

// append sends APPEND and returns what came before the tagged answer and the
// UID that answer reports.
func (c *imapProofClient) append(folder, flags, message string) ([]string, uint) {
	c.t.Helper()
	tag := c.nextTag()
	c.write(fmt.Sprintf("%s APPEND %s %s{%d}\r\n", tag, folder, flags, len(message)))
	if line := c.line(); !strings.HasPrefix(line, "+") {
		c.t.Fatalf("APPEND continuation: %q", line)
	}
	c.write(message + "\r\n")
	untagged, tagged := c.until(tag)
	match := regexp.MustCompile(`\[APPENDUID \d+ (\d+)\]`).FindStringSubmatch(tagged)
	if match == nil {
		c.t.Fatalf("APPEND: %q", tagged)
	}
	return untagged, imapProofUID(c.t, match[1])
}

func imapProofUID(t *testing.T, text string) uint {
	t.Helper()
	uid, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return uint(uid)
}

// copied is the source and destination UID a tagged COPY answer or an untagged
// MOVE line reports.
func copied(t *testing.T, line string) (source, destination uint) {
	t.Helper()
	match := regexp.MustCompile(`\[COPYUID \d+ (\d+) (\d+)\]`).FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("no COPYUID in %q", line)
	}
	return imapProofUID(t, match[1]), imapProofUID(t, match[2])
}

// flagsByUID runs UID FETCH over every message in the selected folder.
func (c *imapProofClient) flagsByUID() map[uint]string {
	c.t.Helper()
	untagged, _ := c.ok("UID FETCH 1:* (FLAGS)")
	found := map[uint]string{}
	for _, line := range untagged {
		uid := regexp.MustCompile(`UID (\d+)`).FindStringSubmatch(line)
		flags := regexp.MustCompile(`FLAGS \(([^)]*)\)`).FindStringSubmatch(line)
		if uid == nil || flags == nil {
			c.t.Fatalf("FETCH line: %q", line)
		}
		found[imapProofUID(c.t, uid[1])] = flags[1]
	}
	return found
}

func requireLineWith(t *testing.T, lines []string, want string) {
	t.Helper()
	for _, line := range lines {
		if line == want {
			return
		}
	}
	t.Errorf("no %q in %q", want, lines)
}

func hasFlag(flags, want string) bool {
	for _, flag := range strings.Fields(flags) {
		if strings.EqualFold(flag, want) {
			return true
		}
	}
	return false
}

// The data-loss sequence: a client copies a message in place, marks the original
// deleted and expunges it. It must be left with the copy.
func TestIMAPCopyInPlaceThenExpungeKeepsTheCopy(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	apiURL := imapProofAPI(t, db, account)
	client := newIMAPProofClient(t, apiURL)

	_, original := client.append("INBOX", `(\Seen) `, imapProofMessage)
	client.ok("SELECT INBOX")

	untagged, tagged := client.ok("COPY 1 INBOX")
	source, copyUID := copied(t, tagged)
	if source != original {
		t.Errorf("COPYUID source = %d, want %d", source, original)
	}
	if copyUID <= original {
		t.Errorf("COPYUID destination = %d, want a UID higher than %d (RFC 3501 §2.3.1.1)", copyUID, original)
	}
	requireLineWith(t, untagged, "* 2 EXISTS")

	client.ok(fmt.Sprintf(`UID STORE %d +FLAGS (\Deleted)`, original))
	client.ok("EXPUNGE")

	var stored []models.Message
	if err := db.Where("mailbox_id = ?", account.ID).Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("the mailbox holds %d messages after expunging the original, want the one copy", len(stored))
	}
	if stored[0].ID != copyUID || stored[0].RawMessage != imapProofMessage || !stored[0].IsRead {
		t.Fatalf("what is left: id %d (copy was %d), read %v, source intact %v", stored[0].ID, copyUID, stored[0].IsRead, stored[0].RawMessage == imapProofMessage)
	}
	var attachments int64
	if err := db.Model(&models.Attachment{}).Where("message_id = ?", copyUID).Count(&attachments).Error; err != nil {
		t.Fatal(err)
	}
	if attachments != 1 {
		t.Fatalf("the copy has %d attachments, want 1", attachments)
	}

	// A second client, starting fresh, finds the message and its octets.
	reader := newIMAPProofClient(t, apiURL)
	untagged, _ = reader.ok("SELECT INBOX")
	requireLineWith(t, untagged, "* 1 EXISTS")
	untagged, _ = reader.ok(fmt.Sprintf("UID FETCH %d (BODY.PEEK[])", copyUID))
	if !strings.Contains(strings.Join(untagged, "\n"), "the body") {
		t.Fatalf("the copy's source did not come back: %q", untagged)
	}
}

// MOVE is a copy and a delete, so a move into the same folder deleted the only
// copy on the spot.
func TestIMAPMoveInPlaceKeepsTheMessage(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	client := newIMAPProofClient(t, imapProofAPI(t, db, account))
	_, original := client.append("INBOX", "", imapProofMessage)
	client.ok("SELECT INBOX")

	untagged, _ := client.ok("MOVE 1 INBOX")
	if len(untagged) == 0 {
		t.Fatal("MOVE answered nothing before its tagged OK")
	}
	_, moved := copied(t, untagged[0])
	if moved <= original {
		t.Errorf("COPYUID destination = %d, want a UID higher than %d", moved, original)
	}
	requireLineWith(t, untagged, "* 1 EXPUNGE")
	requireLineWith(t, untagged, "* 1 EXISTS")

	var stored []models.Message
	if err := db.Where("mailbox_id = ?", account.ID).Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].ID != moved {
		t.Fatalf("after MOVE into the same folder the mailbox holds %d messages, want only %d", len(stored), moved)
	}
	if got := client.flagsByUID(); len(got) != 1 {
		t.Fatalf("the session sees %v, want only the moved message", got)
	}
}

// RFC 3501 §6.3.11: APPEND adds "a new message", whatever is already there.
func TestIMAPRepeatedIdenticalAppendAddsEachMessage(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	client := newIMAPProofClient(t, imapProofAPI(t, db, account))
	client.ok("SELECT INBOX")

	last := uint(0)
	for count := 1; count <= 3; count++ {
		untagged, uid := client.append("INBOX", "", imapProofMessage)
		if uid <= last {
			t.Errorf("APPEND %d reported UID %d, want a UID higher than %d", count, uid, last)
		}
		requireLineWith(t, untagged, fmt.Sprintf("* %d EXISTS", count))
		last = uid
	}
	var stored int64
	if err := db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ?", account.ID, "INBOX").Count(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != 3 {
		t.Fatalf("three APPENDs stored %d messages", stored)
	}
}

// Identical octets appended with two date-times are two rows. Each has a new UID and
// the date-time its own APPEND carried (#1553, #1556). A COPY of the first is a third
// row, which keeps the date of its source.
func TestIMAPRepeatedAppendKeepsEachDateTime(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	client := newIMAPProofClient(t, imapProofAPI(t, db, account))
	client.ok("SELECT INBOX")

	_, first := client.append("INBOX", `"07-Feb-1994 22:43:04 -0800" `, imapProofMessage)
	untagged, second := client.append("INBOX", `"08-Mar-1995 01:02:03 +0100" `, imapProofMessage)
	if second <= first {
		t.Errorf("the second APPEND reported UID %d, want a UID higher than %d", second, first)
	}
	requireLineWith(t, untagged, "* 2 EXISTS")
	_, tagged := client.ok(fmt.Sprintf("UID COPY %d INBOX", first))
	_, third := copied(t, tagged)
	if third <= second {
		t.Errorf("COPY reported UID %d, want a UID higher than %d", third, second)
	}

	earlier := time.Date(1994, time.February, 7, 22, 43, 4, 0, time.FixedZone("", -8*3600))
	later := time.Date(1995, time.March, 8, 1, 2, 3, 0, time.FixedZone("", 3600))
	for _, want := range []struct {
		uid  uint
		date time.Time
	}{{first, earlier}, {second, later}, {third, earlier}} {
		var stored models.Message
		if err := db.First(&stored, want.uid).Error; err != nil {
			t.Fatalf("UID %d is not stored: %v", want.uid, err)
		}
		if !stored.ReceivedAt.Equal(want.date) {
			t.Errorf("UID %d is stored with date %v, want %v", want.uid, stored.ReceivedAt, want.date)
		}
		fetched, _ := client.ok(fmt.Sprintf("UID FETCH %d (INTERNALDATE)", want.uid))
		match := regexp.MustCompile(`INTERNALDATE "([^"]+)"`).FindStringSubmatch(strings.Join(fetched, "\n"))
		if match == nil {
			t.Fatalf("UID FETCH %d: %q", want.uid, fetched)
		}
		date, err := time.Parse("_2-Jan-2006 15:04:05 -0700", match[1])
		if err != nil {
			t.Fatalf("UID FETCH %d: %v", want.uid, err)
		}
		if !date.Equal(want.date) {
			t.Errorf("UID %d answers INTERNALDATE %v, want %v", want.uid, date, want.date)
		}
	}
}

// The flags asked for land on the new message, and the message already there is
// left as it was (RFC 3501 §§6.3.11, 6.4.7). A plain import would have kept the
// unread message and reported it.
func TestIMAPFlagsApplyToTheNewMessage(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	client := newIMAPProofClient(t, imapProofAPI(t, db, account))
	client.ok("CREATE Archive")

	_, unread := client.append("Archive", "", imapProofMessage)

	t.Run("APPEND", func(t *testing.T) {
		_, appended := client.append("Archive", `(\Seen \Flagged) `, imapProofMessage)
		if appended <= unread {
			t.Fatalf("APPEND reported UID %d, want a UID higher than %d", appended, unread)
		}
		client.ok("SELECT Archive")
		flags := client.flagsByUID()
		if !hasFlag(flags[appended], `\Seen`) || !hasFlag(flags[appended], `\Flagged`) {
			t.Errorf("new message %d has flags %q", appended, flags[appended])
		}
		if hasFlag(flags[unread], `\Seen`) || hasFlag(flags[unread], `\Flagged`) {
			t.Errorf("the message already there changed: %q", flags[unread])
		}
		client.ok("CLOSE")
	})

	t.Run("COPY", func(t *testing.T) {
		_, source := client.append("INBOX", `(\Seen \Answered) `, imapProofMessage)
		client.ok("SELECT INBOX")
		client.ok(fmt.Sprintf("UID STORE %d +FLAGS ($Work)", source))
		_, tagged := client.ok(fmt.Sprintf("UID COPY %d Archive", source))
		_, copyUID := copied(t, tagged)
		if copyUID <= source {
			t.Fatalf("COPY reported UID %d, want a UID higher than %d", copyUID, source)
		}
		client.ok("CLOSE")
		client.ok("SELECT Archive")
		flags := client.flagsByUID()
		for _, want := range []string{`\Seen`, `\Answered`, "$Work"} {
			if !hasFlag(flags[copyUID], want) {
				t.Errorf("copy %d has flags %q, want %s", copyUID, flags[copyUID], want)
			}
		}
		if hasFlag(flags[unread], `\Seen`) {
			t.Errorf("the message already there changed: %q", flags[unread])
		}

		var from, to models.Message
		if err := db.First(&from, source).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&to, copyUID).Error; err != nil {
			t.Fatal(err)
		}
		if !to.ReceivedAt.Equal(from.ReceivedAt) {
			t.Errorf("copy's internal date %v, want the source's %v", to.ReceivedAt, from.ReceivedAt)
		}
	})
}

func postImport(t *testing.T, router http.Handler, accountID uint, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", accountID), accountID, nil, string(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func importAnswer(t *testing.T, response *httptest.ResponseRecorder) apitypes.ImportMessageResult {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("import: %d %s", response.Code, response.Body)
	}
	var answer endpoint.Data[apitypes.ImportMessageResult]
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer.Data
}

// The migration path keeps its deduplication. The request here is the one
// `restmail import` builds: the body type with nothing added.
func TestMessageImportPlainRerunStillSkips(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	if err := db.Create(&models.QuotaUsage{MailboxID: account.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(db, &account); err != nil {
		t.Fatal(err)
	}
	importer := NewMessageImportHandler(db, 1<<20, nil, nil)
	importer.directory = t.TempDir()
	router := importMessageRoutes(importer)
	body := apitypes.ImportMessageBody{RawMessage: []byte(imapProofMessage), Folder: "Archive", InternalDate: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	first, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(first), "always_store") {
		t.Fatalf("a plain import names always_store on the wire: %s", first)
	}
	imported := importAnswer(t, postImport(t, router, account.ID, first))
	if !imported.Imported {
		t.Fatalf("first import: %+v", imported)
	}
	var before models.QuotaUsage
	if err := db.Where("mailbox_id = ?", account.ID).First(&before).Error; err != nil {
		t.Fatal(err)
	}

	// The rerun asks for other flags and another date, which a skip must not apply.
	body.Flags = []string{`\Seen`}
	body.InternalDate = body.InternalDate.AddDate(1, 0, 0)
	second, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rerun := importAnswer(t, postImport(t, router, account.ID, second))
	if rerun.Imported || rerun.ID != imported.ID {
		t.Fatalf("rerun: %+v, want a skip naming message %d", rerun, imported.ID)
	}
	var stored []models.Message
	if err := db.Where("mailbox_id = ?", account.ID).Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].IsRead {
		t.Fatalf("after a rerun: %d rows, first read %v", len(stored), len(stored) == 1 && stored[0].IsRead)
	}
	var after models.QuotaUsage
	if err := db.Where("mailbox_id = ?", account.ID).First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.MessageCount != before.MessageCount || after.BodyBytes != before.BodyBytes {
		t.Fatalf("a rerun charged quota: %+v then %+v", before, after)
	}
}

// always_store is what the gateway asks for. It is off unless it is set.
func TestMessageImportAlwaysStore(t *testing.T) {
	db := importProofDB(t)
	account := seedAccountsFixture(t, db).user
	if err := db.Create(&models.QuotaUsage{MailboxID: account.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(db, &account); err != nil {
		t.Fatal(err)
	}
	importer := NewMessageImportHandler(db, 1<<20, nil, nil)
	importer.directory = t.TempDir()
	router := importMessageRoutes(importer)
	payload := func(always string, flags ...string) []byte {
		fields := map[string]any{"raw_message": []byte(imapProofMessage), "folder": "Archive", "internal_date": "2020-01-02T03:04:05Z"}
		if len(flags) != 0 {
			fields["flags"] = flags
		}
		if always != "" {
			fields["always_store"] = json.RawMessage(always)
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	first := importAnswer(t, postImport(t, router, account.ID, payload("")))

	if skipped := importAnswer(t, postImport(t, router, account.ID, payload("false"))); skipped.Imported || skipped.ID != first.ID {
		t.Fatalf("always_store false: %+v, want a skip naming message %d", skipped, first.ID)
	}
	again := importAnswer(t, postImport(t, router, account.ID, payload("true", `\Seen`)))
	if !again.Imported || again.ID <= first.ID {
		t.Fatalf("always_store true: %+v, want a new message after %d", again, first.ID)
	}
	var created, existing models.Message
	if err := db.First(&created, again.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&existing, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !created.IsRead || existing.IsRead {
		t.Fatalf("flags: new message read %v, existing message read %v", created.IsRead, existing.IsRead)
	}
	var usage models.QuotaUsage
	if err := db.Where("mailbox_id = ?", account.ID).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.MessageCount != 2 {
		t.Fatalf("quota counts %d messages, want 2", usage.MessageCount)
	}

	// After a new message exists, a plain import still skips: the first match wins.
	if skipped := importAnswer(t, postImport(t, router, account.ID, payload(""))); skipped.Imported || skipped.ID != first.ID {
		t.Fatalf("plain import after always_store: %+v, want a skip naming message %d", skipped, first.ID)
	}

	// A row with no stored source cannot be compared, and a plain import treats it
	// as the same message. A request to store must not.
	if err := db.Model(&models.Message{}).Where("id IN ?", []uint{first.ID, again.ID}).
		Updates(map[string]any{"raw_message": "", "raw_octets": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if skipped := importAnswer(t, postImport(t, router, account.ID, payload(""))); skipped.Imported {
		t.Fatalf("plain import against a row with no source: %+v, want a skip", skipped)
	}
	if stored := importAnswer(t, postImport(t, router, account.ID, payload("true"))); !stored.Imported {
		t.Fatalf("always_store against a row with no source: %+v, want a new message", stored)
	}

	if refused := postImport(t, router, account.ID, payload(`"yes"`)); refused.Code != http.StatusUnprocessableEntity || !strings.Contains(refused.Body.String(), "always_store") {
		t.Fatalf("always_store as text: %d %s", refused.Code, refused.Body)
	}
}
