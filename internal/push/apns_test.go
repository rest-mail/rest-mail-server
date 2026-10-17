package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/db/models"
)

const (
	testKeyID  = "KEY1234567"
	testTeamID = "TEAM123456"
	testTopic  = "com.antimatterstudios.restmail"
	testToken  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// apnsRequest is one push the fake APNs received.
type apnsRequest struct {
	ProtoMajor int
	Method     string
	Path       string
	Header     http.Header
	Body       []byte
}

// fakeAPNs is an HTTP/2 TLS server that records each push and answers with
// the status and reason answer gives for its token.
type fakeAPNs struct {
	*httptest.Server
	mu       sync.Mutex
	requests []apnsRequest
	answer   func(token string) (int, string)
}

func newFakeAPNs(t *testing.T) *fakeAPNs {
	t.Helper()
	f := &fakeAPNs{answer: func(string) (int, string) { return http.StatusOK, "" }}
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, apnsRequest{r.ProtoMajor, r.Method, r.URL.Path, r.Header.Clone(), body})
		answer := f.answer
		f.mu.Unlock()
		status, reason := answer(strings.TrimPrefix(r.URL.Path, "/3/device/"))
		w.Header().Set("apns-id", "00000000-0000-0000-0000-000000000000")
		w.WriteHeader(status)
		if reason != "" {
			_, _ = w.Write([]byte(`{"reason":"` + reason + `"}`))
		}
	}))
	f.EnableHTTP2 = true
	f.StartTLS()
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPNs) received() []apnsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apnsRequest(nil), f.requests...)
}

// writeKey writes key as a .p8 file (PKCS #8 PEM) and returns its path.
func writeKey(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// newTestAPNs is a sender whose production and sandbox hosts are the two
// fakes, at the time now returns.
func newTestAPNs(t *testing.T, key *ecdsa.PrivateKey, production, sandbox *fakeAPNs, now func() time.Time) *APNs {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(production.Certificate())
	roots.AddCert(sandbox.Certificate())
	a, err := NewAPNs(APNsConfig{
		KeyFile: writeKey(t, key), KeyID: testKeyID, TeamID: testTeamID, Topic: testTopic,
		ProductionURL: production.URL, SandboxURL: sandbox.URL, RootCAs: roots, Now: now,
	})
	if err != nil {
		t.Fatalf("NewAPNs: %v", err)
	}
	return a
}

// TestAPNsSendsABackgroundPush: a push goes over HTTP/2 to the device's
// path, with the background headers, and a provider token APNs can check:
// ES256, signed by the .p8 key, naming the key and the team.
func TestAPNsSendsABackgroundPush(t *testing.T) {
	production, sandbox := newFakeAPNs(t), newFakeAPNs(t)
	key := newKey(t)
	now := time.Unix(1_800_000_000, 0)
	a := newTestAPNs(t, key, production, sandbox, func() time.Time { return now })

	payload := Payload("12", 3, 4)
	res, err := a.Send(context.Background(), Notification{Token: testToken, Environment: models.APNsProduction, Payload: payload})
	if err != nil || res.Status != http.StatusOK {
		t.Fatalf("Send = %+v, %v; want 200", res, err)
	}
	got := production.received()
	if len(got) != 1 || len(sandbox.received()) != 0 {
		t.Fatalf("production got %d pushes, sandbox %d; want 1 and 0", len(got), len(sandbox.received()))
	}
	req := got[0]
	if req.ProtoMajor != 2 {
		t.Errorf("push went over HTTP/%d, want HTTP/2", req.ProtoMajor)
	}
	if req.Method != http.MethodPost || req.Path != "/3/device/"+testToken {
		t.Errorf("push = %s %s, want POST /3/device/<token>", req.Method, req.Path)
	}
	for header, want := range map[string]string{
		"apns-push-type":  "background",
		"apns-priority":   "5",
		"apns-topic":      testTopic,
		"apns-expiration": strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		"content-type":    "application/json",
	} {
		if v := req.Header.Get(header); v != want {
			t.Errorf("%s = %q, want %q", header, v, want)
		}
	}
	if string(req.Body) != string(payload) {
		t.Errorf("body = %s, want %s", req.Body, payload)
	}

	bearer := strings.TrimPrefix(req.Header.Get("authorization"), "bearer ")
	parsed, err := jwt.Parse(bearer, func(tok *jwt.Token) (any, error) {
		if tok.Method != jwt.SigningMethodES256 {
			t.Errorf("provider token alg = %v, want ES256", tok.Header["alg"])
		}
		if tok.Header["kid"] != testKeyID {
			t.Errorf("provider token kid = %v, want %s", tok.Header["kid"], testKeyID)
		}
		return &key.PublicKey, nil
	}, jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("provider token does not verify with the key: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["iss"] != testTeamID {
		t.Errorf("provider token iss = %v, want the team id", claims["iss"])
	}
	if iat, _ := claims["iat"].(float64); int64(iat) != now.Unix() {
		t.Errorf("provider token iat = %v, want %d", claims["iat"], now.Unix())
	}
}

// TestAPNsUsesTheDevicesEnvironment: a sandbox token goes to the sandbox
// host.
func TestAPNsUsesTheDevicesEnvironment(t *testing.T) {
	production, sandbox := newFakeAPNs(t), newFakeAPNs(t)
	a := newTestAPNs(t, newKey(t), production, sandbox, nil)
	if _, err := a.Send(context.Background(), Notification{Token: testToken, Environment: models.APNsSandbox, Payload: Payload("1", 1, 0)}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.received()) != 1 || len(production.received()) != 0 {
		t.Fatalf("sandbox got %d pushes, production %d; want 1 and 0", len(sandbox.received()), len(production.received()))
	}
}

// TestAPNsProviderTokenIsReusedUnderAnHour: one provider token serves every
// push until it is 50 minutes old, then a new one is signed; one APNs says
// has expired is replaced at once.
func TestAPNsProviderTokenIsReusedUnderAnHour(t *testing.T) {
	production, sandbox := newFakeAPNs(t), newFakeAPNs(t)
	start := time.Unix(1_800_000_000, 0)
	now := start
	a := newTestAPNs(t, newKey(t), production, sandbox, func() time.Time { return now })
	send := func() string {
		t.Helper()
		if _, err := a.Send(context.Background(), Notification{Token: testToken, Payload: Payload("1", 1, 0)}); err != nil {
			t.Fatal(err)
		}
		got := production.received()
		return got[len(got)-1].Header.Get("authorization")
	}

	first := send()
	now = start.Add(49 * time.Minute)
	if send() != first {
		t.Error("a 49-minute-old provider token was replaced; it should be reused")
	}
	now = start.Add(51 * time.Minute)
	second := send()
	if second == first {
		t.Error("a 51-minute-old provider token was reused; it should be replaced")
	}

	production.mu.Lock()
	production.answer = func(string) (int, string) { return http.StatusForbidden, "ExpiredProviderToken" }
	production.mu.Unlock()
	if send() != second {
		t.Fatal("the refused push should still carry the cached token")
	}
	now = now.Add(time.Second)
	if send() == second {
		t.Error("a provider token APNs said had expired was used again")
	}
}

// TestAPNsResultGone: the answers that mean the token is of no further use,
// and only those, delete the device.
func TestAPNsResultGone(t *testing.T) {
	for _, c := range []struct {
		status int
		reason string
		gone   bool
	}{
		{http.StatusGone, "Unregistered", true},
		{http.StatusGone, "", true},
		{http.StatusBadRequest, "BadDeviceToken", true},
		{http.StatusBadRequest, "DeviceTokenNotForTopic", true},
		{http.StatusBadRequest, "BadTopic", false},
		{http.StatusTooManyRequests, "TooManyRequests", false},
		{http.StatusForbidden, "ExpiredProviderToken", false},
		{http.StatusServiceUnavailable, "ServiceUnavailable", false},
		{http.StatusOK, "", false},
	} {
		if got := (Result{Status: c.status, Reason: c.reason}).Gone(); got != c.gone {
			t.Errorf("Result{%d, %q}.Gone() = %v, want %v", c.status, c.reason, got, c.gone)
		}
	}
}

// TestPayloadIsOnlyTheSignal: the payload is a background wake-up and the
// device, account and unread count, and nothing else.
func TestPayloadIsOnlyTheSignal(t *testing.T) {
	const want = `{"aps":{"content-available":1},"restmail":{"v":1,"device":"12","account":3,"unread":4}}`
	if got := string(Payload("12", 3, 4)); got != want {
		t.Fatalf("Payload = %s, want %s", got, want)
	}
	var decoded map[string]any
	if err := json.Unmarshal(Payload("x", 1, 0), &decoded); err != nil {
		t.Fatal(err)
	}
	if keys := reflect.ValueOf(decoded).MapKeys(); len(keys) != 2 {
		t.Fatalf("payload has %d top-level keys, want aps and restmail only", len(keys))
	}
}

// TestParseAPNsKey: a .p8 must be a P-256 private key.
func TestParseAPNsKey(t *testing.T) {
	good, err := os.ReadFile(writeKey(t, newKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAPNsKey(good); err != nil {
		t.Errorf("a P-256 .p8 was refused: %v", err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongCurve, _ := os.ReadFile(writeKey(t, p384))
	for name, data := range map[string][]byte{
		"not PEM":     []byte("not a key"),
		"a P-384 key": wrongCurve,
		"a cert":      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}),
	} {
		if _, err := ParseAPNsKey(data); err == nil {
			t.Errorf("%s was accepted as an APNs key", name)
		}
	}
	if _, err := NewAPNs(APNsConfig{KeyFile: filepath.Join(t.TempDir(), "missing.p8"), KeyID: "k", TeamID: "t", Topic: "x"}); err == nil {
		t.Error("NewAPNs accepted a key file that does not exist")
	}
	if _, err := NewAPNs(APNsConfig{KeyID: "k", TeamID: "t", Topic: "x"}); err == nil {
		t.Error("NewAPNs accepted a configuration without a key file")
	}
}
