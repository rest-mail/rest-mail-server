package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// TestEncryptWebPushRFC8291Vector encrypts RFC 8291 Appendix A's message with
// its keys and salt, and gets its encrypted message byte for byte.
func TestEncryptWebPushRFC8291Vector(t *testing.T) {
	plaintext := []byte("When I grow up, I want to be a watermelon")
	asPrivate, err := ecdh.P256().NewPrivateKey(b64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(asPrivate.PublicKey().Bytes()); got !=
		"BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("application server public key = %s", got)
	}
	uaPublic := b64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	auth := b64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := b64(t, "DGv6ra1nlYgDCS1FRnbzlw")

	got, err := EncryptWebPush(plaintext, uaPublic, auth, asPrivate, salt)
	if err != nil {
		t.Fatal(err)
	}
	const want = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if enc := base64.RawURLEncoding.EncodeToString(got); enc != want {
		t.Fatalf("encrypted message\n got %s\nwant %s", enc, want)
	}

	// And the user agent's side decrypts it.
	uaPrivate, err := ecdh.P256().NewPrivateKey(b64(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	if err != nil {
		t.Fatal(err)
	}
	if plain := decryptWebPush(t, got, uaPrivate, auth); string(plain) != string(plaintext) {
		t.Fatalf("decrypted %q", plain)
	}
}

// decryptWebPush is the user agent's side of RFC 8291, for checking what the
// sender posts.
func decryptWebPush(t *testing.T, body []byte, uaPrivate *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body too short: %d", len(body))
	}
	salt := body[:16]
	if rs := binary.BigEndian.Uint32(body[16:20]); rs != webPushRecordSize {
		t.Fatalf("record size %d", rs)
	}
	idlen := int(body[20])
	asPublicBytes := body[21 : 21+idlen]
	asPublic, err := ecdh.P256().NewPublicKey(asPublicBytes)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := uaPrivate.ECDH(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	uaPublic := uaPrivate.PublicKey().Bytes()
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPublic...), asPublicBytes...)
	ikm := hkdfExpand(hmacSHA256(auth, secret), keyInfo, 32)
	prk := hmacSHA256(salt, ikm)
	block, _ := aes.NewCipher(hkdfExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16))
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, hkdfExpand(prk, []byte("Content-Encoding: nonce\x00"), 12), body[21+idlen:], nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatalf("no last-record delimiter")
	}
	return plain[:len(plain)-1]
}

// vapidKeyFile writes a fresh P-256 key, PKCS #8 PEM, for a test.
func vapidKeyFile(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "vapid.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, key
}

type subscription struct {
	private *ecdh.PrivateKey
	auth    []byte
}

func newSubscription(t *testing.T) subscription {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return subscription{private: k, auth: auth}
}

func (s subscription) notification(endpoint string, payload []byte) Notification {
	return Notification{
		Platform: models.PushPlatformWebPush,
		Token:    endpoint,
		Payload:  payload,
		P256dh:   base64.RawURLEncoding.EncodeToString(s.private.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(s.auth),
	}
}

type posted struct {
	header http.Header
	path   string
	body   []byte
}

func webPushServer(t *testing.T, status int) (*httptest.Server, chan posted) {
	t.Helper()
	got := make(chan posted, 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- posted{header: r.Header.Clone(), path: r.URL.Path, body: body}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newTestWebPush(t *testing.T, srv *httptest.Server, now time.Time) (*WebPush, *ecdsa.PrivateKey) {
	t.Helper()
	keyFile, key := vapidKeyFile(t)
	w, err := NewWebPush(WebPushConfig{
		KeyFile: keyFile, Subject: "mailto:postmaster@example.com",
		AllowPrivate: true, Client: srv.Client(), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return w, key
}

// TestWebPushSendPostsEncryptedSignal: the sender POSTs to the endpoint with
// RFC 8030/8292/8291's headers, a VAPID token for the push service's origin,
// and a body the browser decrypts to the APNs signal and nothing more.
func TestWebPushSendPostsEncryptedSignal(t *testing.T) {
	srv, got := webPushServer(t, http.StatusCreated)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	w, key := newTestWebPush(t, srv, now)
	sub := newSubscription(t)

	payload := Payload("42", 7, 3)
	res, err := w.Send(context.Background(), sub.notification(srv.URL+"/push/abc", payload))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != http.StatusOK || res.Gone() {
		t.Fatalf("result %+v", res)
	}
	p := <-got
	if p.path != "/push/abc" {
		t.Fatalf("path %q", p.path)
	}
	for h, want := range map[string]string{
		"Content-Encoding": "aes128gcm",
		"Content-Type":     "application/octet-stream",
		"Ttl":              "3600",
		"Urgency":          "normal",
	} {
		if v := p.header.Get(h); v != want {
			t.Errorf("%s = %q, want %q", h, v, want)
		}
	}

	// Authorization: vapid t=<JWT>, k=<public key> (RFC 8292 §3).
	authz := p.header.Get("Authorization")
	if !strings.HasPrefix(authz, "vapid t=") {
		t.Fatalf("Authorization %q", authz)
	}
	parts := strings.SplitN(strings.TrimPrefix(authz, "vapid t="), ", k=", 2)
	if len(parts) != 2 || parts[1] != w.PublicKey() {
		t.Fatalf("Authorization %q", authz)
	}
	pub, _ := key.PublicKey.ECDH()
	if parts[1] != base64.RawURLEncoding.EncodeToString(pub.Bytes()) {
		t.Fatal("k is not the VAPID public key")
	}
	var claims jwt.MapClaims
	tok, err := jwt.NewParser(jwt.WithValidMethods([]string{"ES256"}), jwt.WithTimeFunc(func() time.Time { return now })).
		ParseWithClaims(parts[0], &claims, func(*jwt.Token) (any, error) { return &key.PublicKey, nil })
	if err != nil || !tok.Valid {
		t.Fatalf("VAPID token: %v", err)
	}
	if claims["aud"] != srv.URL {
		t.Errorf("aud = %v, want the push service's origin %s", claims["aud"], srv.URL)
	}
	if claims["sub"] != "mailto:postmaster@example.com" {
		t.Errorf("sub = %v", claims["sub"])
	}
	exp, _ := claims.GetExpirationTime()
	if exp == nil || exp.After(now.Add(24*time.Hour)) || !exp.After(now) {
		t.Errorf("exp = %v: RFC 8292 wants it within 24 hours", exp)
	}
	if len(claims) != 3 {
		t.Errorf("claims %v: only aud, exp and sub", claims)
	}

	// The body decrypts to exactly the APNs signal: no message content.
	plain := decryptWebPush(t, p.body, sub.private, sub.auth)
	if !bytes.Equal(plain, payload) {
		t.Fatalf("decrypted %s, want %s", plain, payload)
	}
	var signal map[string]any
	if err := json.Unmarshal(plain, &signal); err != nil {
		t.Fatal(err)
	}
	rm, _ := signal["restmail"].(map[string]any)
	for k := range rm {
		switch k {
		case "v", "device", "account", "unread":
		default:
			t.Errorf("the push carries %q", k)
		}
	}
}

// TestWebPushGoneSubscription: a 404 or 410 from the push service says the
// subscription is gone (RFC 8030 §7.3); another refusal does not.
func TestWebPushGoneSubscription(t *testing.T) {
	for status, gone := range map[int]bool{
		http.StatusGone: true, http.StatusNotFound: true,
		http.StatusTooManyRequests: false, http.StatusBadRequest: false,
	} {
		srv, got := webPushServer(t, status)
		w, _ := newTestWebPush(t, srv, time.Now())
		res, err := w.Send(context.Background(), newSubscription(t).notification(srv.URL+"/s", Payload("1", 1, 1)))
		if err != nil {
			t.Fatal(err)
		}
		<-got
		if res.Gone() != gone {
			t.Errorf("status %d: Gone() = %v, want %v", status, res.Gone(), gone)
		}
	}
}

// TestWebPushRefusesNonHTTPSAndPrivate: the endpoint is a URL a person gave
// the server; plain http and an internal address are never dialled.
func TestWebPushRefusesNonHTTPSAndPrivate(t *testing.T) {
	keyFile, _ := vapidKeyFile(t)
	w, err := NewWebPush(WebPushConfig{KeyFile: keyFile, Subject: "https://mail.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	sub := newSubscription(t)
	for _, endpoint := range []string{"http://push.example.com/x", "https://127.0.0.1:1/x", "https://10.0.0.1:1/x"} {
		if _, err := w.Send(context.Background(), sub.notification(endpoint, Payload("1", 1, 1))); err == nil {
			t.Errorf("%s: sent", endpoint)
		}
	}
}

func TestWebPushConfigRefusesBadSubject(t *testing.T) {
	keyFile, _ := vapidKeyFile(t)
	if _, err := NewWebPush(WebPushConfig{KeyFile: keyFile, Subject: "postmaster@example.com"}); err == nil {
		t.Fatal("a subject that is not mailto: or https: was accepted")
	}
}

// TestSendersRoutesByPlatform: each push goes through its platform's sender.
func TestSendersRoutesByPlatform(t *testing.T) {
	apns := &fakeSender{sent: make(chan Notification, 1), result: Result{Status: 200}}
	web := &fakeSender{sent: make(chan Notification, 1), result: Result{Status: 200}}
	s := Senders{models.PushPlatformAPNs: apns, models.PushPlatformWebPush: web}
	if _, err := s.Send(context.Background(), Notification{Platform: models.PushPlatformWebPush}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(context.Background(), Notification{Platform: "fcm"}); err == nil {
		t.Fatal("a platform with no sender was sent")
	}
	if len(web.sent) != 1 || len(apns.sent) != 0 {
		t.Fatalf("web %d, apns %d", len(web.sent), len(apns.sent))
	}
}
