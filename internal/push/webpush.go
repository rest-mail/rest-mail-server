package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Web Push (#1572): a browser subscribes with its push service and hands the
// server the subscription — an endpoint URL, the browser's P-256 public key
// (p256dh) and a 16-byte authentication secret. The server POSTs each push to
// the endpoint (RFC 8030), authenticated with a VAPID token (RFC 8292), its
// body encrypted to the browser (RFC 8291, aes128gcm per RFC 8188), so the
// push service carries it without reading it. What is encrypted is the same
// signal APNs is sent (Payload): no message content.

// webPushRecordSize is the aes128gcm record size the header announces. One
// record holds the whole of a push.
const webPushRecordSize = 4096

// webPushTTL is how long the push service keeps a push it cannot deliver at
// once, in seconds: the hour APNs is given (pushExpiration).
var webPushTTL = strconv.Itoa(int(pushExpiration / time.Second))

// vapidTokenLifetime is how long one VAPID token is used. RFC 8292 §2 caps
// exp at 24 hours ahead; a token is signed per push service origin.
const (
	vapidTokenLifetime = 12 * time.Hour
	vapidTokenReuse    = 10 * time.Hour
)

// WebPushConfig is what the server needs to send Web Push: the VAPID private
// key (a PEM P-256 key file, mounted from a secret) and the contact the push
// services may reach the operator at (a mailto: or https: URI).
type WebPushConfig struct {
	KeyFile string
	Subject string

	// For tests: allow endpoints on private addresses (a local test server),
	// the client's TLS trust, and a clock.
	AllowPrivate bool
	Client       *http.Client
	Now          func() time.Time
}

// Configured reports whether everything Web Push needs is set.
func (c WebPushConfig) Configured() bool { return c.KeyFile != "" && c.Subject != "" }

// WebPush sends Web Push messages.
type WebPush struct {
	key       *ecdsa.PrivateKey
	publicKey string // base64url, uncompressed point: the applicationServerKey
	subject   string
	client    *http.Client
	now       func() time.Time

	mu     sync.Mutex
	tokens map[string]vapidToken // by push service origin
}

type vapidToken struct {
	jwt    string
	issued time.Time
}

// NewWebPush reads the VAPID key and builds the sender.
func NewWebPush(cfg WebPushConfig) (*WebPush, error) {
	if !cfg.Configured() {
		return nil, errors.New("web push is not configured: it needs the VAPID key file and subject")
	}
	if !strings.HasPrefix(cfg.Subject, "mailto:") && !strings.HasPrefix(cfg.Subject, "https://") {
		return nil, errors.New("the VAPID subject must be a mailto: or https: URI (RFC 8292 §2.1)")
	}
	data, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("read the VAPID key: %w", err)
	}
	key, err := ParseAPNsKey(data)
	if err != nil {
		return nil, fmt.Errorf("the VAPID key: %w", err)
	}
	pub, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, fmt.Errorf("the VAPID key: %w", err)
	}
	w := &WebPush{
		key:       key,
		publicKey: base64.RawURLEncoding.EncodeToString(pub.Bytes()),
		subject:   cfg.Subject,
		client:    cfg.Client,
		now:       cfg.Now,
		tokens:    map[string]vapidToken{},
	}
	if w.now == nil {
		w.now = time.Now
	}
	if w.client == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second, Control: publicOnly(cfg.AllowPrivate)}
		w.client = &http.Client{
			Timeout: sendTimeout,
			Transport: &http.Transport{
				DialContext:     dialer.DialContext,
				IdleConnTimeout: 10 * time.Minute,
			},
			// A push service answers; it never redirects a push elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return w, nil
}

// PublicKey is the VAPID public key, base64url: the applicationServerKey a
// browser subscribes with.
func (w *WebPush) PublicKey() string { return w.publicKey }

// publicOnly refuses to dial a non-public address: the endpoint is a URL a
// signed-in person handed the server, which must not reach anything internal.
func publicOnly(allowPrivate bool) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsPrivate() || ip.IsUnspecified() {
			return fmt.Errorf("web push: refusing to connect to non-public address %s", host)
		}
		return nil
	}
}

// vapid is the Authorization header for the push service at origin
// (RFC 8292 §3): "vapid t=<JWT>, k=<public key>".
func (w *WebPush) vapid(origin string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if t, ok := w.tokens[origin]; ok && now.Sub(t.issued) < vapidTokenReuse {
		return "vapid t=" + t.jwt + ", k=" + w.publicKey, nil
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"aud": origin,
		"exp": now.Add(vapidTokenLifetime).Unix(),
		"sub": w.subject,
	}).SignedString(w.key)
	if err != nil {
		return "", fmt.Errorf("sign the VAPID token: %w", err)
	}
	w.tokens[origin] = vapidToken{jwt: signed, issued: now}
	return "vapid t=" + signed + ", k=" + w.publicKey, nil
}

// Send posts n, encrypted to the subscription, to its endpoint. A 404 or 410
// says the subscription is gone (RFC 8030 §7.3), which Result.Gone reports.
func (w *WebPush) Send(ctx context.Context, n Notification) (Result, error) {
	endpoint, err := url.Parse(n.Token)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return Result{}, errors.New("web push: the endpoint is not an https URL")
	}
	uaPublic, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(n.P256dh, "="))
	if err != nil {
		return Result{}, fmt.Errorf("web push: the p256dh key: %w", err)
	}
	auth, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(n.Auth, "="))
	if err != nil {
		return Result{}, fmt.Errorf("web push: the auth secret: %w", err)
	}
	asPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return Result{}, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Result{}, err
	}
	body, err := EncryptWebPush(n.Payload, uaPublic, auth, asPrivate, salt)
	if err != nil {
		return Result{}, err
	}
	authorization, err := w.vapid(endpoint.Scheme + "://" + endpoint.Host)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("TTL", webPushTTL)
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := w.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	res := Result{Status: resp.StatusCode}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.Status = http.StatusOK
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		res.Reason = "Unregistered"
	default:
		res.Reason = resp.Status
	}
	return res, nil
}

// EncryptWebPush encrypts plaintext to a browser's subscription as RFC 8291
// §3 says: an ECDH secret between the ephemeral asPrivate and the browser's
// uaPublic, mixed with its auth secret, keys one aes128gcm record (RFC 8188)
// whose header carries salt, the record size and asPrivate's public key.
func EncryptWebPush(plaintext, uaPublic, auth []byte, asPrivate *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(auth) != 16 {
		return nil, errors.New("web push: the auth secret must be 16 bytes")
	}
	if len(salt) != 16 {
		return nil, errors.New("web push: the salt must be 16 bytes")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("web push: the p256dh key is not a P-256 point: %w", err)
	}
	secret, err := asPrivate.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := asPrivate.PublicKey().Bytes()

	// PRK_key = HMAC(auth, ecdh); IKM = HKDF-Expand(PRK_key, key_info, 32).
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPublic...), asPublic...)
	ikm := hkdfExpand(hmacSHA256(auth, secret), keyInfo, 32)
	// PRK = HMAC(salt, IKM); CEK and NONCE are expanded from it.
	prk := hmacSHA256(salt, ikm)
	cek := hkdfExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpand(prk, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// One record, the last: the plaintext and its 0x02 delimiter.
	record := append(append([]byte{}, plaintext...), 0x02)
	if len(record)+gcm.Overhead() > webPushRecordSize {
		return nil, errors.New("web push: the payload is too large for one record")
	}

	header := make([]byte, 0, 16+4+1+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, webPushRecordSize)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return gcm.Seal(header, nonce, record, nil), nil
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// hkdfExpand is HKDF-Expand (RFC 5869) for an output of at most one hash.
func hkdfExpand(prk, info []byte, length int) []byte {
	return hmacSHA256(prk, append(append([]byte{}, info...), 0x01))[:length]
}

// CheckP256dh checks a browser's p256dh key: base64url, an
// uncompressed P-256 point.
func CheckP256dh(s string) error {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return err
	}
	_, err = ecdh.P256().NewPublicKey(b)
	return err
}
