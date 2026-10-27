package e2e

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLimits spends every limit the API declares and checks that it answers as
// declared (#660): the next request gets the documented 429 with Retry-After,
// or the 413 or 400; the allowance comes back once Retry-After has passed; and
// a different key, or another limit's bucket, is unaffected.
//
// The limits are read from the served description, so a limit added to a
// route's registration is held by this suite without it being listed here: an
// operation that declares a 429 the suite cannot place fails it.
//
// It runs on its own (`chore test:limits`) against a freshly restarted API, so
// the in-memory buckets are its own and no other phase has spent them. Every
// test from the suite's container shares one client IP, so the per-IP limits
// are checked for isolation against each other's buckets, not against a
// second address.
func TestLimits(t *testing.T) {
	if os.Getenv("E2E_LIMITS") == "" {
		t.Skip("the limits suite spends the API's limits; run it with `chore test:limits` (E2E_LIMITS=1)")
	}
	waitForAPI(t, 3*time.Minute)

	l := newLimitsRun(t)
	// Ordered so that a phase never needs a bucket an earlier phase has spent
	// beyond what Retry-After gives back.
	t.Run("Declared", l.declared)
	t.Run("PageSize", l.pageSize)
	t.Run("Recipients", l.recipients)
	t.Run("Sends", l.sends)
	t.Run("EventStreams", l.eventStreams)
	t.Run("AuthThrottle", l.authThrottle)
	t.Run("RecoveryFailures", l.recoveryFailures)
	t.Run("OwnerThrottle", l.ownerThrottle)
	t.Run("TLSRPT", l.tlsrpt)
	t.Run("PipelineTestThrottle", l.pipelineTestThrottle)
	t.Run("APIKeyRefusals", l.apiKeyRefusals)
	t.Run("BasicRefusals", l.basicRefusals)
	t.Run("SendsComeBack", l.sendsComeBack)
	t.Run("BodyCaps", l.bodyCaps)
}

// The limit groups, by the description of the 429 their routes declare. A 429
// with any other description is a limit this suite does not know yet.
const (
	authThrottleShared  = "rate_limited: too many requests to the shared authentication throttle"
	authThrottleGrants  = "rate_limited: login's per-IP limit"
	ownerThrottleGroup  = "rate_limited: too many requests to the owner endpoints' throttle"
	tlsrptThrottleGroup = "rate_limited: too many reports from one address"
	pipelineTestGroup   = "rate_limited: pipeline preview limit exceeded"
	basicRefusalGroup   = "rate_limited: refused HTTP Basic credentials, throttled at login's rate"
	// handlerLimitGroup is the bare status text: what a route says when it
	// lists 429 among its errors. The limit is its handler's (handlerLimits),
	// or else the refused API keys' throttle in front of the admin routes.
	handlerLimitGroup = "Too Many Requests"
)

// handlerLimits are the operations whose handler answers 429 itself, and the
// limit each spends: the account's send rate (#184), and the address's
// recovery failures (#124). Every other operation that declares the bare 429
// gets it from the refused API keys' throttle (#1269).
var handlerLimits = map[string]string{
	"POST /api/v1/messages/send":            "sends",
	"POST /api/v1/messages/draft/{id}/send": "sends",
	"POST /api/v1/messages/{id}/forward":    "sends",
	"POST /api/v1/auth/recover":             "recovery",
}

// The defaults the suite expects the topology to run with (config.go).
const (
	listPageMax          = 200
	messagePageMax       = 100
	messagePageDefault   = 50
	maxRecipients        = 100
	sendsPerMinute       = 20
	streamsPerAccount    = 8
	recoveryFailures     = 5
	tlsrptMaxBodyBytes   = 512 << 10
	overEveryBodyCap     = 1 << 30
	unknownLimitsAddress = "nobody-limits@restmail.test"
	refusedAPIKey        = "rmk_limits-suite-not-a-key"
)

type limitsOperation struct {
	method, path string
}

func (o limitsOperation) String() string { return o.method + " " + o.path }

type limitsRun struct {
	admin, a, b    *apiClient
	aID, bID       uint
	aAddr, bAddr   string
	limits         map[string][]limitsOperation // 429 description → operations
	capped         []limitsOperation            // operations that declare a 413
	apiKeyLimited  []limitsOperation            // the bare 429s of the refused API keys' throttle
	sendsRetryFrom time.Time                    // when the send limit may be spent again
}

func newLimitsRun(t *testing.T) *limitsRun {
	t.Helper()
	l := &limitsRun{admin: newAPIClient(), aAddr: "limits-a@restmail.test", bAddr: "limits-b@restmail.test"}
	requireNoError(t, l.admin.loginAdmin())
	createMailbox(t, l.admin, l.aAddr, accountPassword, "Limits A")
	createMailbox(t, l.admin, l.bAddr, accountPassword, "Limits B")
	l.a, l.aID = restmailInbox(t, l.aAddr, accountPassword)
	l.b, l.bID = restmailInbox(t, l.bAddr, accountPassword)

	resp, err := httpClient.Get(apiBaseURL + "/api/docs/openapi.json")
	requireNoError(t, err)
	requireStatus(t, resp, http.StatusOK)
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	requireNoError(t, decodeJSON(resp, &doc))
	l.limits = map[string][]limitsOperation{}
	for path, item := range doc.Paths {
		for method, raw := range item {
			var op struct {
				Responses map[string]struct {
					Description string `json:"description"`
				} `json:"responses"`
			}
			if json.Unmarshal(raw, &op) != nil || op.Responses == nil {
				continue
			}
			o := limitsOperation{strings.ToUpper(method), path}
			if r, ok := op.Responses["429"]; ok {
				l.limits[r.Description] = append(l.limits[r.Description], o)
			}
			if _, ok := op.Responses["413"]; ok {
				l.capped = append(l.capped, o)
			}
		}
	}
	for _, ops := range l.limits {
		sortOperations(ops)
	}
	sortOperations(l.capped)
	return l
}

func sortOperations(ops []limitsOperation) {
	sort.Slice(ops, func(i, j int) bool { return ops[i].String() < ops[j].String() })
}

// declared places every declared 429 in a limit the suite spends.
func (l *limitsRun) declared(t *testing.T) {
	known := map[string]bool{authThrottleShared: true, authThrottleGrants: true, ownerThrottleGroup: true,
		tlsrptThrottleGroup: true, pipelineTestGroup: true, basicRefusalGroup: true, handlerLimitGroup: true}
	for description, ops := range l.limits {
		if !known[description] {
			t.Errorf("%d operations declare a 429 the limits suite does not spend (%q), e.g. %s: add the limit to tests/e2e/limits_test.go",
				len(ops), description, ops[0])
		}
	}
	for _, op := range l.limits[handlerLimitGroup] {
		if handlerLimits[op.String()] == "" {
			l.apiKeyLimited = append(l.apiKeyLimited, op)
		}
	}
	if len(l.limits[authThrottleShared]) == 0 || len(l.limits[basicRefusalGroup]) == 0 {
		t.Fatalf("the description declares no sign-in or HTTP Basic limit; was it served from the right API? %v", l.limits)
	}
}

// pageSize asks for more rows than a page may hold: a list is clamped to 200,
// and a folder's messages are 1 to 100, the default otherwise.
func (l *limitsRun) pageSize(t *testing.T) {
	contacts := make([]map[string]string, listPageMax+5)
	for i := range contacts {
		contacts[i] = map[string]string{"email": fmt.Sprintf("page-%03d@limits.example", i)}
	}
	resp, err := l.a.post(fmt.Sprintf("/api/v1/accounts/%d/contacts/import", l.aID), map[string]any{"contacts": contacts})
	requireNoError(t, err)
	requireSuccess(t, resp)
	resp.Body.Close()

	var page struct {
		Data       []json.RawMessage `json:"data"`
		Pagination struct {
			HasMore bool `json:"has_more"`
		} `json:"pagination"`
	}
	resp, err = l.a.get(fmt.Sprintf("/api/v1/accounts/%d/contacts?limit=%d", l.aID, listPageMax*5))
	requireNoError(t, err)
	requireStatus(t, resp, http.StatusOK)
	requireNoError(t, decodeJSON(resp, &page))
	if len(page.Data) != listPageMax || !page.Pagination.HasMore {
		t.Errorf("contacts with limit=%d: %d rows, has_more %v; want a page of %d and more to come",
			listPageMax*5, len(page.Data), page.Pagination.HasMore, listPageMax)
	}

	for i := 0; i <= messagePageMax+1; i++ {
		raw := fmt.Sprintf("From: page@limits.example\r\nTo: %s\r\nSubject: page %03d\r\nMessage-ID: <page-%03d@limits.example>\r\n"+
			"Date: Mon, 26 Oct 2026 10:00:00 +0000\r\n\r\nbody\r\n", l.aAddr, i, i)
		resp, err := l.admin.post(fmt.Sprintf("/api/v1/accounts/%d/messages/import", l.aID), map[string]any{
			"raw_message": base64.StdEncoding.EncodeToString([]byte(raw)), "folder": "INBOX",
			"internal_date": "2026-10-26T10:00:00Z"})
		requireNoError(t, err)
		requireSuccess(t, resp)
		resp.Body.Close()
	}
	for _, c := range []struct{ limit, want int }{{messagePageMax, messagePageMax}, {messagePageMax + 1, messagePageDefault}} {
		var messages struct {
			Data []json.RawMessage `json:"data"`
		}
		resp, err := l.a.get(fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages?limit=%d", l.aID, c.limit))
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
		requireNoError(t, decodeJSON(resp, &messages))
		if len(messages.Data) != c.want {
			t.Errorf("messages with limit=%d: %d rows, want %d", c.limit, len(messages.Data), c.want)
		}
	}
}

// recipients sends one message to one more recipient than a message may have.
func (l *limitsRun) recipients(t *testing.T) {
	to := make([]string, maxRecipients+1)
	for i := range to {
		to[i] = fmt.Sprintf("r%03d@limits.example", i)
	}
	resp, err := l.a.post("/api/v1/messages/send", map[string]any{"from": l.aAddr, "to": to, "subject": "too many", "body_text": "x"})
	requireNoError(t, err)
	requireRefusal(t, resp, http.StatusBadRequest, "too_many_recipients")
}

func (l *limitsRun) send(t *testing.T, c *apiClient, from, to string) *http.Response {
	t.Helper()
	resp, err := c.post("/api/v1/messages/send", map[string]any{"from": from, "to": []string{to}, "subject": "limits", "body_text": "x"})
	requireNoError(t, err)
	return resp
}

// sends spends account A's send rate; account B may still send.
func (l *limitsRun) sends(t *testing.T) {
	for i := 0; i < sendsPerMinute; i++ {
		resp := l.send(t, l.a, l.aAddr, l.bAddr)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("send %d of the %d a minute allows: %d %s", i+1, sendsPerMinute, resp.StatusCode, readBody(resp))
		}
		resp.Body.Close()
	}
	retry := requireRateLimited(t, l.send(t, l.a, l.aAddr, l.bAddr), "the send after the minute's allowance")
	l.sendsRetryFrom = time.Now().Add(retry)

	resp := l.send(t, l.b, l.bAddr, l.aAddr)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("another account's send while A's are spent: %d %s", resp.StatusCode, readBody(resp))
	} else {
		resp.Body.Close()
	}
}

// sendsComeBack sends as A again once the Retry-After of sends has passed.
func (l *limitsRun) sendsComeBack(t *testing.T) {
	if l.sendsRetryFrom.IsZero() {
		t.Skip("the send limit was not spent")
	}
	time.Sleep(time.Until(l.sendsRetryFrom) + time.Second)
	resp := l.send(t, l.a, l.aAddr, l.bAddr)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("a send after Retry-After: %d %s", resp.StatusCode, readBody(resp))
	} else {
		resp.Body.Close()
	}
}

// eventStreams opens as many streams as an account may hold; the next is
// refused until one closes, and another account's is not.
func (l *limitsRun) eventStreams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := func(c *apiClient, id uint) *http.Response {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/accounts/%d/events", apiBaseURL, id), nil)
		requireNoError(t, err)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := streamClient.Do(req)
		requireNoError(t, err)
		return resp
	}
	var open []*http.Response
	defer func() {
		for _, resp := range open {
			resp.Body.Close()
		}
	}()
	for i := 0; i < streamsPerAccount; i++ {
		resp := stream(l.a, l.aID)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d of %d: %d %s", i+1, streamsPerAccount, resp.StatusCode, readBody(resp))
		}
		open = append(open, resp)
	}
	requireRefusal(t, stream(l.a, l.aID), http.StatusServiceUnavailable, "too_many_streams")

	other := stream(l.b, l.bID)
	if other.StatusCode != http.StatusOK {
		t.Errorf("another account's stream while A holds %d: %d %s", streamsPerAccount, other.StatusCode, readBody(other))
	} else {
		open = append(open, other)
	}

	open[0].Body.Close()
	open = open[1:]
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp := stream(l.a, l.aID)
		if resp.StatusCode == http.StatusOK {
			open = append(open, resp)
			return
		}
		body := readBody(resp)
		if time.Now().After(deadline) {
			t.Fatalf("a stream after one of A's closed: %d %s", resp.StatusCode, body)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// streamClient holds event streams open past httpClient's timeout.
var streamClient = &http.Client{Transport: httpClient.Transport}

// authThrottle spends the sign-in bucket and checks every operation that
// shares it, then the buckets beside it, then that it comes back.
func (l *limitsRun) authThrottle(t *testing.T) {
	spend := func() *http.Response {
		resp, err := newAPIClient().post("/api/v1/auth/login", map[string]string{"email": unknownLimitsAddress, "password": "not the password"})
		requireNoError(t, err)
		return resp
	}
	ops := append(append([]limitsOperation{}, l.limits[authThrottleShared]...), l.limits[authThrottleGrants]...)
	retry := l.spendThenProbe(t, "the sign-in throttle", spend, ops, func(op limitsOperation) *http.Response {
		return l.call(t, l.a, op, nil)
	})

	// Its neighbours keep their own buckets.
	l.requireNotLimited(t, "the owner endpoints' bucket", l.call(t, nil, limitsOperation{"GET", "/api/v1/server"}, nil))
	l.requireNotLimited(t, "the pipeline preview bucket", l.call(t, l.admin, limitsOperation{"POST", "/api/v1/admin/pipelines/test"}, map[string]any{}))

	time.Sleep(retry + 250*time.Millisecond)
	if err := newAPIClient().rawLogin(l.aAddr, accountPassword); err != nil {
		t.Errorf("a sign-in after Retry-After: %v", err)
	}
}

// recoveryFailures spends one address's recovery failures, from an IP whose
// sign-in bucket is not spent; another address may still try.
func (l *limitsRun) recoveryFailures(t *testing.T) {
	time.Sleep(10 * time.Second) // the sign-in bucket refills; each try spends from it too
	recoverAs := func(address string) *http.Response {
		resp, err := newAPIClient().post("/api/v1/auth/recover", map[string]string{"email": address,
			"totp_code": "000000", "recovery_code": "not-a-recovery-code", "new_password": "a new password 1234"})
		requireNoError(t, err)
		return resp
	}
	spent, other := "recover-limits-a@restmail.test", "recover-limits-b@restmail.test"
	for i := 0; i < recoveryFailures; i++ {
		requireRefusal(t, recoverAs(spent), http.StatusUnauthorized, "recovery_failed")
	}
	retry := requireRateLimited(t, recoverAs(spent), "the recovery after five refusals")
	if retry < time.Minute {
		t.Errorf("Retry-After %s after five refused recoveries; the budget gives one back every 15 minutes", retry)
	}
	// One back every 15 minutes is too long to wait for in CI: the unit tests
	// hold the refill (middleware.FailureBudget).
	requireRefusal(t, recoverAs(other), http.StatusUnauthorized, "recovery_failed")
}

// ownerThrottle spends the owner endpoints' bucket; sign-in keeps its own.
func (l *limitsRun) ownerThrottle(t *testing.T) {
	spend := func() *http.Response { return l.call(t, nil, limitsOperation{"GET", "/api/v1/server"}, nil) }
	retry := l.spendThenProbe(t, "the owner endpoints' throttle", spend, l.limits[ownerThrottleGroup], func(op limitsOperation) *http.Response {
		return l.call(t, nil, op, map[string]any{})
	})
	if err := newAPIClient().rawLogin(l.aAddr, accountPassword); err != nil {
		t.Errorf("a sign-in while the owner endpoints' bucket is spent: %v", err)
	}
	time.Sleep(retry + 250*time.Millisecond)
	l.requireNotLimited(t, "GET /api/v1/server after Retry-After", spend())
}

// tlsrpt spends TLS-RPT ingestion's bucket, then sends one report over its
// body cap.
func (l *limitsRun) tlsrpt(t *testing.T) {
	report := limitsOperation{"POST", "/.well-known/smtp-tlsrpt"}
	spend := func() *http.Response { return l.call(t, nil, report, map[string]any{}) }
	retry := l.spendThenProbe(t, "the TLS-RPT throttle", spend, l.limits[tlsrptThrottleGroup], func(op limitsOperation) *http.Response {
		return l.call(t, nil, op, map[string]any{})
	})
	l.requireNotLimited(t, "the sign-in bucket", l.call(t, nil, limitsOperation{"POST", "/api/v1/auth/refresh"}, map[string]any{}))
	time.Sleep(retry + 250*time.Millisecond)

	big := `{"organization-name":"` + strings.Repeat("a", tlsrptMaxBodyBytes) + `"}`
	req, err := http.NewRequest(http.MethodPost, apiBaseURL+report.path, strings.NewReader(big))
	requireNoError(t, err)
	req.Header.Set("Content-Type", "application/tlsrpt+json")
	resp, err := httpClient.Do(req)
	requireNoError(t, err)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a report over 512 KiB: %d %s", resp.StatusCode, readBody(resp))
	} else {
		resp.Body.Close()
	}
}

// pipelineTestThrottle spends the pipeline previews' bucket as the admin.
func (l *limitsRun) pipelineTestThrottle(t *testing.T) {
	spend := func() *http.Response {
		return l.call(t, l.admin, limitsOperation{"POST", "/api/v1/admin/pipelines/test"}, map[string]any{})
	}
	retry := l.spendThenProbe(t, "the pipeline preview throttle", spend, l.limits[pipelineTestGroup], func(op limitsOperation) *http.Response {
		return l.call(t, l.admin, op, map[string]any{})
	})
	l.requireNotLimited(t, "the owner endpoints' bucket", l.call(t, nil, limitsOperation{"GET", "/api/v1/server"}, nil))
	time.Sleep(retry + 250*time.Millisecond)
	l.requireNotLimited(t, "a preview after Retry-After", spend())
}

// apiKeyRefusals spends the refused API keys of this IP, and checks the
// operations whose bare 429 is that throttle's. HTTP Basic refusals and
// sessions keep their own buckets.
func (l *limitsRun) apiKeyRefusals(t *testing.T) {
	withKey := func(op limitsOperation) *http.Response {
		return l.call(t, &apiClient{token: refusedAPIKey}, op, map[string]any{})
	}
	spend := func() *http.Response { return withKey(limitsOperation{"GET", "/api/v1/admin/domains"}) }
	retry := l.spendThenProbe(t, "refused API keys", spend, l.apiKeyLimited, withKey)
	l.requireNotLimited(t, "a session on the same routes", l.call(t, l.admin, limitsOperation{"GET", "/api/v1/admin/domains"}, nil))
	req, err := http.NewRequest(http.MethodGet, apiBaseURL+"/api/v1/accounts", nil)
	requireNoError(t, err)
	req.SetBasicAuth(unknownLimitsAddress, "not the password")
	resp, err := httpClient.Do(req)
	requireNoError(t, err)
	l.requireNotLimited(t, "the refused HTTP Basic credentials' bucket", resp)
	time.Sleep(retry + 250*time.Millisecond)
	if resp := spend(); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a refused API key after Retry-After: %d %s, want 401", resp.StatusCode, readBody(resp))
	} else {
		resp.Body.Close()
	}
}

// basicRefusals spends the refused HTTP Basic credentials of this IP, and
// checks every operation that takes HTTP Basic.
func (l *limitsRun) basicRefusals(t *testing.T) {
	basic := func(op limitsOperation) *http.Response {
		req, err := http.NewRequest(op.method, apiBaseURL+limitsPath(op.path, l.aID), strings.NewReader("{}"))
		requireNoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(unknownLimitsAddress, "not the password")
		resp, err := httpClient.Do(req)
		requireNoError(t, err)
		return resp
	}
	spend := func() *http.Response { return basic(limitsOperation{"GET", "/api/v1/accounts"}) }
	retry := l.spendThenProbe(t, "refused HTTP Basic credentials", spend, l.limits[basicRefusalGroup], basic)
	l.requireNotLimited(t, "a session on the same routes", l.call(t, l.a, limitsOperation{"GET", "/api/v1/accounts"}, nil))
	time.Sleep(retry + 250*time.Millisecond)
	if resp := spend(); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a refused credential after Retry-After: %d %s, want 401", resp.StatusCode, readBody(resp))
	} else {
		resp.Body.Close()
	}
}

// bodyCaps declares a body over every cap to each operation that declares a
// 413, and expects the 413 before a byte of it is sent. TLS-RPT reads before
// it refuses, so it is held with a real body (tlsrpt). The requests are A's,
// not the admin's: the API's cap comes before a route's capability check, so
// an operation without the cap refuses A instead of acting on a missing body.
func (l *limitsRun) bodyCaps(t *testing.T) {
	u, err := url.Parse(apiBaseURL)
	requireNoError(t, err)
	for _, op := range l.capped {
		if op.path == "/.well-known/smtp-tlsrpt" {
			continue
		}
		status, err := declareOversizedBody(u.Host, op.method, limitsPath(op.path, l.aID), l.a.token)
		if err != nil {
			t.Errorf("%s: %v", op, err)
			continue
		}
		if status != http.StatusRequestEntityTooLarge {
			t.Errorf("%s with a declared %d-byte body: %d, want 413", op, overEveryBodyCap, status)
		}
	}
}

// declareOversizedBody sends a request whose Content-Length is over every cap,
// without its body, and reads the status the server answers before the body.
func declareOversizedBody(host, method, path, token string) (int, error) {
	conn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n",
		method, path, host, token, overEveryBodyCap)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return 0, fmt.Errorf("no answer before the body was sent: %w", err)
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// spendThenProbe spends a bucket with spend until it answers 429, then, for
// each operation that declares the same limit, spends it again and sends the
// operation, which must be refused alike. It returns the longest Retry-After.
func (l *limitsRun) spendThenProbe(t *testing.T, limit string, spend func() *http.Response, ops []limitsOperation,
	probe func(limitsOperation) *http.Response) time.Duration {
	t.Helper()
	if len(ops) == 0 {
		t.Fatalf("no operation declares %s", limit)
	}
	exhaust := func() time.Duration {
		for i := 0; i < 200; i++ {
			resp := spend()
			if resp.StatusCode == http.StatusTooManyRequests {
				return requireRateLimited(t, resp, limit)
			}
			resp.Body.Close()
		}
		t.Fatalf("%s answered no 429 in 200 requests: is the limit off?", limit)
		return 0
	}
	longest := exhaust()
	for _, op := range ops {
		exhaust()
		resp := probe(op)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Errorf("%s while %s is spent: %d %s, want 429", op, limit, resp.StatusCode, readBody(resp))
			continue
		}
		longest = max(longest, requireRateLimited(t, resp, op.String()))
	}
	return longest
}

// call sends op as c (anonymously when c is nil), with its path parameters
// filled with A's account id.
func (l *limitsRun) call(t *testing.T, c *apiClient, op limitsOperation, body any) *http.Response {
	t.Helper()
	var payload string
	if body != nil {
		raw, err := json.Marshal(body)
		requireNoError(t, err)
		payload = string(raw)
	}
	req, err := http.NewRequest(op.method, apiBaseURL+limitsPath(op.path, l.aID), strings.NewReader(payload))
	requireNoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := httpClient.Do(req)
	requireNoError(t, err)
	return resp
}

func (l *limitsRun) requireNotLimited(t *testing.T, what string, resp *http.Response) {
	t.Helper()
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Errorf("%s answered 429 (Retry-After %s): it shares a bucket it should not", what, resp.Header.Get("Retry-After"))
	}
	resp.Body.Close()
}

var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

// limitsPath fills a templated path: a folder is INBOX, any other parameter
// the account id.
func limitsPath(path string, id uint) string {
	return pathParameter.ReplaceAllStringFunc(path, func(p string) string {
		if p == "{folder}" {
			return "INBOX"
		}
		return strconv.FormatUint(uint64(id), 10)
	})
}

// requireRateLimited checks a 429 rate_limited with a whole-second Retry-After
// and returns it.
func requireRateLimited(t *testing.T, resp *http.Response, what string) time.Duration {
	t.Helper()
	header := resp.Header.Get("Retry-After")
	requireRefusal(t, resp, http.StatusTooManyRequests, "rate_limited")
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds < 1 {
		t.Fatalf("%s: Retry-After %q, want whole seconds of at least 1", what, header)
	}
	return time.Duration(seconds) * time.Second
}

// requireRefusal checks a response's status and error code.
func requireRefusal(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	body := readBody(resp)
	var refusal struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &refusal)
	if resp.StatusCode != status || refusal.Error.Code != code {
		t.Fatalf("%d %s, want %d %s", resp.StatusCode, body, status, code)
	}
}
