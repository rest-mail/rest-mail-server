package handlers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/push"
	"gorm.io/gorm"
)

const (
	pushTestSecret = "push-test-secret"
	pushTestTopic  = "com.antimatterstudios.restmail"
)

// pushTestToken is an APNs device token: 64 lowercase hex digits, the last
// two n.
func pushTestToken(n int) string {
	return strings.Repeat("ab", 31) + fmt.Sprintf("%02x", n)
}

// fakeAPNsServer is an HTTP/2 TLS server standing in for APNs: it records
// each push, and answers with the status and reason answer gives its token.
type fakeAPNsServer struct {
	*httptest.Server
	mu     sync.Mutex
	pushes []fakePush
	answer map[string][2]any // token -> {status, reason}
}

type fakePush struct {
	proto  int
	token  string
	header http.Header
	body   []byte
}

func newFakeAPNsServer(t *testing.T) *fakeAPNsServer {
	t.Helper()
	f := &fakeAPNsServer{answer: map[string][2]any{}}
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		token := strings.TrimPrefix(r.URL.Path, "/3/device/")
		f.mu.Lock()
		f.pushes = append(f.pushes, fakePush{r.ProtoMajor, token, r.Header.Clone(), body})
		answer, refuse := f.answer[token]
		f.mu.Unlock()
		if !refuse {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(answer[0].(int))
		_, _ = fmt.Fprintf(w, `{"reason":%q}`, answer[1].(string))
	}))
	f.EnableHTTP2 = true
	f.StartTLS()
	t.Cleanup(f.Close)
	return f
}

// take returns the pushes received since the last take.
func (f *fakeAPNsServer) take() []fakePush {
	f.mu.Lock()
	defer f.mu.Unlock()
	got := f.pushes
	f.pushes = nil
	return got
}

func (f *fakeAPNsServer) refuse(token string, status int, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer[token] = [2]any{status, reason}
}

// pushEnv is a server with push on, in a transaction rolled back after the
// test: the device routes behind the JWT middleware and the audit log, the
// auth handler, and a dispatcher sending to a fake APNs that the test steps
// itself with a clock of its own.
type pushEnv struct {
	tx         *gorm.DB
	jwt        *auth.JWTService
	authH      *AuthHandler
	routes     http.Handler
	store      *push.Store
	apns       *fakeAPNsServer
	dispatcher *push.Dispatcher
	auditFrom  int64
}

func newPushEnv(t *testing.T) *pushEnv {
	t.Helper()
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("push DB test skipped: no database reachable (%v)", err)
	}
	if err := db.AutoMigrate(append(accountModels, &models.Message{}, &models.Attachment{}, &models.PushDevice{})...); err != nil {
		t.Skipf("push DB test skipped: migrate failed (%v)", err)
	}
	// Device changes are audited, and the log cannot be emptied: everything
	// happens in a transaction the test rolls back.
	if err := rmdb.MigrateAuditLog(db); err != nil {
		t.Skipf("push DB test skipped: audit log migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	e := &pushEnv{tx: tx, jwt: auth.NewJWTService(pushTestSecret, 15*time.Minute, 7*24*time.Hour)}
	if err := tx.Raw("SELECT COALESCE(MAX(seq), 0) FROM audit_log").Scan(&e.auditFrom).Error; err != nil {
		t.Fatal(err)
	}
	e.store = push.NewStore(tx)
	e.authH = NewAuthHandler(tx, e.jwt, "", nil)
	e.authH.SetPushDevices(e.store)

	auditor := middleware.NewAuditor(func(entry *models.AuditEntry) error { return audit.Append(tx, entry) }, nil, e.jwt)
	h := NewDeviceHandler(tx, pushTestTopic)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTMiddleware(e.jwt))
		r.Get("/api/v1/devices", h.List)
		r.With(auditor.Audit(push.ActionDeviceRegister, middleware.AuditBodyFields("platform", "environment"),
			middleware.AuditResponseField("id", "device_id"))).Post("/api/v1/devices", h.Register)
		r.With(auditor.Audit(push.ActionDeviceRemove)).Delete("/api/v1/devices/{device_id}", h.Remove)
	})
	e.routes = r

	e.apns = newFakeAPNsServer(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "AuthKey.p8")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(e.apns.Certificate())
	sender, err := push.NewAPNs(push.APNsConfig{
		KeyFile: keyFile, KeyID: "KEY1234567", TeamID: "TEAM123456", Topic: pushTestTopic,
		ProductionURL: e.apns.URL, SandboxURL: e.apns.URL, RootCAs: roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.dispatcher = push.NewDispatcher(e.store, sender, 5*time.Second)
	return e
}

// pushSessionT is a signed-in mailbox session, as the app holds it.
type pushSessionT struct {
	access, refresh string
	sid             string
	account         uint // the account: its id in GET /accounts, the mailbox's
}

// signIn signs mb in as the app does, with its password, for body tokens.
func (e *pushEnv) signIn(t *testing.T, mb models.Mailbox) pushSessionT {
	t.Helper()
	rr := httptest.NewRecorder()
	e.authH.Login(rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(fmt.Sprintf(`{"email":%q,"password":"password123","client":"native"}`, mb.Address))))
	if rr.Code != http.StatusOK {
		t.Fatalf("sign in %s: %d %s", mb.Address, rr.Code, rr.Body.String())
	}
	return e.sessionFrom(t, rr.Body.Bytes())
}

func (e *pushEnv) sessionFrom(t *testing.T, body []byte) pushSessionT {
	t.Helper()
	var resp struct {
		Data loginResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	claims, err := e.jwt.ValidateAccessToken(resp.Data.AccessToken)
	if err != nil {
		t.Fatalf("access token: %v", err)
	}
	return pushSessionT{access: resp.Data.AccessToken, refresh: resp.Data.RefreshToken, sid: claims.SessionID, account: resp.Data.User.ID}
}

// refreshSession refreshes s as the app does, with the refresh token in the
// body.
func (e *pushEnv) refreshSession(t *testing.T, s pushSessionT) pushSessionT {
	t.Helper()
	rr := httptest.NewRecorder()
	e.authH.Refresh(rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh",
		strings.NewReader(fmt.Sprintf(`{"refresh_token":%q}`, s.refresh))))
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rr.Code, rr.Body.String())
	}
	return e.sessionFrom(t, rr.Body.Bytes())
}

func (e *pushEnv) logout(t *testing.T, refresh string) {
	t.Helper()
	rr := httptest.NewRecorder()
	e.authH.Logout(rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout",
		strings.NewReader(fmt.Sprintf(`{"refresh_token":%q}`, refresh))))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rr.Code, rr.Body.String())
	}
}

// call makes a request to the device routes with access token.
func (e *pushEnv) call(t *testing.T, method, path, access, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	e.routes.ServeHTTP(rr, req)
	return rr.Code, rr.Body.Bytes()
}

// register registers token for s and returns the device's id.
func (e *pushEnv) register(t *testing.T, s pushSessionT, token string) string {
	t.Helper()
	status, body := e.call(t, http.MethodPost, "/api/v1/devices", s.access,
		fmt.Sprintf(`{"platform":"apns","token":%q,"environment":"production","app_id":%q}`, token, pushTestTopic))
	if status != http.StatusCreated {
		t.Fatalf("register: %d %s", status, body)
	}
	var resp struct {
		Data deviceResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Data.ID
}

// devices is GET /api/v1/devices for s.
func (e *pushEnv) devices(t *testing.T, s pushSessionT) (platforms []string, ids []string) {
	t.Helper()
	status, body := e.call(t, http.MethodGet, "/api/v1/devices", s.access, "")
	if status != http.StatusOK {
		t.Fatalf("list devices: %d %s", status, body)
	}
	var resp struct {
		Data struct {
			Platforms []string         `json:"platforms"`
			Devices   []deviceResponse `json:"devices"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Data.Devices {
		ids = append(ids, d.ID)
	}
	return resp.Data.Platforms, ids
}

func (e *pushEnv) deviceExists(t *testing.T, id string) bool {
	t.Helper()
	var n int64
	if err := e.tx.Model(&models.PushDevice{}).Where("id = ?", id).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// newMail puts an unread message, full of things a push must never carry,
// in mb's folder, and tells the dispatcher if it is the INBOX.
func (e *pushEnv) newMail(t *testing.T, mb models.Mailbox, folder string) models.Message {
	t.Helper()
	msg := models.Message{
		MailboxID: mb.ID, Folder: folder,
		Sender: "leak-sender@spy.test", SenderName: "Leaky Sender",
		Subject: "LEAK-SUBJECT quarterly numbers", BodyText: "LEAK-BODY the numbers are", BodyHTML: "<p>LEAK-HTML</p>",
		MsgID:        "<leak-message-id@spy.test>",
		RecipientsTo: models.JSONB(`["` + mb.Address + `"]`), RecipientsCc: models.JSONB(`[]`),
	}
	if err := e.tx.Create(&msg).Error; err != nil {
		t.Fatal(err)
	}
	if folder == "INBOX" {
		e.dispatcher.NewMail(mb.ID)
	}
	return msg
}

// auditEntries is what the test wrote to the audit log with action.
func (e *pushEnv) auditEntries(t *testing.T, action string) []models.AuditEntry {
	t.Helper()
	var entries []models.AuditEntry
	if err := e.tx.Where("seq > ? AND action = ?", e.auditFrom, action).Order("seq").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	return entries
}

// signal is a push's payload, decoded with nothing left out.
type signal struct {
	Device  string
	Account uint
	Unread  int64
}

func decodeSignal(t *testing.T, body []byte) signal {
	t.Helper()
	var p struct {
		APS      map[string]any `json:"aps"`
		RestMail signal         `json:"restmail"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("payload %s: %v", body, err)
	}
	return p.RestMail
}

// TestDevicesRegisterListRemove: a session registers its phone once per
// token, sees only its own devices and never a token, removes them, and each
// registration and removal is in the audit log without the token.
func TestDevicesRegisterListRemove(t *testing.T) {
	e := newPushEnv(t)
	mb := seedMailbox(t, e.tx, "push-reg")
	s := e.signIn(t, mb)
	if s.sid == "" {
		t.Fatal("a sign-in minted no sid")
	}

	platforms, ids := e.devices(t, s)
	if len(platforms) != 1 || platforms[0] != "apns" || len(ids) != 0 {
		t.Fatalf("GET before registering = platforms %v devices %v; want [apns] and none", platforms, ids)
	}

	token := pushTestToken(1)
	id := e.register(t, s, token)
	status, body := e.call(t, http.MethodPost, "/api/v1/devices", s.access,
		fmt.Sprintf(`{"platform":"apns","token":%q,"environment":"production"}`, token))
	if status != http.StatusOK || !strings.Contains(string(body), `"id":"`+id+`"`) {
		t.Fatalf("registering the same token again = %d %s; want 200 with device %s", status, body, id)
	}

	status, body = e.call(t, http.MethodGet, "/api/v1/devices", s.access, "")
	if status != http.StatusOK || strings.Contains(string(body), token) {
		t.Fatalf("GET = %d %s; want 200 and never the token", status, body)
	}
	for _, want := range []string{`"id":"` + id + `"`, `"platform":"apns"`, `"environment":"production"`, `"created_at":"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("GET %s lacks %s", body, want)
		}
	}

	// Another session of the same mailbox: its devices are its own.
	other := e.signIn(t, mb)
	if other.sid == s.sid {
		t.Fatal("two sign-ins got one sid")
	}
	if _, ids := e.devices(t, other); len(ids) != 0 {
		t.Fatalf("another session sees devices %v", ids)
	}
	if status, _ := e.call(t, http.MethodDelete, "/api/v1/devices/"+id, other.access, ""); status != http.StatusNotFound {
		t.Fatalf("another session deleting the device = %d, want 404", status)
	}
	// The same token from it is a device of its own.
	if otherID := e.register(t, other, token); otherID == id {
		t.Fatal("a second session's registration of the token answered with the first's device")
	}

	if status, body := e.call(t, http.MethodDelete, "/api/v1/devices/"+id, s.access, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s, want 204", status, body)
	}
	if status, _ := e.call(t, http.MethodDelete, "/api/v1/devices/"+id, s.access, ""); status != http.StatusNotFound {
		t.Fatalf("DELETE again = %d, want 404", status)
	}
	if status, _ := e.call(t, http.MethodDelete, "/api/v1/devices/not-an-id", s.access, ""); status != http.StatusNotFound {
		t.Fatalf("DELETE of a malformed id = %d, want 404", status)
	}

	registered := e.auditEntries(t, push.ActionDeviceRegister)
	if len(registered) != 3 { // the first, its repeat, and the other session's
		t.Fatalf("%d devices.register entries, want 3", len(registered))
	}
	first := registered[0]
	if first.Outcome != models.AuditOutcomeSuccess || first.Target["device_id"] != id ||
		first.Target["platform"] != "apns" || first.Target["environment"] != "production" ||
		first.ActorKind != models.AuditActorAccount || first.ActorName != mb.Address {
		t.Errorf("devices.register entry = %+v", first)
	}
	removedEntries := e.auditEntries(t, push.ActionDeviceRemove)
	if len(removedEntries) != 4 || removedEntries[1].Target["device_id"] != id || removedEntries[1].Outcome != models.AuditOutcomeSuccess ||
		removedEntries[0].Outcome != models.AuditOutcomeFailed {
		t.Errorf("devices.remove entries = %+v; want the other session's refusal, the removal, and two refusals", removedEntries)
	}
	for _, entry := range append(registered, removedEntries...) {
		if raw, _ := json.Marshal(entry); strings.Contains(string(raw), token) {
			t.Errorf("an audit entry holds the device token: %s", raw)
		}
	}
}

// TestDevicesRegisterValidates: what the app's contract says is refused, is,
// with the field it names.
func TestDevicesRegisterValidates(t *testing.T) {
	e := newPushEnv(t)
	s := e.signIn(t, seedMailbox(t, e.tx, "push-valid"))
	for _, c := range []struct {
		body, field string
	}{
		{`{"platform":"fcm","token":"abc"}`, "platform"},
		{`{"token":"` + pushTestToken(1) + `"}`, "platform"},
		{`{"platform":"apns","token":"ABCDEF` + strings.Repeat("0", 60) + `"}`, "token"},
		{`{"platform":"apns","token":"abc123"}`, "token"},
		{`{"platform":"apns","token":"` + pushTestToken(1) + `","environment":"development"}`, "environment"},
		{`{"platform":"apns","token":"` + pushTestToken(1) + `","app_id":"com.example.other"}`, "app_id"},
	} {
		status, body := e.call(t, http.MethodPost, "/api/v1/devices", s.access, c.body)
		var resp struct {
			Error struct {
				Code    string `json:"code"`
				Details struct {
					Fields map[string]string `json:"fields"`
				} `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &resp)
		if status != http.StatusUnprocessableEntity || resp.Error.Code != "validation_failed" || resp.Error.Details.Fields[c.field] == "" {
			t.Errorf("POST %s = %d %s; want 422 validation_failed on %s", c.body, status, body, c.field)
		}
	}

	// No environment is production; a sandbox token is kept as one.
	status, body := e.call(t, http.MethodPost, "/api/v1/devices", s.access, `{"platform":"apns","token":"`+pushTestToken(2)+`"}`)
	if status != http.StatusCreated || !strings.Contains(string(body), `"environment":"production"`) {
		t.Errorf("POST without an environment = %d %s; want 201, production", status, body)
	}
	status, body = e.call(t, http.MethodPost, "/api/v1/devices", s.access,
		`{"platform":"apns","token":"`+pushTestToken(3)+`","environment":"sandbox"}`)
	if status != http.StatusCreated || !strings.Contains(string(body), `"environment":"sandbox"`) {
		t.Errorf("POST with sandbox = %d %s; want 201, sandbox", status, body)
	}

	for i := 4; i < 4+push.MaxDevicesPerSession-2; i++ {
		e.register(t, s, pushTestToken(i))
	}
	status, body = e.call(t, http.MethodPost, "/api/v1/devices", s.access, `{"platform":"apns","token":"`+pushTestToken(99)+`"}`)
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(body), `"token"`) {
		t.Errorf("registering past %d devices = %d %s, want 422 on token", push.MaxDevicesPerSession, status, body)
	}
}

// TestDevicesTokenWithoutSidIsRefused: every session has a sid, so a token
// without one is refused: an access token on each devices route, and a
// refresh token at refresh, which sends the client back to sign in. The
// tokens differ from the session's own only in the missing sid, and those
// still work.
func TestDevicesTokenWithoutSidIsRefused(t *testing.T) {
	e := newPushEnv(t)
	s := e.signIn(t, seedMailbox(t, e.tx, "push-nosid"))
	withoutSid := func(token string, validate func(string) (*auth.Claims, error)) string {
		t.Helper()
		claims, err := validate(token)
		if err != nil {
			t.Fatal(err)
		}
		claims.SessionID = ""
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(pushTestSecret))
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	access := withoutSid(s.access, e.jwt.ValidateAccessToken)
	refresh := withoutSid(s.refresh, e.jwt.ValidateRefreshToken)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/devices", ""},
		{http.MethodPost, "/api/v1/devices", `{"platform":"apns","token":"` + pushTestToken(1) + `"}`},
		{http.MethodDelete, "/api/v1/devices/1", ""},
	} {
		if status, body := e.call(t, c.method, c.path, access, c.body); status != http.StatusUnauthorized {
			t.Errorf("%s %s with an access token without a sid = %d %s; want 401", c.method, c.path, status, body)
		}
	}

	rr := httptest.NewRecorder()
	e.authH.Refresh(rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh",
		strings.NewReader(fmt.Sprintf(`{"refresh_token":%q}`, refresh))))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh with a refresh token without a sid = %d %s; want 401", rr.Code, rr.Body.String())
	}

	e.register(t, s, pushTestToken(1))
	e.refreshSession(t, s)
}

// TestDevicesEveryAccountIsOfferedPush: every account is a mailbox, so an
// admin's session, like any other, has mail to be told about and is offered
// the platform.
func TestDevicesEveryAccountIsOfferedPush(t *testing.T) {
	e := newPushEnv(t)
	admin := seedMailbox(t, e.tx, "push-admin")
	s := e.signIn(t, admin)
	if platforms, _ := e.devices(t, s); len(platforms) != 1 || platforms[0] != models.PushPlatformAPNs {
		t.Fatalf("GET = platforms %v; want apns", platforms)
	}
}

// TestPushDeliveryCarriesNoMessageContent: new mail in the INBOX sends the
// session's phone a background push over HTTP/2 with the contract's headers,
// whose payload is the device, the account and the unread count, and not one
// thing of the message.
func TestPushDeliveryCarriesNoMessageContent(t *testing.T) {
	e := newPushEnv(t)
	mb := seedMailbox(t, e.tx, "push-deliver")
	s := e.signIn(t, mb)
	token := pushTestToken(1)
	id := e.register(t, s, token)

	msg := e.newMail(t, mb, "INBOX")
	e.newMail(t, mb, "Archive") // not the INBOX: no push of its own
	e.dispatcher.Step(context.Background(), time.Now())

	pushes := e.apns.take()
	if len(pushes) != 1 {
		t.Fatalf("%d pushes, want 1", len(pushes))
	}
	p := pushes[0]
	if p.proto != 2 || p.token != token {
		t.Errorf("push over HTTP/%d to %s; want HTTP/2 to the device's token", p.proto, p.token)
	}
	for header, want := range map[string]string{"apns-push-type": "background", "apns-priority": "5", "apns-topic": pushTestTopic} {
		if got := p.header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if exp := p.header.Get("apns-expiration"); exp == "" || exp == "0" {
		t.Errorf("apns-expiration = %q, want a short expiry", exp)
	}

	if got, want := string(p.body), string(push.Payload(id, s.account, 1)); got != want {
		t.Fatalf("payload = %s, want exactly %s", got, want)
	}
	var whole map[string]map[string]any
	if err := json.Unmarshal(p.body, &whole); err != nil {
		t.Fatal(err)
	}
	if len(whole) != 2 || len(whole["aps"]) != 1 || whole["aps"]["content-available"] != float64(1) || len(whole["restmail"]) != 4 {
		t.Fatalf("payload %s holds more than the signal", p.body)
	}
	for _, leak := range []string{msg.Sender, msg.SenderName, msg.Subject, "LEAK", msg.MsgID, "leak-message-id", mb.Address,
		"spy.test", fmt.Sprintf(`"%d"`, msg.ID), "alert", "subject", "sender", "from"} {
		if strings.Contains(strings.ToLower(string(p.body)), strings.ToLower(leak)) {
			t.Errorf("payload %s carries %q", p.body, leak)
		}
	}
}

// TestDeliveryTellsThePushOfNewInboxMail: delivering a message into the
// INBOX tells the notifier its mailbox; a message delivered elsewhere, or
// already read, does not.
func TestDeliveryTellsThePushOfNewInboxMail(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}); err != nil {
		t.Skipf("deliver DB test skipped: migrate quota_usage failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)

	notified := &recordingNotifier{}
	h := NewMessageHandler(tx, nil, nil, "", nil)
	h.SetNewMailNotifier(notified)
	deliver := func(extra string) {
		t.Helper()
		rr := httptest.NewRecorder()
		h.DeliverMessage(rr, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(fmt.Sprintf(
			`{"mailbox_id":%d,"sender":"someone@remote.test","subject":"hi","body_text":"hello"%s}`, fx.mailbox.ID, extra))))
		if rr.Code != http.StatusCreated {
			t.Fatalf("deliver: %d %s", rr.Code, rr.Body.String())
		}
	}
	deliver("")
	deliver(`,"folder":"Archive"`)
	deliver(`,"folder":"INBOX","is_read":true`)
	if len(notified.mailboxes) != 1 || notified.mailboxes[0] != fx.mailbox.ID {
		t.Fatalf("notified of %v, want only the INBOX delivery to mailbox %d", notified.mailboxes, fx.mailbox.ID)
	}
}

type recordingNotifier struct{ mailboxes []uint }

func (n *recordingNotifier) NewMail(mailboxID uint) { n.mailboxes = append(n.mailboxes, mailboxID) }

// TestPushCoalescesPerDeviceAndAccount: one push per device and account in
// any five seconds; mail inside the window is told at its end, with the
// unread count then; another device, or another account, is not held back.
func TestPushCoalescesPerDeviceAndAccount(t *testing.T) {
	e := newPushEnv(t)
	mb := seedMailbox(t, e.tx, "push-coalesce")
	linked := seedMailbox(t, e.tx, "push-linked")
	phone := e.signIn(t, mb)
	tablet := e.signIn(t, mb)
	phoneID := e.register(t, phone, pushTestToken(1))
	tabletID := e.register(t, tablet, pushTestToken(2))
	// mb owns linked: GET /accounts lists it, by its own id.
	var owner models.Role
	if err := e.tx.Where("name = ?", models.RoleOwner).FirstOrCreate(&owner, models.Role{Name: models.RoleOwner}).Error; err != nil {
		t.Fatal(err)
	}
	var capID uint
	var mailRead models.Capability
	if err := e.tx.Where("name = ?", models.CapMailRead).FirstOrCreate(&mailRead, models.Capability{Name: models.CapMailRead}).Error; err != nil {
		t.Fatal(err)
	}
	capID = mailRead.ID
	e.tx.Where("role_id = ? AND capability_id = ?", owner.ID, capID).FirstOrCreate(&models.RoleCapability{RoleID: owner.ID, CapabilityID: capID})
	if err := e.tx.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: mb.ID, RoleID: owner.ID,
		Scope: "mailbox:" + linked.Address, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	link := struct{ ID uint }{ID: linked.ID}

	ctx := context.Background()
	t0 := time.Now()
	byDevice := func(pushes []fakePush) map[string]signal {
		t.Helper()
		out := map[string]signal{}
		for _, p := range pushes {
			s := decodeSignal(t, p.body)
			out[s.Device+"/"+fmt.Sprint(s.Account)] = s
		}
		if len(out) != len(pushes) {
			t.Fatalf("two pushes for one device and account in one step: %d pushes", len(pushes))
		}
		return out
	}

	e.newMail(t, mb, "INBOX")
	if next := e.dispatcher.Step(ctx, t0); !next.IsZero() {
		t.Errorf("after the first push, next = %v; want nothing waiting", next)
	}
	got := byDevice(e.apns.take())
	if len(got) != 2 || got[phoneID+"/"+fmt.Sprint(phone.account)].Unread != 1 || got[tabletID+"/"+fmt.Sprint(tablet.account)].Unread != 1 {
		t.Fatalf("first mail pushed %v; want one push to each device, unread 1", got)
	}

	e.newMail(t, mb, "INBOX")
	if next := e.dispatcher.Step(ctx, t0.Add(time.Second)); !next.Equal(t0.Add(5 * time.Second)) {
		t.Errorf("mail inside the window: next = %v, want %v", next, t0.Add(5*time.Second))
	}
	e.newMail(t, mb, "INBOX")
	e.dispatcher.Step(ctx, t0.Add(2*time.Second))
	if pushes := e.apns.take(); len(pushes) != 0 {
		t.Fatalf("%d pushes inside the window, want none", len(pushes))
	}

	// The owned mailbox is another account of both sessions: the account
	// owns it, so each lists it, and it is not held back by the window of
	// the mailbox's own.
	e.newMail(t, linked, "INBOX")
	e.dispatcher.Step(ctx, t0.Add(3*time.Second))
	got = byDevice(e.apns.take())
	if len(got) != 2 || got[phoneID+"/"+fmt.Sprint(link.ID)].Unread != 1 || got[tabletID+"/"+fmt.Sprint(link.ID)].Unread != 1 {
		t.Fatalf("owned mailbox pushed %v; want one push to each device for account %d, unread 1", got, link.ID)
	}

	e.dispatcher.Step(ctx, t0.Add(5*time.Second))
	got = byDevice(e.apns.take())
	if len(got) != 2 || got[phoneID+"/"+fmt.Sprint(phone.account)].Unread != 3 || got[tabletID+"/"+fmt.Sprint(tablet.account)].Unread != 3 {
		t.Fatalf("end of the window pushed %v; want one push to each device, unread 3", got)
	}
	e.dispatcher.Step(ctx, t0.Add(11*time.Second))
	if pushes := e.apns.take(); len(pushes) != 0 {
		t.Fatalf("%d pushes with no new mail, want none", len(pushes))
	}
}

// TestPushRemovesDevicesAPNsSaysAreGone: 410, BadDeviceToken and
// DeviceTokenNotForTopic delete the device, recorded in the audit log; any
// other refusal keeps it.
func TestPushRemovesDevicesAPNsSaysAreGone(t *testing.T) {
	e := newPushEnv(t)
	mb := seedMailbox(t, e.tx, "push-gone")
	ids := map[string]string{}
	for i, refusal := range []struct {
		name   string
		status int
		reason string
	}{
		{"unregistered", http.StatusGone, "Unregistered"},
		{"bad token", http.StatusBadRequest, "BadDeviceToken"},
		{"not for topic", http.StatusBadRequest, "DeviceTokenNotForTopic"},
		{"throttled", http.StatusTooManyRequests, "TooManyRequests"},
		{"accepted", 0, ""},
	} {
		token := pushTestToken(i + 1)
		ids[refusal.name] = e.register(t, e.signIn(t, mb), token)
		if refusal.status != 0 {
			e.apns.refuse(token, refusal.status, refusal.reason)
		}
	}

	e.newMail(t, mb, "INBOX")
	e.dispatcher.Step(context.Background(), time.Now())
	if n := len(e.apns.take()); n != 5 {
		t.Fatalf("%d pushes, want 5", n)
	}
	for name, id := range ids {
		gone := name == "unregistered" || name == "bad token" || name == "not for topic"
		if e.deviceExists(t, id) == gone {
			t.Errorf("device %q exists = %v, want %v", name, !gone, !gone)
		}
	}
	entries := e.auditEntries(t, push.ActionDeviceRemove)
	if len(entries) != 3 {
		t.Fatalf("%d devices.remove entries, want 3", len(entries))
	}
	reasons := map[string]bool{}
	for _, entry := range entries {
		if entry.ActorKind != models.AuditActorSystem || entry.Target["reason"] != push.RemovedByAPNs {
			t.Errorf("devices.remove entry = %+v; want the system, for APNs", entry)
		}
		reasons[entry.Target["apns_reason"]] = true
	}
	if !reasons["Unregistered"] || !reasons["BadDeviceToken"] || !reasons["DeviceTokenNotForTopic"] {
		t.Errorf("APNs reasons recorded = %v", reasons)
	}
}

// TestPushDevicesGoWhenTheSessionEnds: a refresh keeps a session's devices;
// a logout, a revocation and an expired refresh token end them. A session
// that has ended gets no push even before the sweep deletes its devices, and
// the sweep leaves a session that is mid-refresh alone.
func TestPushDevicesGoWhenTheSessionEnds(t *testing.T) {
	e := newPushEnv(t)
	mb := seedMailbox(t, e.tx, "push-end")
	other := seedMailbox(t, e.tx, "push-revoked")
	ctx := context.Background()
	now := time.Now()

	// A refresh rotates the jti, not the session: the device stays.
	kept := e.signIn(t, mb)
	keptID := e.register(t, kept, pushTestToken(1))
	refreshed := e.refreshSession(t, kept)
	if refreshed.sid != kept.sid {
		t.Fatalf("a refresh changed the sid from %q to %q", kept.sid, refreshed.sid)
	}
	if _, ids := e.devices(t, refreshed); len(ids) != 1 || ids[0] != keptID {
		t.Fatalf("after a refresh the session lists %v, want [%s]", ids, keptID)
	}
	// Signing out with the old, rotated refresh token does not end it.
	e.logout(t, kept.refresh)
	if !e.deviceExists(t, keptID) {
		t.Fatal("a logout with a rotated refresh token removed the live session's device")
	}

	// Logout.
	out := e.signIn(t, mb)
	outID := e.register(t, out, pushTestToken(2))
	e.logout(t, out.refresh)
	if e.deviceExists(t, outID) {
		t.Fatal("the device outlived its session's logout")
	}
	// Its access token is still valid for a while, but cannot register again.
	if status, body := e.call(t, http.MethodPost, "/api/v1/devices", out.access,
		`{"platform":"apns","token":"`+pushTestToken(2)+`"}`); status != http.StatusUnauthorized {
		t.Fatalf("registering after logout = %d %s, want 401", status, body)
	}

	// Expiry: the refresh token has run out.
	expired := e.signIn(t, mb)
	expiredID := e.register(t, expired, pushTestToken(3))
	if err := e.tx.Model(&models.RefreshToken{}).Where("session_id = ?", expired.sid).
		Update("expires_at", now.Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}

	// Mid-refresh: the old token rotated, the new one not yet saved.
	midway := e.signIn(t, mb)
	midwayID := e.register(t, midway, pushTestToken(4))
	midwayClaims, err := e.jwt.ValidateRefreshToken(midway.refresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.NewRefreshTokenRepository(e.tx).Rotate(midwayClaims.ID); err != nil {
		t.Fatal(err)
	}

	// Revocation: a password change, or a disabled mailbox.
	revoked := e.signIn(t, other)
	revokedID := e.register(t, revoked, pushTestToken(5))
	if err := repositories.NewRefreshTokenRepository(e.tx).RevokeAllForAccount(other.ID); err != nil {
		t.Fatal(err)
	}

	e.newMail(t, mb, "INBOX")
	e.newMail(t, other, "INBOX")
	e.dispatcher.Step(ctx, now)
	pushes := e.apns.take()
	if len(pushes) != 1 || decodeSignal(t, pushes[0].body).Device != keptID {
		t.Fatalf("new mail pushed to %d devices; want only the live session's %s", len(pushes), keptID)
	}

	if n, err := e.store.Sweep(now); err != nil {
		t.Fatal(err)
	} else if n != 2 {
		t.Errorf("sweep removed %d devices; want the expired and the revoked session's (2)", n)
	}
	if !e.deviceExists(t, midwayID) {
		t.Fatal("the sweep removed the device of a session in the middle of a refresh")
	}
	if n, err := e.store.Sweep(now.Add(3 * time.Minute)); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Errorf("a sweep after the grace removed %d devices; want the session whose refresh never finished (1)", n)
	}
	for id, want := range map[string]bool{keptID: true, expiredID: false, midwayID: false, revokedID: false} {
		if e.deviceExists(t, id) != want {
			t.Errorf("device %s exists = %v, want %v", id, !want, want)
		}
	}
	ended := 0
	for _, entry := range e.auditEntries(t, push.ActionDeviceRemove) {
		if entry.Target["reason"] == push.RemovedSessionEnded && entry.ActorKind == models.AuditActorSystem {
			ended++
		}
	}
	if ended != 4 {
		t.Errorf("%d devices.remove entries for ended sessions, want 4 (logout, expired, revoked, mid-refresh)", ended)
	}
}
