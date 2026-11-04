package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// The reference server that judges what REST Mail sends. e2e:up gives it the
// overlay in testdata/rspamd-verdict, which turns on its checks and has it write
// them into each message it accepts.
const (
	verdictReceiver  = "mail2.test"
	verdictRecipient = "charlie@mail2.test"
	verdictDomain    = "restmail.test"
	// An account the stages before this one already use: a new one would move
	// the ids of every account the contract run makes after them.
	verdictSender = "testuser@restmail.test"
	// How long a sent message has to reach the receiver. The queue is shared
	// with the stages before this one.
	verdictArrival = 120 * time.Second
)

// testStage15ReceiverVerdict sends mail out through REST Mail's real path, and
// asserts what the receiving server made of it. The verdict is the receiver's own
// Authentication-Results, read over the receiver's IMAP: REST Mail plays no part
// in it. A signature that breaks while its DNS records stay intact fails here.
func testStage15ReceiverVerdict(t *testing.T) {
	admin := newAPIClient()
	if err := admin.loginAdmin(); err != nil {
		skipOrFail(t, "Cannot get admin token: %v", err)
	}
	createMailbox(t, admin, verdictSender, accountPassword, "GW Test User")
	sender := newAPIClient()
	requireNoError(t, sender.login(verdictSender, accountPassword))

	pass := receiverVerdict{
		receiver: verdictReceiver,
		domain:   verdictDomain,
		selector: dkimSelector(t, admin, verdictDomain),
		mailFrom: verdictSender,
		dkim:     "pass",
		spf:      "pass",
		dmarc:    "pass",
	}

	// The API signs a message as it queues it; the queue worker signs one that
	// arrived unsigned over SMTP submission. Both have to come out right.
	var delivered []byte
	var sentSubject string
	t.Run("API_Send", func(t *testing.T) {
		sentSubject = fmt.Sprintf("receiver-verdict-api-%d", time.Now().UnixNano())
		resp, err := sender.post("/api/v1/messages/send", map[string]any{
			"from":      verdictSender,
			"to":        []string{verdictRecipient},
			"subject":   sentSubject,
			"body_text": "What does the receiver make of this?",
		})
		requireNoError(t, err)
		requireSuccess(t, resp)
		resp.Body.Close()

		delivered = fetchDelivered(t, admin, sentSubject)
		if err := pass.check(delivered); err != nil {
			t.Errorf("%v\nheaders as delivered:\n%s", err, headerBlock(delivered))
		}
	})

	t.Run("SMTP_Submission", func(t *testing.T) {
		subject := fmt.Sprintf("receiver-verdict-submission-%d", time.Now().UnixNano())
		sc := dialSMTPTLS(t, restmailSubmitAddr)
		defer sc.close()
		sc.ehlo(t, "test.local")
		sc.authPlain(t, verdictSender, accountPassword)
		sc.sendExpect(t, "MAIL FROM:<"+verdictSender+">", "250")
		sc.sendExpect(t, "RCPT TO:<"+verdictRecipient+">", "250")
		sc.sendExpect(t, "DATA", "354")
		sc.send(t, fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <verdict-%d@%s>\r\n\r\nWhat does the receiver make of this?",
			verdictSender, verdictRecipient, subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano(), verdictDomain))
		sc.sendExpect(t, ".", "250")
		sc.sendExpect(t, "QUIT", "221")

		raw := fetchDelivered(t, admin, subject)
		if err := pass.check(raw); err != nil {
			t.Errorf("%v\nheaders as delivered:\n%s", err, headerBlock(raw))
		}
	})

	// The negative control, on every run: the same receiver, sent a real
	// signature that no longer matches its message from a host SPF does not
	// allow, has to say so. A receiver that passes anything, or says nothing,
	// fails here even if the two sends above pass.
	t.Run("BrokenSignature_ReceiverRefuses", func(t *testing.T) {
		if delivered == nil {
			t.Fatal("API_Send delivered nothing to break")
		}
		subject := fmt.Sprintf("receiver-verdict-broken-%d", time.Now().UnixNano())
		broken, err := breakSignature(delivered, sentSubject, subject)
		requireNoError(t, err)
		sendRawMailViaSMTP(t, mail2SMTPAddr, verdictSender, verdictRecipient, string(broken))

		raw := fetchDelivered(t, admin, subject)
		refused := pass
		refused.dkim, refused.spf, refused.dmarc = "fail", "fail", "fail"
		if err := refused.check(raw); err != nil {
			t.Errorf("%v\nheaders as delivered:\n%s", err, headerBlock(raw))
		}
		if err := pass.check(raw); err == nil {
			t.Errorf("the check passed a broken signature:\n%s", headerBlock(raw))
		}
	})
}

// dkimSelector is the selector a domain signs with, as the admin API reports it.
func dkimSelector(t *testing.T, admin *apiClient, domain string) string {
	t.Helper()
	resp, err := admin.get("/api/v1/admin/dkim")
	requireNoError(t, err)
	var keys struct {
		Data []struct {
			Domain   string `json:"domain"`
			Selector string `json:"selector"`
			HasKey   bool   `json:"has_key"`
		} `json:"data"`
	}
	requireNoError(t, decodeJSON(resp, &keys))
	for _, k := range keys.Data {
		if k.Domain == domain && k.HasKey && k.Selector != "" {
			return k.Selector
		}
	}
	t.Fatalf("%s has no DKIM key: %+v", domain, keys.Data)
	return ""
}

// fetchDelivered waits for a message on the receiver, and reads the exact bytes
// the receiver holds, which the line-trimming fetchBody does not give.
//
// A send that hits a passing DNS timeout is deferred for minutes, longer than a
// run can wait, and how the queue retries is stage 11's to check. So the wait
// sends deferred mail for this recipient again, instead of failing on it.
func fetchDelivered(t *testing.T, admin *apiClient, subject string) []byte {
	t.Helper()
	deadline := time.Now().Add(verdictArrival)
	for {
		if raw := fetchBySubject(t, subject); raw != nil {
			return raw
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %q did not reach %s within %s", subject, verdictRecipient, verdictArrival)
		}
		retryDeferred(t, admin)
		time.Sleep(2 * time.Second)
	}
}

// retryDeferred asks the queue to send deferred mail for the recipient now.
func retryDeferred(t *testing.T, admin *apiClient) {
	t.Helper()
	resp, err := admin.get("/api/v1/admin/queue?status=deferred&limit=50&recipient=" + verdictRecipient)
	requireNoError(t, err)
	var queue struct {
		Data struct {
			Items []struct {
				ID        uint   `json:"id"`
				LastError string `json:"last_error"`
			} `json:"items"`
		} `json:"data"`
	}
	requireNoError(t, decodeJSON(resp, &queue))
	for _, item := range queue.Data.Items {
		t.Logf("sending deferred queue item %d again: %s", item.ID, item.LastError)
		resp, err := admin.post(fmt.Sprintf("/api/v1/admin/queue/%d/retry", item.ID), nil)
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	}
}

// fetchBySubject reads a message from the receiver's INBOX, or nil if it is not
// there yet.
func fetchBySubject(t *testing.T, subject string) []byte {
	t.Helper()
	ic := dialIMAP(t, mail2IMAPAddr)
	defer ic.close()
	ic.login(t, verdictRecipient, adminPassword)
	if result, _ := ic.command(t, "SELECT INBOX"); !strings.Contains(result, "OK") {
		t.Fatalf("SELECT INBOX: %s", result)
	}
	_, lines := ic.command(t, fmt.Sprintf(`SEARCH SUBJECT "%s"`, subject))
	seq := ""
	for _, line := range lines {
		if fields := strings.Fields(line); len(fields) >= 3 && fields[1] == "SEARCH" {
			seq = fields[len(fields)-1]
		}
	}
	if seq == "" {
		return nil
	}

	tag := ic.nextTag()
	_ = ic.conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(ic.conn, "%s FETCH %s (BODY.PEEK[])\r\n", tag, seq); err != nil {
		t.Fatalf("send FETCH: %v", err)
	}
	for {
		line := ic.readLine(t)
		if strings.HasPrefix(line, tag+" ") {
			t.Fatalf("FETCH %s returned no message: %s", seq, line)
		}
		open := strings.LastIndexByte(line, '{')
		if !strings.Contains(line, "FETCH") || open < 0 || !strings.HasSuffix(line, "}") {
			continue // an untagged line about other mail arriving
		}
		var size int
		if _, err := fmt.Sscanf(line[open:], "{%d}", &size); err != nil {
			t.Fatalf("FETCH literal in %q: %v", line, err)
		}
		raw := make([]byte, size)
		if _, err := io.ReadFull(ic.reader, raw); err != nil {
			t.Fatalf("read FETCH literal: %v", err)
		}
		ic.readUntilTag(t, tag)
		return raw
	}
}

// breakSignature turns a delivered message back into one a sender could send,
// with a new subject under the signature that covered the old one. It keeps the
// verdict the receiver gave the original, a stale pass that must not count again.
func breakSignature(delivered []byte, subject, newSubject string) ([]byte, error) {
	start := bytes.Index(delivered, []byte("Authentication-Results:"))
	if start < 0 {
		return nil, errors.New("the delivered message has no Authentication-Results to start from")
	}
	if !bytes.Contains(delivered[start:], []byte("DKIM-Signature:")) {
		return nil, errors.New("the delivered message has no DKIM-Signature to break")
	}
	old := []byte("Subject: " + subject + "\r\n")
	if !bytes.Contains(delivered[start:], old) {
		return nil, fmt.Errorf("the delivered message has no field %q", old)
	}
	return bytes.Replace(delivered[start:], old, []byte("Subject: "+newSubject+"\r\n"), 1), nil
}

// headerBlock is a message's header, for a failure message.
func headerBlock(raw []byte) string {
	if end := bytes.Index(raw, []byte("\r\n\r\n")); end >= 0 {
		return string(raw[:end])
	}
	return string(raw)
}

// receiverVerdict is what a receiver has to have said about a message.
type receiverVerdict struct {
	receiver         string // the authserv-id of the receiver's own field
	domain, selector string
	mailFrom         string // the envelope sender SPF judged
	dkim, spf, dmarc string // the result each method must report
}

// check holds a delivered message to the verdict. Only the receiver's newest
// Authentication-Results counts: the topmost field with its authserv-id, read as
// RFC 8601 structure, so neither another server's field, nor an older one, nor a
// comment or the body saying dkim=pass, can satisfy it.
func (v receiverVerdict) check(raw []byte) error {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parse the delivered message: %w", err)
	}
	for _, field := range msg.Header["Authentication-Results"] {
		id, results := parseAuthResults(field)
		if !strings.EqualFold(id, v.receiver) {
			continue
		}
		return errors.Join(
			requireResult(results, "dkim", v.dkim, map[string]string{"header.d": v.domain, "header.s": v.selector}),
			requireResult(results, "spf", v.spf, map[string]string{"smtp.mailfrom": v.mailFrom}),
			requireResult(results, "dmarc", v.dmarc, map[string]string{"header.from": v.domain}),
		)
	}
	return fmt.Errorf("no verdict: no Authentication-Results from %s among %d such fields", v.receiver, len(msg.Header["Authentication-Results"]))
}

// requireResult finds a method's results for these properties. There must be
// one, and every one of them must be the wanted result.
func requireResult(results []authResult, method, want string, props map[string]string) error {
	found := 0
	for _, r := range results {
		if r.method != method || !r.has(props) {
			continue
		}
		found++
		if r.result != want {
			return fmt.Errorf("%s=%s for %v, want %s", method, r.result, props, want)
		}
	}
	if found == 0 {
		return fmt.Errorf("no %s result for %v, want %s; the receiver said %+v", method, props, want, results)
	}
	return nil
}

// authResult is one method's result in an Authentication-Results field.
type authResult struct {
	method, result string
	props          map[string]string
}

func (r authResult) has(props map[string]string) bool {
	for k, v := range props {
		if !strings.EqualFold(r.props[k], v) {
			return false
		}
	}
	return true
}

// parseAuthResults reads an Authentication-Results field's value (RFC 8601): its
// authserv-id, then a result for each method, with comments gone.
func parseAuthResults(value string) (string, []authResult) {
	parts := splitOutsideQuotes(dropComments(value), ';')
	id := ""
	if fields := strings.Fields(parts[0]); len(fields) > 0 {
		id = fields[0]
	}
	var results []authResult
	for _, part := range parts[1:] {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		method, result, ok := strings.Cut(fields[0], "=")
		if !ok {
			continue
		}
		method, _, _ = strings.Cut(method, "/") // a method's version
		r := authResult{strings.ToLower(method), strings.ToLower(result), map[string]string{}}
		for _, field := range fields[1:] {
			if k, v, ok := strings.Cut(field, "="); ok {
				r.props[strings.ToLower(k)] = strings.Trim(v, `"`)
			}
		}
		results = append(results, r)
	}
	return id, results
}

// dropComments removes RFC 5322 comments, which nest, and leaves quoted strings
// alone.
func dropComments(value string) string {
	var out strings.Builder
	depth, quoted := 0, false
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case (quoted || depth > 0) && c == '\\':
			if depth == 0 {
				out.WriteByte(c)
				if i+1 < len(value) {
					out.WriteByte(value[i+1])
				}
			}
			i++
		case depth == 0 && c == '"':
			quoted = !quoted
			out.WriteByte(c)
		case !quoted && c == '(':
			depth++
		case !quoted && depth > 0 && c == ')':
			depth--
		case depth == 0:
			out.WriteByte(c)
		}
	}
	return out.String()
}

func splitOutsideQuotes(value string, sep byte) []string {
	var parts []string
	start, quoted := 0, false
	for i := 0; i < len(value); i++ {
		switch {
		case value[i] == '\\':
			i++
		case value[i] == '"':
			quoted = !quoted
		case value[i] == sep && !quoted:
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	return append(parts, value[start:])
}

// TestReceiverVerdictCheck is the negative control on the check itself, with no
// stack: it refuses a verdict that is absent, belongs to another server, or says
// anything but pass for the domain's selector.
func TestReceiverVerdictCheck(t *testing.T) {
	const good = "mail2.test; dkim=pass (2048-bit key) header.d=restmail.test header.s=default header.b=\"abc\";\r\n" +
		"\tspf=pass (mail2.test: domain of verdict-out@restmail.test designates 10.99.0.13 as permitted sender) smtp.mailfrom=verdict-out@restmail.test;\r\n" +
		"\tdmarc=pass (policy=reject) header.from=restmail.test"
	message := func(values ...string) []byte {
		var b strings.Builder
		b.WriteString("Received: from restmail.test by mail2.test\r\n")
		for _, v := range values {
			b.WriteString("Authentication-Results: " + v + "\r\n")
		}
		b.WriteString("From: verdict-out@restmail.test\r\nSubject: s\r\n\r\nbody\r\n")
		return []byte(b.String())
	}
	pass := receiverVerdict{receiver: "mail2.test", domain: "restmail.test", selector: "default",
		mailFrom: "verdict-out@restmail.test", dkim: "pass", spf: "pass", dmarc: "pass"}

	if err := pass.check(message(good)); err != nil {
		t.Errorf("a good verdict was refused: %v", err)
	}
	if err := pass.check(message(good, "mail2.test; dkim=fail header.d=restmail.test header.s=default")); err != nil {
		t.Errorf("an older verdict below a good one counted: %v", err)
	}
	refused := pass
	refused.dkim, refused.spf, refused.dmarc = "fail", "fail", "fail"
	if err := refused.check(message(strings.ReplaceAll(good, "=pass", "=fail"))); err != nil {
		t.Errorf("a refusal was not recognised as one: %v", err)
	}

	for name, raw := range map[string][]byte{
		"verdict absent":           message(),
		"another server's verdict": message(strings.Replace(good, "mail2.test;", "restmail.test;", 1)),
		"broken signature":         message(strings.Replace(good, "dkim=pass", "dkim=fail", 1)),
		"another selector":         message(strings.Replace(good, "header.s=default", "header.s=other", 1)),
		"another domain's key":     message(strings.Replace(good, "header.d=restmail.test", "header.d=mail1.test", 1)),
		"no DKIM result":           message("mail2.test; spf=pass smtp.mailfrom=verdict-out@restmail.test; dmarc=pass header.from=restmail.test"),
		"SPF softfail":             message(strings.Replace(good, "spf=pass", "spf=softfail", 1)),
		"no SPF result":            message("mail2.test; dkim=pass header.d=restmail.test header.s=default; dmarc=pass header.from=restmail.test"),
		"DMARC fail":               message(strings.Replace(good, "dmarc=pass", "dmarc=fail", 1)),
		"no DMARC result":          message("mail2.test; dkim=pass header.d=restmail.test header.s=default; spf=pass smtp.mailfrom=verdict-out@restmail.test"),
		"pass only in a comment":   message("mail2.test; dkim=fail (dkim=pass; spf=pass; dmarc=pass) header.d=restmail.test header.s=default"),
		"a stale pass below":       message(strings.Replace(good, "dkim=pass", "dkim=fail", 1), good),
		"pass only in the body":    []byte("From: a@restmail.test\r\n\r\nAuthentication-Results: " + good + "\r\n"),
	} {
		if err := pass.check(raw); err == nil {
			t.Errorf("%s: the check passed it", name)
		}
	}
}
