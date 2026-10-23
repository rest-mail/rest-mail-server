package smtp

import (
	"fmt"
	"strings"
	"testing"
)

const internationalBody = "From: 用户 <用户@例え.jp>\r\n" +
	"To: 受信者 <受信者@例え.jp>\r\n" +
	"Cc: Zoë <zoë@example.test>\r\n" +
	"Subject: Grüße aus München\r\n" +
	"Message-ID: <eai@example.test>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nこんにちは\r\n"

func TestSMTP_SMTPUTF8Advertisement(t *testing.T) {
	for _, submission := range []bool{false, true} {
		t.Run(fmt.Sprintf("submission=%t", submission), func(t *testing.T) {
			h := newCleartextSMTPHarness(t, newMockBackend(), newMockStore(), submission)
			for _, encrypted := range []bool{false, true} {
				if encrypted {
					h.upgradeTLS(h.conn)
				}
				lines, final := h.readReplyAfter("EHLO client.test")
				if replyCode(final) != "250" {
					t.Fatalf("EHLO = %q", final)
				}
				caps := strings.Join(lines, "\n")
				for _, want := range []string{"250-SMTPUTF8", "250-8BITMIME"} {
					if !strings.Contains(caps, want) {
						t.Errorf("TLS=%t: missing %q in %s", encrypted, want, caps)
					}
				}
			}
		})
	}
}

func TestSMTP_SMTPUTF8EnvelopeRequiresParameter(t *testing.T) {
	back, store := newMockBackend(), newMockStore()
	h := newSMTPHarness(t, back, store, false)
	h.ehlo()
	for _, sender := range []string{"用户@remote.test", "sender@例え.jp"} {
		if r := h.cmd("MAIL FROM:<%s>", sender); !strings.HasPrefix(r, "550 5.6.7 ") {
			t.Fatalf("MAIL %q without SMTPUTF8 = %q", sender, r)
		}
	}
	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	for _, recipient := range []string{"受信者@local.test", "user@例え.jp"} {
		if r := h.cmd("RCPT TO:<%s>", recipient); !strings.HasPrefix(r, "553 5.6.7 ") {
			t.Fatalf("RCPT %q without SMTPUTF8 = %q", recipient, r)
		}
		back.mu.Lock()
		calls := back.checkCount[recipient]
		back.mu.Unlock()
		if calls != 0 {
			t.Fatalf("unnegotiated recipient reached mailbox lookup %d times", calls)
		}
	}
	if len(back.deliveredTo()) != 0 || len(store.queued()) != 0 {
		t.Fatal("unnegotiated envelope committed mail")
	}
}

func TestSMTP_SMTPUTF8InboundDelivery(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("TLS=%t", encrypted), func(t *testing.T) {
			back, store := newMockBackend(), newMockStore()
			const recipient = "受信者@例え.jp"
			back.local[recipient] = true
			h := newSMTPHarnessTLS(t, back, store, false, encrypted)
			h.ehlo()
			if r := h.cmd("MAIL FROM:<用户@例え.jp> BODY=8BITMIME smtputf8"); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if r := h.cmd("RCPT TO:<%s>", recipient); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if r := h.dataBody(internationalBody); replyCode(r) != "250" {
				t.Fatalf("internationalized DATA = %q", r)
			}
			req := back.lastDeliverReq()
			if req == nil {
				t.Fatal("no local delivery")
			}
			if req.Address != recipient || req.Sender != "用户@例え.jp" || req.Subject != "Grüße aus München" || req.SenderName != "用户" || !strings.Contains(req.BodyText, "こんにちは") {
				t.Errorf("EAI fields not preserved: %+v", req)
			}
			if string(req.RecipientsTo) != `["受信者@例え.jp"]` || string(req.RecipientsCc) != `["zoë@example.test"]` {
				t.Errorf("EAI header addresses = %s / %s", req.RecipientsTo, req.RecipientsCc)
			}
			if !strings.Contains(string(req.RawMessage), internationalBody) {
				t.Errorf("original message bytes changed: %q", req.RawMessage)
			}
			with := "UTF8SMTP"
			if encrypted {
				with += "S"
			}
			assertReceivedTrace(t, string(req.RawMessage), with, recipient)
		})
	}
}

func TestSMTP_SMTPUTF8SubmissionAuthorizationAndRouting(t *testing.T) {
	back, store := newMockBackend(), newMockStore()
	back.user = "用户@例え.jp"
	back.local["受信者@例え.jp"] = true
	h := newSMTPHarness(t, back, store, true)
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatal(r)
	}
	// SMTPUTF8 grants no additional sending identities.
	if r := h.cmd("MAIL FROM:<spoof@例え.jp> SMTPUTF8"); !strings.HasPrefix(r, "553 5.7.1 ") {
		t.Fatalf("unauthorized EAI sender = %q", r)
	}
	for _, spoof := range []bool{true, false} {
		if r := h.cmd("MAIL FROM:<%s> BODY=8BITMIME SMTPUTF8", back.user); replyCode(r) != "250" {
			t.Fatal(r)
		}
		for _, recipient := range []string{"受信者@例え.jp", "münchen@städte.de"} {
			if r := h.cmd("RCPT TO:<%s>", recipient); replyCode(r) != "250" {
				t.Fatal(r)
			}
		}
		body, code := internationalBody, "250"
		if spoof {
			body, code = strings.ReplaceAll(body, back.user, "spoof@例え.jp"), "550"
		}
		if r := h.dataBody(body); replyCode(r) != code {
			t.Fatalf("spoof=%t: DATA = %q, want %s", spoof, r, code)
		}
		want := 1
		if spoof {
			want = 0
		}
		if len(back.deliveredTo()) != want || len(store.queued()) != want || len(store.persistedSubmissions()) != want {
			t.Fatalf("spoof=%t: delivery/queue/persistence = %d/%d/%d, want %d each", spoof, len(back.deliveredTo()), len(store.queued()), len(store.persistedSubmissions()), want)
		}
	}
	q := store.queued()[0]
	if q.Sender != back.user || q.Recipient != "münchen@städte.de" || q.Domain != "städte.de" || q.BodyType != "8BITMIME" || !strings.Contains(q.RawMessage, internationalBody) {
		t.Errorf("internationalized submission changed: %+v", q)
	}
	assertReceivedTrace(t, q.RawMessage, "UTF8SMTPSA", "")
	if strings.Contains(q.RawMessage[:endOfFirstHeader(q.RawMessage)], "for <") {
		t.Fatal("multi-recipient EAI trace disclosed a recipient")
	}
}

// go-smtp (v0.31.0 on) answers an envelope address that is not valid UTF-8
// before the session sees it: 550 5.6.7 (MAIL) and 553 5.6.7 (RCPT) without
// SMTPUTF8 (RFC 6531 §3.4: a non-ASCII address needs the parameter), and
// 501 5.5.2 with it (not a well-formed address).
func TestSMTP_SMTPUTF8InvalidEnvelopeEncoding(t *testing.T) {
	for _, c := range []struct{ parameter, mailReply, rcptReply string }{
		{"", "550 5.6.7 ", "553 5.6.7 "},
		{" SMTPUTF8", "501 5.5.2 ", "501 5.5.2 "},
	} {
		parameter := c.parameter
		t.Run(parameter, func(t *testing.T) {
			back, store := newMockBackend(), newMockStore()
			h := newSMTPHarness(t, back, store, false)
			h.ehlo()
			for _, address := range []string{"bad\xff@remote.test", "sender@bad\xff.test"} {
				if r := h.cmd("MAIL FROM:<%s>%s", address, parameter); !strings.HasPrefix(r, c.mailReply) {
					t.Fatalf("invalid MAIL encoding %q = %q, want %s", address, r, c.mailReply)
				}
			}
			if r := h.cmd("MAIL FROM:<sender@remote.test>%s", parameter); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if r := h.cmd("RCPT TO:<bad\xff@local.test>"); !strings.HasPrefix(r, c.rcptReply) {
				t.Fatalf("invalid RCPT encoding = %q, want %s", r, c.rcptReply)
			}
		})
	}
}

func TestSMTP_SMTPUTF8UnknownRecipientReplyIsASCII(t *testing.T) {
	h := newSMTPHarness(t, newMockBackend(), newMockStore(), false)
	h.ehlo()
	if r := h.cmd("MAIL FROM:<> SMTPUTF8"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	r := h.cmd("RCPT TO:<不存在@例え.jp>")
	if !strings.HasPrefix(r, "550 5.1.1 ") || strings.ContainsFunc(r, func(r rune) bool { return r > 127 }) {
		t.Fatalf("unknown recipient reply must be ASCII: %q", r)
	}
}

// All rejected messages have both a local and a remote recipient. Neither
// delivery, queue insertion nor submission persistence may occur on failure.
func TestSMTP_SMTPUTF8HeaderPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, parameter, body, reply string
	}{
		{"raw headers require parameter", "", "Subject: Grüß\r\n", "554 5.6.9 "},
		{"folded raw headers require parameter", "", "Subject: Hi\r\n\tGrüß\r\n", "554 5.6.9 "},
		{"invalid headers with parameter", " SMTPUTF8", "Subject: bad\xff\r\n", "550 5.6.0 "},
		{"invalid headers without parameter", "", "Subject: bad\xff\r\n", "550 5.6.0 "},
		{"raw headers accepted", " SMTPUTF8", "Subject: Grüß\r\n", "250 "},
		{"encoded words remain legacy compatible", "", "Subject: =?UTF-8?B?R3LDvMOf?=\r\n", "250 "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			back, store := newMockBackend(), newMockStore()
			back.local["bob@local.test"] = true
			h := newSMTPHarness(t, back, store, true)
			h.ehlo()
			if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
				t.Fatal(r)
			}
			if r := h.cmd("MAIL FROM:<alice@example.com> BODY=8BITMIME%s", tc.parameter); replyCode(r) != "250" {
				t.Fatal(r)
			}
			for _, recipient := range []string{"bob@local.test", "carol@remote.test"} {
				if r := h.cmd("RCPT TO:<%s>", recipient); replyCode(r) != "250" {
					t.Fatal(r)
				}
			}
			body := "From: Alice <alice@example.com>\r\n" + tc.body + "\r\nbody\r\n"
			if r := h.dataBody(body); !strings.HasPrefix(r, tc.reply) {
				t.Fatalf("DATA = %q, want prefix %q", r, tc.reply)
			}
			want := 0
			if strings.HasPrefix(tc.reply, "250") {
				want = 1
			}
			if len(back.deliveredTo()) != want || len(store.queued()) != want || len(store.persistedSubmissions()) != want {
				t.Fatalf("delivery/queue/persistence = %d/%d/%d, want %d each", len(back.deliveredTo()), len(store.queued()), len(store.persistedSubmissions()), want)
			}
			if want == 1 {
				if q := store.queued()[0]; !strings.Contains(q.RawMessage, body) || q.BodyType != "8BITMIME" {
					t.Errorf("outbound raw message or BODY parameter changed: %+v", q)
				}
			}
		})
	}
}

func TestSMTP_SMTPUTF8TransactionReset(t *testing.T) {
	for _, reset := range []string{"RSET", "EHLO again.test", "DATA", "rejected DATA"} {
		t.Run(reset, func(t *testing.T) {
			back, store := newMockBackend(), newMockStore()
			back.local["alice@local.test"] = true
			h := newSMTPHarness(t, back, store, false)
			h.ehlo()
			if r := h.cmd("MAIL FROM:<> BODY=8BITMIME SMTPUTF8"); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if strings.Contains(reset, "DATA") {
				if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
					t.Fatal(r)
				}
				body, code := internationalBody, "250"
				if reset == "rejected DATA" {
					body, code = "Subject: bad\xff\r\n\r\nbody\r\n", "550"
				}
				if r := h.dataBody(body); replyCode(r) != code {
					t.Fatal(r)
				}
			} else if r := h.cmd("%s", reset); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if r := h.cmd("RCPT TO:<受信者@local.test>"); !strings.HasPrefix(r, "553 5.6.7 ") {
				t.Fatalf("SMTPUTF8 leaked to next transaction: %q", r)
			}
			if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if r := h.dataBody(internationalBody); !strings.HasPrefix(r, "554 5.6.9 ") {
				t.Fatalf("SMTPUTF8 header permission leaked: %q", r)
			}
		})
	}
}

func TestSMTP_SMTPUTF8BodyOnlyDoesNotRequireParameter(t *testing.T) {
	back, store := newMockBackend(), newMockStore()
	back.local["alice@local.test"] = true
	h := newSMTPHarness(t, back, store, false)
	h.ehlo()
	if r := h.cmd("MAIL FROM:<sender@remote.test> BODY=8BITMIME"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	// Includes bytes that aren't UTF-8: 8BITMIME does not prescribe a charset.
	body := "Subject: body only\r\n\r\nGrüße\xff\r\n"
	if r := h.dataBody(body); replyCode(r) != "250" {
		t.Fatalf("8BITMIME body-only DATA = %q", r)
	}
	if req := back.lastDeliverReq(); req == nil || !strings.Contains(string(req.RawMessage), body) {
		t.Fatal("8-bit body was changed or not delivered")
	}
}

func TestSMTP_SMTPUTF8BDAT(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("SMTPUTF8=%t", enabled), func(t *testing.T) {
			back, store := newMockBackend(), newMockStore()
			back.local["alice@local.test"] = true
			h := newSMTPHarness(t, back, store, false)
			h.ehlo()
			parameter, code := "", "554"
			if enabled {
				parameter, code = " SMTPUTF8", "250"
			}
			if r := h.cmd("MAIL FROM:<> BODY=8BITMIME%s", parameter); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
				t.Fatal(r)
			}
			h.send("BDAT %d LAST", len(internationalBody))
			if _, err := h.cw.WriteString(internationalBody); err != nil {
				t.Fatal(err)
			}
			if err := h.cw.Flush(); err != nil {
				t.Fatal(err)
			}
			if _, r := h.readReply(); replyCode(r) != code {
				t.Fatalf("BDAT = %q, want %s", r, code)
			}
			want := 0
			if enabled {
				want = 1
			}
			if len(back.deliveredTo()) != want {
				t.Fatalf("BDAT delivered %d, want %d", len(back.deliveredTo()), want)
			}
		})
	}
}
