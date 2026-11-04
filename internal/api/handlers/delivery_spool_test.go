package handlers

// These speak only to the routes, so they hold on any way of reading a request. The answers and
// rows in testdata/delivery_wire_answers.json were recorded from the routes as they stood when
// they bound a whole request in memory, and the limits below fail on those routes.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"gorm.io/gorm"
)

// spoolProbe is a filter that answers as it is told, and records the message it was given.
type spoolProbe struct {
	action  pipeline.Action
	sawRaw  *string
	sawBody *string
}

func (p spoolProbe) Name() string              { return "spool_probe" }
func (p spoolProbe) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (p spoolProbe) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	if p.sawRaw != nil {
		*p.sawRaw = email.Metadata["raw_message"]
	}
	if p.sawBody != nil {
		*p.sawBody = email.Body.Content
	}
	result := &pipeline.FilterResult{Action: p.action}
	if p.action == pipeline.ActionReject {
		result.RejectMsg = "refused by the probe"
	}
	return result, nil
}

// spoolFixture is two accounts in a domain whose pipeline is a probe.
type spoolFixture struct {
	db      *gorm.DB
	mailbox models.Mailbox
	engine  *pipeline.Engine
}

func spoolEngine(t *testing.T, probe spoolProbe) *pipeline.Engine {
	t.Helper()
	registry := withQuietVacation(pipeline.NewRegistry())
	registry.Register("dmarc_reports_received", func([]byte) (pipeline.Filter, error) { return spoolProbe{action: pipeline.ActionContinue}, nil })
	registry.Register("spool_probe", func([]byte) (pipeline.Filter, error) { return probe, nil })
	registry.Register("extract_attachments", filters.NewExtractAttachments)
	return pipeline.NewEngine(registry, nil)
}

func newSpoolFixture(t *testing.T, probe spoolProbe) spoolFixture {
	t.Helper()
	db := importProofDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}, &models.Pipeline{}, &models.Quarantine{}, &models.OutboundQueue{}, &models.CalendarEventVersion{}, &models.SieveScript{}, &models.Alias{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "spool.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "owner", Address: "owner@spool.test", Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, db, mailbox)
	if err := db.Create(&models.QuotaUsage{MailboxID: mailbox.ID}).Error; err != nil {
		t.Fatal(err)
	}
	// An identity that exists and can't receive: no grant reaches it.
	silent := models.Mailbox{DomainID: domain.ID, LocalPart: "silent", Address: "silent@spool.test", Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&silent).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal([]pipeline.FilterConfig{
		{Name: "spool_probe", Type: pipeline.FilterTypeAction, Enabled: true},
		{Name: "extract_attachments", Type: pipeline.FilterTypeTransform, Enabled: true, Config: json.RawMessage(`{"storage_type":"filesystem","storage_path":` + spoolAttachmentRoot(t) + `}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Active: true, Filters: stored}).Error; err != nil {
		t.Fatal(err)
	}
	return spoolFixture{db: db, mailbox: mailbox, engine: spoolEngine(t, probe)}
}

// spoolAttachmentRoot is a directory for attachments, as JSON.
func spoolAttachmentRoot(t *testing.T) string {
	t.Helper()
	encoded, _ := json.Marshal(t.TempDir())
	return string(encoded)
}

// spoolWriter is a response that costs nothing to write, so that a measure is of the route alone.
type spoolWriter struct {
	header http.Header
	code   int
	body   []byte
}

func (w *spoolWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *spoolWriter) WriteHeader(code int) { w.code = code }
func (w *spoolWriter) Write(data []byte) (int, error) {
	if room := 256 - len(w.body); room > 0 {
		w.body = append(w.body, data[:min(room, len(data))]...)
	}
	return len(data), nil
}

func (d spoolFixture) gateway(w http.ResponseWriter, body io.Reader, contentType string) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", body)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	deliveryRoute(NewMessageHandler(d.db, nil, d.engine, "", nil))(w, r)
}

func (d spoolFixture) restmail(w http.ResponseWriter, body io.Reader, contentType string, auth RestmailDeliverAuthConfig) {
	r := httptest.NewRequest(http.MethodPost, restmailMessagesPath, body)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	r.RemoteAddr = "192.0.2.10:1234"
	restmailRoutes(NewRestmailHandler(d.db, d.engine, nil, RestmailTarpitConfig{}, auth)).ServeHTTP(w, r)
}

var restmailTrusted = RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}}

func spoolBase64(message string) string { return base64.StdEncoding.EncodeToString([]byte(message)) }

func gatewayBody(message string, extra map[string]any) []byte {
	body := map[string]any{"address": "owner@spool.test", "sender": "someone@remote.test", "subject": "big", "raw_message": spoolBase64(message)}
	for key, value := range extra {
		body[key] = value
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

func restmailBody(message string, extra map[string]any) []byte {
	body := map[string]any{"from": "someone@remote.test", "to": []string{"owner@spool.test"}, "raw_message": message}
	for key, value := range extra {
		if value == nil {
			delete(body, key)
			continue
		}
		body[key] = value
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

const spoolMessageSize = 2 << 20

func spoolTextMessage() string {
	return "From: someone@remote.test\r\nTo: owner@spool.test\r\nSubject: big\r\nMessage-ID: <big@remote.test>\r\n\r\n" + strings.Repeat("x", spoolMessageSize)
}

func spoolAttachmentMessage() string {
	return "From: someone@remote.test\r\nTo: owner@spool.test\r\nSubject: big\r\nMessage-ID: <big@remote.test>\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nbody\r\n" +
		"--B\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\n" + strings.Repeat("y", spoolMessageSize) + "\r\n--B--\r\n"
}

func spoolCalendarMessage() string {
	return "From: someone@remote.test\r\nTo: owner@spool.test\r\nSubject: cal\r\nMessage-ID: <cal@remote.test>\r\nContent-Type: text/calendar; charset=utf-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:one\r\nSEQUENCE:1\r\nSUMMARY:Lunch\r\nDTSTART:20200102T030405Z\r\nDTEND:20200102T040405Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

// spoolStoredLimit is what a stored message may allocate across its request, over its size. What
// is left is what the pipeline and the row need: the message read once and parsed once, with text
// or an attachment's base64 built from it, and each column encoded for the database. The race
// detector adds about two to that. Routes that bound whole requests allocated 17 to 24 times over,
// and fail this.
const spoolStoredLimit = 15

// spoolRefusalLimit is what a refusal may allocate across its request. A refusal costs about 0.9 MiB
// at most with the race detector on, whatever the message's size, and once went just past 1 MiB, so
// three quarters of the message fails on one copy of it and holds here.
const spoolRefusalLimit = spoolMessageSize * 3 / 4

func TestDeliveryRefusalsDoNotHoldTheMessage(t *testing.T) {
	spool := spoolDirectory(t)
	d := newSpoolFixture(t, spoolProbe{action: pipeline.ActionContinue})
	message := spoolTextMessage()
	large := strings.Repeat("x", spoolMessageSize/2)
	cases := []struct {
		name   string
		body   []byte
		serve  func(w http.ResponseWriter, body io.Reader)
		status int
	}{}
	gateway := func(w http.ResponseWriter, body io.Reader) { d.gateway(w, body, "application/json") }
	for _, c := range []struct {
		name   string
		extra  map[string]any
		status int
	}{
		{"no such mailbox", map[string]any{"address": "no@spool.test", "body_text": large}, 404},
		{"a mailbox that can't receive", map[string]any{"address": "silent@spool.test", "body_html": large}, 404},
		{"a bad folder", map[string]any{"folder": " x", "body_text": large}, 422},
		{"a field no one declared", map[string]any{"x": 1}, 422},
		{"NUL in the body text", map[string]any{"body_text": large + "\x00"}, 422},
	} {
		cases = append(cases, struct {
			name   string
			body   []byte
			serve  func(w http.ResponseWriter, body io.Reader)
			status int
		}{"gateway " + c.name, gatewayBody(message, c.extra), gateway, c.status})
	}
	cases = append(cases, struct {
		name   string
		body   []byte
		serve  func(w http.ResponseWriter, body io.Reader)
		status int
	}{"gateway NUL in the message", gatewayBody(message+"\x00", nil), gateway, 422})
	for _, c := range []struct {
		name   string
		extra  map[string]any
		auth   RestmailDeliverAuthConfig
		status int
	}{
		{"no such recipient", map[string]any{"to": []string{"no@spool.test"}}, restmailTrusted, 422},
		{"a spoofed sender, unsigned", map[string]any{"from": "x@spool.test", "body_text": large}, RestmailDeliverAuthConfig{}, 403},
		{"a field no one declared", map[string]any{"x": 1}, restmailTrusted, 422},
	} {
		auth := c.auth
		cases = append(cases, struct {
			name   string
			body   []byte
			serve  func(w http.ResponseWriter, body io.Reader)
			status int
		}{"restmail " + c.name, restmailBody(message, c.extra), func(w http.ResponseWriter, body io.Reader) { d.restmail(w, body, "application/json", auth) }, c.status})
	}
	// The limits on hops and structure are decided without holding the message either.
	padded := func(raw string) string {
		head, tail, _ := strings.Cut(raw, "\r\n\r\n")
		return head + "\r\nX-Pad: y\r\n\r\n" + tail + strings.Repeat("x", spoolMessageSize)
	}
	limitCase := func(name string, body []byte, serve func(w http.ResponseWriter, body io.Reader), status int) {
		cases = append(cases, struct {
			name   string
			body   []byte
			serve  func(w http.ResponseWriter, body io.Reader)
			status int
		}{name, body, serve, status})
	}
	trusted := func(w http.ResponseWriter, body io.Reader) { d.restmail(w, body, "application/json", restmailTrusted) }
	open := func(w http.ResponseWriter, body io.Reader) {
		d.restmail(w, body, "application/json", RestmailDeliverAuthConfig{})
	}
	looping := padded(receivedMessage(rmime.DefaultMaxReceived+1, "owner@spool.test"))
	limitCase("gateway a mail loop", gatewayBody(looping, map[string]any{"received_tls": false}), gateway, 550)
	limitCase("restmail a mail loop", restmailBody(looping, nil), trusted, 403)
	// A part as large as the message comes first, so reaching the limit means passing over it.
	tooNested := "From: someone@remote.test\r\nTo: owner@spool.test\r\nContent-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\n" + strings.Repeat("y", spoolMessageSize) + "\r\n--outer\r\n"
	for depth := 2; depth <= rmime.DefaultMaxNestingDepth+1; depth++ {
		tooNested += fmt.Sprintf("Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", depth, depth)
	}
	limitCase("restmail nested past the limit", restmailBody(tooNested+"Content-Type: text/plain\r\n\r\nbottom\r\n", nil), trusted, 403)
	// A multipart within the limits still has its parts passed over before the spoofing check.
	limitCase("restmail a spoofed sender, unsigned, an attachment", restmailBody(spoolAttachmentMessage(), map[string]any{"from": "x@spool.test"}), open, 403)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &spoolWriter{}
			allocated := allocatedBy(func() { tc.serve(w, bytes.NewReader(tc.body)) })
			t.Logf("refusing %d bytes allocated %d", spoolMessageSize, allocated)
			if w.code != tc.status {
				t.Fatalf("answered %d %s", w.code, w.body)
			}
			if allocated > spoolRefusalLimit {
				t.Fatalf("allocated %d bytes refusing a %d byte message", allocated, spoolMessageSize)
			}
			assertNoSpool(t, spool)
		})
	}
}

// A message that is stored is read once, and the row is the last place it goes.
func TestDeliveredMessageHoldsOnlyWhatItNeeds(t *testing.T) {
	spool := spoolDirectory(t)
	d := newSpoolFixture(t, spoolProbe{action: pipeline.ActionContinue})
	gateway := func(w http.ResponseWriter, body io.Reader) { d.gateway(w, body, "application/json") }
	restmail := func(w http.ResponseWriter, body io.Reader) { d.restmail(w, body, "application/json", restmailTrusted) }
	for _, tc := range []struct {
		name  string
		body  []byte
		serve func(w http.ResponseWriter, body io.Reader)
	}{
		{"gateway, an attachment", gatewayBody(spoolAttachmentMessage(), nil), gateway},
		{"gateway, text", gatewayBody(spoolTextMessage(), nil), gateway},
		{"restmail, an attachment", restmailBody(spoolAttachmentMessage(), nil), restmail},
		{"restmail, text", restmailBody(spoolTextMessage(), nil), restmail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &spoolWriter{}
			allocated := allocatedBy(func() { tc.serve(w, bytes.NewReader(tc.body)) })
			t.Logf("storing %d bytes allocated %d, %.1f times over", spoolMessageSize, allocated, float64(allocated)/spoolMessageSize)
			if w.code != http.StatusCreated {
				t.Fatalf("answered %d %s", w.code, w.body)
			}
			if allocated > spoolStoredLimit*spoolMessageSize {
				t.Fatalf("allocated %.1f times over, the limit is %d", float64(allocated)/spoolMessageSize, spoolStoredLimit)
			}
			assertNoSpool(t, spool)
		})
	}
}

type panickingDeliveryBroker struct{}

func (panickingDeliveryBroker) Publish(uint, SSEEvent) { panic("broker") }

// Every way out of the routes leaves no file: each answer, a client gone mid-request and a panic.
func TestDeliveryLeavesNoFile(t *testing.T) {
	spool := spoolDirectory(t)
	d := newSpoolFixture(t, spoolProbe{action: pipeline.ActionContinue})
	message := "From: someone@remote.test\r\nTo: owner@spool.test\r\nSubject: s\r\nMessage-ID: <m@remote.test>\r\n\r\nbody\r\n"
	broken := func(start string) io.Reader {
		return io.MultiReader(strings.NewReader(start), iotest.ErrReader(errors.New("connection reset")))
	}
	limited := func(serve http.Handler) http.Handler { return middleware.MaxBodyBytes(64)(serve) }
	for _, action := range []pipeline.Action{pipeline.ActionContinue, pipeline.ActionReject, pipeline.ActionDefer, pipeline.ActionDiscard, pipeline.ActionQuarantine} {
		probed := d
		probed.engine = spoolEngine(t, spoolProbe{action: action})
		t.Run("gateway "+string(action), func(t *testing.T) {
			w := httptest.NewRecorder()
			probed.gateway(w, bytes.NewReader(gatewayBody(message, map[string]any{"body_text": "text", "body_html": "<p>html</p>"})), "application/json")
			if w.Code >= 500 && w.Code != 550 {
				t.Fatalf("answered %d %s", w.Code, w.Body)
			}
			assertNoSpool(t, spool)
		})
		t.Run("restmail "+string(action), func(t *testing.T) {
			w := httptest.NewRecorder()
			probed.restmail(w, bytes.NewReader(restmailBody(message, map[string]any{"body_text": "text", "body_html": "<p>html</p>"})), "application/json", restmailTrusted)
			if w.Code >= 500 && w.Code != 503 {
				t.Fatalf("answered %d %s", w.Code, w.Body)
			}
			assertNoSpool(t, spool)
		})
	}
	for _, tc := range []struct {
		name   string
		body   io.Reader
		status int
	}{
		{"not JSON", strings.NewReader(`{`), 400},
		{"empty", strings.NewReader(``), 400},
		{"text sent twice", strings.NewReader(`{"body_text":"a","body_text":"b","address":"owner@spool.test"}`), 201},
		{"client gone in the message", broken(`{"raw_message":"QQ`), 400},
		{"client gone in the text", broken(`{"body_text":"QQ`), 400},
		{"client gone at once", broken(``), 400},
	} {
		t.Run("gateway "+tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			d.gateway(w, tc.body, "application/json")
			if w.Code != tc.status {
				t.Fatalf("answered %d %s", w.Code, w.Body)
			}
			assertNoSpool(t, spool)
		})
	}
	t.Run("past the body cap", func(t *testing.T) {
		for path, routes := range map[string]http.Handler{
			"/api/v1/messages/deliver": limited(deliveryRoute(NewMessageHandler(d.db, nil, d.engine, "", nil))),
			restmailMessagesPath:       limited(restmailRoutes(NewRestmailHandler(d.db, d.engine, nil, RestmailTarpitConfig{}, restmailTrusted))),
		} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(gatewayBody(strings.Repeat("x", 4096), nil)))
			r.Header.Set("Content-Type", "application/json")
			routes.ServeHTTP(w, r)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s answered %d %s", path, w.Code, w.Body)
			}
			assertNoSpool(t, spool)
		}
	})
	panics := func(t *testing.T, serve func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatal("expected a panic")
			}
			assertNoSpool(t, spool)
		}()
		serve()
	}
	t.Run("panic past the point every file exists", func(t *testing.T) {
		// A nil database panics at the first query.
		nothing := spoolFixture{}
		panics(t, func() {
			nothing.gateway(httptest.NewRecorder(), bytes.NewReader(gatewayBody(message, map[string]any{"body_text": "text"})), "application/json")
		})
		panics(t, func() {
			nothing.restmail(httptest.NewRecorder(), bytes.NewReader(restmailBody(message, map[string]any{"body_text": "text"})), "application/json", restmailTrusted)
		})
	})
	t.Run("panic after the message is stored", func(t *testing.T) {
		h := NewMessageHandler(d.db, nil, d.engine, "", nil)
		h.broker = panickingDeliveryBroker{}
		panics(t, func() {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", bytes.NewReader(gatewayBody(message, nil)))
			r.Header.Set("Content-Type", "application/json")
			deliveryRoute(h)(httptest.NewRecorder(), r)
		})
	})
}

// The server's own disk failing is not the client's fault, so it is not a client's refusal.
func TestDeliveryDiskFailureIsNotARefusal(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()+"/gone")
	d := spoolFixture{}
	gateway := httptest.NewRecorder()
	d.gateway(gateway, bytes.NewReader(gatewayBody("Subject: x\r\n\r\nbody", nil)), "application/json")
	restmail := httptest.NewRecorder()
	d.restmail(restmail, bytes.NewReader(restmailBody("Subject: x\r\n\r\nbody", nil)), "application/json", restmailTrusted)
	for _, w := range []*httptest.ResponseRecorder{gateway, restmail} {
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("answered %d %s", w.Code, w.Body)
		}
	}
}

// The stored message is what was sent: the probe and the row see the same bytes.
func TestDeliveryGivesThePipelineTheMessage(t *testing.T) {
	message := "From: someone@remote.test\r\nTo: owner@spool.test\r\nSubject: s\r\nMessage-ID: <exact@remote.test>\r\nContent-Type: text/plain\r\n\r\nbody caf\xc3\xa9\r\n"
	var raw, body string
	d := newSpoolFixture(t, spoolProbe{action: pipeline.ActionContinue, sawRaw: &raw, sawBody: &body})
	w := httptest.NewRecorder()
	d.gateway(w, bytes.NewReader(gatewayBody(message, nil)), "application/json")
	if w.Code != http.StatusCreated {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	if raw != message || body != "body caf\xc3\xa9\r\n" {
		t.Fatalf("the pipeline saw %q and %q", raw, body)
	}
	raw, body = "", ""
	w = httptest.NewRecorder()
	d.restmail(w, bytes.NewReader(restmailBody(message, nil)), "application/json", restmailTrusted)
	if w.Code != http.StatusCreated {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	if raw != message || body != "body caf\xc3\xa9\r\n" {
		t.Fatalf("the pipeline saw %q and %q", raw, body)
	}
}
