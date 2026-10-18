// Package push tells phones that mail has arrived (#363). A phone registers
// with the session it is signed in with; when mail reaches the inbox of a
// mailbox that session lists, the phone gets a background push saying only
// which account to fetch and its unread count. The app then fetches the mail
// over the API itself, so none of it passes through Apple.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/db/models"
)

// APNs hosts, by the environment a device's token comes from.
const (
	APNsProductionURL = "https://api.push.apple.com"
	APNsSandboxURL    = "https://api.sandbox.push.apple.com"
)

// providerTokenLifetime is how long one provider token (the JWT that
// authenticates the server to APNs) is used. APNs refuses one older than an
// hour, and one replaced more often than every 20 minutes.
const providerTokenLifetime = 50 * time.Minute

// pushExpiration is how long APNs keeps trying a push it cannot deliver at
// once. A signal to fetch mail is worth little after an hour.
const pushExpiration = time.Hour

// sendTimeout bounds one request to APNs.
const sendTimeout = 15 * time.Second

// APNsConfig is what the server needs to push through APNs with token auth:
// the .p8 key Apple issued, its key id, the team it belongs to, and the topic
// (the app's bundle id).
type APNsConfig struct {
	KeyFile string
	KeyID   string
	TeamID  string
	Topic   string

	// For tests: other hosts, the CAs to trust for them, and a clock. Empty
	// means Apple's hosts, the system's CAs and time.Now.
	ProductionURL string
	SandboxURL    string
	RootCAs       *x509.CertPool
	Now           func() time.Time
}

// Configured reports whether everything APNs needs is set. Push is off
// otherwise.
func (c APNsConfig) Configured() bool {
	return c.KeyFile != "" && c.KeyID != "" && c.TeamID != "" && c.Topic != ""
}

// Notification is one push to one device.
type Notification struct {
	// Token is the device's APNs token, lowercase hex.
	Token string
	// Environment is models.APNsProduction or models.APNsSandbox.
	Environment string
	Payload     []byte
}

// Result is how APNs answered a push: the HTTP status, and its reason when
// it refused one.
type Result struct {
	Status int
	Reason string
}

// Gone reports whether APNs said the token is of no further use for this
// topic: the app was removed, or the token was never valid for it. The device
// should be deleted.
func (r Result) Gone() bool {
	switch {
	case r.Status == http.StatusGone, r.Reason == "Unregistered":
		return true
	case r.Reason == "BadDeviceToken", r.Reason == "DeviceTokenNotForTopic":
		return true
	}
	return false
}

// APNs sends background pushes over HTTP/2, authenticated with an ES256
// provider token signed with the .p8 key, reused for under an hour.
type APNs struct {
	keyID, teamID, topic   string
	key                    *ecdsa.PrivateKey
	productionURL, sandbox string
	client                 *http.Client
	now                    func() time.Time

	mu     sync.Mutex
	token  string
	issued time.Time
}

// NewAPNs reads the .p8 key and builds the sender. It fails on a key it
// cannot read or that is not an ES256 (P-256) private key.
func NewAPNs(cfg APNsConfig) (*APNs, error) {
	if !cfg.Configured() {
		return nil, errors.New("APNs is not configured: it needs the key file, key id, team id and topic")
	}
	data, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("read the APNs key: %w", err)
	}
	key, err := ParseAPNsKey(data)
	if err != nil {
		return nil, err
	}
	a := &APNs{
		keyID:         cfg.KeyID,
		teamID:        cfg.TeamID,
		topic:         cfg.Topic,
		key:           key,
		productionURL: cfg.ProductionURL,
		sandbox:       cfg.SandboxURL,
		now:           cfg.Now,
	}
	if a.productionURL == "" {
		a.productionURL = APNsProductionURL
	}
	if a.sandbox == "" {
		a.sandbox = APNsSandboxURL
	}
	if a.now == nil {
		a.now = time.Now
	}
	// HTTP/2 only: APNs speaks nothing else.
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	a.client = &http.Client{
		Timeout: sendTimeout,
		Transport: &http.Transport{
			Protocols:       protocols,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.RootCAs},
			IdleConnTimeout: 10 * time.Minute,
		},
	}
	return a, nil
}

// ParseAPNsKey reads the private key from a .p8 file: PEM, PKCS #8, on the
// P-256 curve.
func ParseAPNsKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("the APNs key is not PEM: expected the .p8 file Apple issued")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("the APNs key is a %q PEM block, not a private key", block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("parse the APNs key: %w", err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("the APNs key is not a P-256 key, which ES256 needs")
	}
	return ec, nil
}

// Topic is the bundle id pushes are addressed to.
func (a *APNs) Topic() string { return a.topic }

// providerToken is the JWT that authenticates the server to APNs, made again
// once it is providerTokenLifetime old.
func (a *APNs) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if a.token != "" && now.Sub(a.issued) < providerTokenLifetime {
		return a.token, nil
	}
	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": a.teamID,
		"iat": now.Unix(),
	})
	t.Header["kid"] = a.keyID
	signed, err := t.SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("sign the APNs provider token: %w", err)
	}
	a.token, a.issued = signed, now
	return signed, nil
}

// dropProviderToken makes the next push sign a new provider token: APNs
// said this one has expired or is invalid.
func (a *APNs) dropProviderToken(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token == token {
		a.token = ""
	}
}

// Send pushes n as a low-priority background notification. An error means
// APNs was not reached or did not answer; a refusal is a Result.
func (a *APNs) Send(ctx context.Context, n Notification) (Result, error) {
	token, err := a.providerToken()
	if err != nil {
		return Result{}, err
	}
	host := a.productionURL
	if n.Environment == models.APNsSandbox {
		host = a.sandbox
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		host+"/3/device/"+url.PathEscape(n.Token), bytes.NewReader(n.Payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("authorization", "bearer "+token)
	req.Header.Set("apns-push-type", "background")
	// Background pushes must go at priority 5; APNs refuses 10 for them.
	req.Header.Set("apns-priority", "5")
	req.Header.Set("apns-topic", a.topic)
	req.Header.Set("apns-expiration", strconv.FormatInt(a.now().Add(pushExpiration).Unix(), 10))
	req.Header.Set("content-type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	res := Result{Status: resp.StatusCode}
	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return res, nil
	}
	var refusal struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&refusal)
	res.Reason = refusal.Reason
	if res.Reason == "ExpiredProviderToken" || res.Reason == "InvalidProviderToken" {
		a.dropProviderToken(token)
	}
	return res, nil
}

// Payload is the whole of a push: a background wake-up, and which device,
// account and unread count it is for. It is built from these three values
// alone, so nothing of a message (a sender, a subject, an address, an id, any
// text) can ever be in it.
func Payload(device string, account uint, unread int64) []byte {
	type aps struct {
		ContentAvailable int `json:"content-available"`
	}
	type signal struct {
		V       int    `json:"v"`
		Device  string `json:"device"`
		Account uint   `json:"account"`
		Unread  int64  `json:"unread"`
	}
	body, _ := json.Marshal(struct {
		APS      aps    `json:"aps"`
		RestMail signal `json:"restmail"`
	}{
		APS:      aps{ContentAvailable: 1},
		RestMail: signal{V: 1, Device: device, Account: account, Unread: unread},
	})
	return body
}
