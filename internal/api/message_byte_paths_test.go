package api

import (
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

type observedMailBody struct {
	body       *strings.Reader
	observe    func()
	checkpoint int
}

func (b *observedMailBody) Read(buf []byte) (int, error) {
	if b.observe != nil && b.body.Len() <= b.checkpoint {
		b.observe()
		b.observe = nil
	}
	return b.body.Read(buf)
}

type mailAcknowledgement struct {
	*httptest.ResponseRecorder
	written     bool
	beforeWrite func(int)
}

func (a *mailAcknowledgement) WriteHeader(status int) {
	if !a.written {
		a.written = true
		a.beforeWrite(status)
	}
	a.ResponseRecorder.WriteHeader(status)
}

func (a *mailAcknowledgement) Write(body []byte) (int, error) {
	if !a.written {
		a.WriteHeader(http.StatusOK)
	}
	return a.ResponseRecorder.Write(body)
}

func TestAC01FileCarryingPathInventoryAndSpoolLifecycle(t *testing.T) {
	root := filepath.Join("..", "..")
	file, err := os.Open(filepath.Join(root, "docs", "message-byte-paths.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.Comma = '\t'
	reader.FieldsPerRecord = 7
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 || rows[0][0] != "id" {
		t.Fatal("missing inventory header or data")
	}
	inventory := make(map[string][]string)
	for _, row := range rows[1:] {
		if inventory[row[0]] != nil {
			t.Fatalf("duplicate inventory row %q", row[0])
		}
		inventory[row[0]] = row
		for _, column := range row {
			if strings.TrimSpace(column) == "" {
				t.Fatalf("empty inventory column: %v", row)
			}
		}
		for _, source := range strings.Split(row[5], ",") {
			if _, err := os.Stat(filepath.Join(root, source)); err != nil {
				t.Errorf("%s production path: %v", row[0], err)
			}
		}
	}
	for _, name := range []string{"contact-upload", "certificate-upload", "message-import", "console-file-import", "console-imap-import", "console-export", "imap-append", "smtp-data", "gateway-delivery", "restmail-delivery", "tls-report-http", "tls-report-mail", "dmarc-report-mail", "attachment-store", "attachment-download", "attachment-forward", "raw-retrieval", "queue-relay", "filter-http"} {
		if inventory[name] == nil {
			t.Fatalf("missing file-carrying flow %q", name)
		}
	}

	fixture := newCredentialFixtureWith(t, &config.Config{Environment: "test", APIMaxBodyBytes: 12 << 20, SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize})
	fixture.own(fixture.mailboxID, fixture.address)
	if err := fixture.db.Create(&models.QuotaUsage{MailboxID: fixture.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	var domain models.Domain
	if err := fixture.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	filters := fmt.Sprintf(`[{"name":"extract_attachments","type":"transform","enabled":true,"config":{"storage_path":%q}},{"name":"sieve","type":"transform","enabled":true}]`, t.TempDir())
	if err := fixture.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: []byte(filters), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	var acknowledged int64
	for _, route := range []string{"message-import", "gateway-delivery", "restmail-delivery"} {
		t.Run(route, func(t *testing.T) {
			for _, bytes := range []int{2 << 20, 6 << 20} {
				t.Run(fmt.Sprint(bytes), func(t *testing.T) {
					messageID := fmt.Sprintf("<%s-%d@remote.test>", route, bytes)
					raw := fmt.Sprintf("From: sender@remote.test\r\nTo: %s\r\nSubject: spool\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nMessage-ID: %s\r\n\r\n", fixture.address, messageID) + strings.Repeat("line with text\r\n", bytes/16)
					encoded := base64.StdEncoding.EncodeToString([]byte(raw))
					path := strings.TrimPrefix(inventory[route][1], "POST ")
					path = strings.ReplaceAll(path, "{id}", fmt.Sprint(fixture.mailboxID))
					var fields, authorization string
					switch route {
					case "message-import":
						fields = `,"internal_date":"2020-01-02T03:04:05Z"`
						authorization = fixture.session
					case "gateway-delivery":
						fields = fmt.Sprintf(`,"address":%q,"sender":"sender@remote.test","recipients_to":[%q]`, fixture.address, fixture.address)
					case "restmail-delivery":
						fields = fmt.Sprintf(`,"from":"sender@remote.test","to":[%q]`, fixture.address)
					}
					for _, accepted := range []bool{false, true} {
						requestFields, want := fields, http.StatusCreated
						if route == "message-import" {
							want = http.StatusOK
						}
						if !accepted {
							requestFields += `,"unknown_field":true`
							want = http.StatusUnprocessableEntity
						}
						body := `{"raw_message":"` + encoded + `"` + requestFields + `}`
						response := &mailAcknowledgement{ResponseRecorder: httptest.NewRecorder()}
						input := &observedMailBody{body: strings.NewReader(body), checkpoint: len(body) / 2, observe: func() {
							if response.written {
								t.Error("protocol response began before the upload ended")
							}
							files, err := os.ReadDir(dir)
							if err != nil {
								t.Fatal(err)
							}
							var spooled int64
							for _, file := range files {
								info, err := file.Info()
								if err != nil {
									t.Fatal(err)
								}
								if strings.HasPrefix(file.Name(), "restmail-spool-") {
									spooled += info.Size()
								}
							}
							if spooled < int64(len(body)/2) || spooled >= int64(len(body)) {
								t.Errorf("mid-upload disk bytes = %d, want half of %d or more but not all", spooled, len(body))
							}
						}}
						response.beforeWrite = func(status int) {
							if input.body.Len() != 0 {
								t.Errorf("HTTP %d sent with %d unread upload bytes", status, input.body.Len())
							}
							if accepted && status == want {
								acknowledged++
								var count int64
								if err := fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", fixture.mailboxID).Count(&count).Error; err != nil || count != acknowledged {
									t.Errorf("HTTP %d written before the message was visible in PostgreSQL: count=%d want=%d err=%v", status, count, acknowledged, err)
								}
							}
						}
						request := httptest.NewRequest(http.MethodPost, path, input)
						request.Header.Set("Content-Type", "application/json")
						request.Header.Set("Authorization", authorization)
						var before, after runtime.MemStats
						runtime.ReadMemStats(&before)
						fixture.router.ServeHTTP(response, request)
						runtime.ReadMemStats(&after)
						if response.Code != want || input.observe != nil || input.body.Len() != 0 {
							t.Fatalf("HTTP %d: %s; want %d after the whole body", response.Code, response.Body, want)
						}
						files, err := os.ReadDir(dir)
						if err != nil || len(files) != 0 {
							t.Fatalf("spools left behind: %v (%v)", files, err)
						}
						if !accepted {
							allocated := after.TotalAlloc - before.TotalAlloc
							t.Logf("refused %d wire bytes, allocated %d Go bytes", len(body), allocated)
							if allocated >= uint64(len(raw)) {
								t.Fatalf("refusal allocated %d bytes, enough for a complete %d-byte decoded message", allocated, len(raw))
							}
							continue
						}
						stored := lastStored(t, fixture, raw)
						if stored.MailboxID != fixture.mailboxID {
							t.Fatalf("acknowledged message stored in mailbox %d, want %d", stored.MailboxID, fixture.mailboxID)
						}
					}
				})
			}
		})
	}
	var stored int64
	if err := fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", fixture.mailboxID).Count(&stored).Error; err != nil || stored != 6 {
		t.Fatalf("stored messages = %d, want one per successful acknowledgement and none per refusal: %v", stored, err)
	}
}
