package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

// deliveryWireAnswers holds what the routes answered and stored for deliveryWireCases, recorded
// from them as they stood when they bound a whole request in memory. Setting
// RESTMAIL_RECORD_DELIVERY_ANSWERS writes the file again.
const deliveryWireAnswers = "testdata/delivery_wire_answers.json"

type deliveryWireCase struct {
	name, route, body, contentType string
	action                         pipeline.Action
	auth                           *RestmailDeliverAuthConfig
}

// deliveryWireAnswer is the answer to a request, and everything it stored.
type deliveryWireAnswer struct {
	Status int    `json:"status"`
	Answer string `json:"answer"`
	Stored string `json:"stored"`
}

func deliveryWireCases() []deliveryWireCase {
	const (
		plain = "From: Some One <someone@remote.test>\r\nTo: owner@spool.test\r\nCc: cc@remote.test\r\nSubject: =?UTF-8?Q?caf=C3=A9?=\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nMessage-ID: <plain@remote.test>\r\nIn-Reply-To: <parent@remote.test>\r\nReferences: <root@remote.test> <parent@remote.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody caf\xc3\xa9\r\n"
		multi = "From: someone@remote.test\r\nTo: owner@spool.test\r\nSubject: parts\r\nMessage-ID: <multi@remote.test>\r\nContent-Type: multipart/mixed; boundary=OUT\r\n\r\n" +
			"--OUT\r\nContent-Type: multipart/alternative; boundary=IN\r\n\r\n--IN\r\nContent-Type: text/plain\r\n\r\nplain part\r\n--IN\r\nContent-Type: text/html\r\n\r\n<p>html part</p>\r\n--IN--\r\n" +
			"--OUT\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename=pic.png\r\nContent-ID: <pic>\r\nContent-Transfer-Encoding: base64\r\n\r\ncGljdHVyZQ==\r\n" +
			"--OUT\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\n%PDF bytes\r\n--OUT--\r\n"
		latin    = "From: someone@remote.test\r\nTo: owner@spool.test\r\nSubject: caf\xe9\r\nMessage-ID: <latin@remote.test>\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\nbod\xe9\r\n"
		ours     = "Authentication-Results: restmail; spf=pass\r\nAuthentication-Results: other.example; spf=fail\r\nFrom: someone@remote.test\r\nTo: owner@spool.test\r\nMessage-ID: <ours@remote.test>\r\nSubject: claims\r\n\r\nAuthentication-Results: restmail; in the body\r\n"
		only     = "Authentication-Results: restmail; spf=pass"
		noID     = "From: someone@remote.test\r\nTo: owner@spool.test\r\nSubject: none\r\n\r\nbody\r\n"
		noTo     = "From: someone@remote.test\r\nSubject: nobody\r\nMessage-ID: <noto@remote.test>\r\n\r\nbody\r\n"
		noFrom   = "To: owner@spool.test\r\nSubject: anon\r\nMessage-ID: <nofrom@remote.test>\r\n\r\nbody\r\n"
		broken   = "this is not a header\r\nnor this\r\n\r\nbody\r\n"
		headless = "\r\nbody with no headers\r\n"
		noEnd    = "From: someone@remote.test\r\nTo: owner@spool.test\r\nMessage-ID: <noend@remote.test>\r\nSubject: no body"
		lfOnly   = "From: someone@remote.test\nTo: owner@spool.test\nMessage-ID: <lf@remote.test>\nSubject: lf\n\nbody\n"
		spoofed  = "From: x@spool.test\r\nTo: owner@spool.test\r\nMessage-ID: <spoof@remote.test>\r\nSubject: s\r\n\r\nbody\r\n"
		signed   = "DKIM-Signature: v=1; a=rsa-sha256\r\nFrom: x@spool.test\r\nTo: owner@spool.test\r\nMessage-ID: <signed@remote.test>\r\nSubject: s\r\n\r\nbody\r\n"
	)
	calendar := spoolCalendarMessage()
	cases := []deliveryWireCase{}
	gateway := func(name, body string) {
		cases = append(cases, deliveryWireCase{name: "gateway/" + name, route: "gateway", body: body})
	}
	raw := func(message string) string { return `"raw_message":"` + spoolBase64(message) + `"` }
	const address = `"address":"owner@spool.test","sender":"someone@remote.test","subject":"s"`
	gateway("plain", `{`+address+`,`+raw(plain)+`}`)
	gateway("parts", `{`+address+`,`+raw(multi)+`}`)
	gateway("calendar", `{`+address+`,`+raw(calendar)+`}`)
	gateway("not_utf8", `{`+address+`,`+raw(latin)+`}`)
	gateway("claims_our_verdicts", `{`+address+`,`+raw(ours)+`}`)
	gateway("only_our_verdicts", `{`+address+`,`+raw(only)+`}`)
	gateway("no_message_id", `{`+address+`,`+raw(noID)+`}`)
	gateway("unparseable", `{`+address+`,"body_text":"t",`+raw(broken)+`}`)
	gateway("no_headers", `{`+address+`,`+raw(headless)+`}`)
	gateway("no_message_at_all", `{`+address+`,"body_text":"just text","body_html":"<p>just</p>","message_id":"<t@remote.test>"}`)
	gateway("text_and_message", `{`+address+`,"body_text":"text","body_html":"<p>html</p>",`+raw(plain)+`}`)
	gateway("text_with_escapes", `{`+address+`,"body_text":"café 😀 \ud83d \\ \" \/ \b\f\n\r\t","message_id":"<e@remote.test>"}`)
	gateway("text_not_utf8", "{"+address+",\"body_text\":\"a\xffb\xc3\",\"message_id\":\"<u@remote.test>\"}")
	gateway("all_fields", `{"mailbox_id":MAILBOX,"sender":"someone@remote.test","sender_name":"Some One","recipients_to":["owner@spool.test"],"recipients_cc":["cc@remote.test"],"subject":"s","body_text":"t","body_html":"h","message_id":"<all@remote.test>","in_reply_to":"<p@remote.test>","references":"<r@remote.test> <p@remote.test>",`+raw(plain)+`,"client_ip":"192.0.2.1","helo_name":"mx.remote.test","folder":"Archive","is_answered":true,"is_read":true,"is_flagged":true,"is_starred":true,"is_draft":true,"received_at":"2020-01-02T03:04:05Z","received_tls":true,"tls_version":"TLS1.3","require_tls":true}`)
	gateway("folder_inbox_any_case", `{`+address+`,"folder":"inbox",`+raw(plain)+`}`)
	gateway("field_order", `{`+raw(plain)+`,"body_text":"t",`+address+`}`)
	gateway("raw_empty", `{`+address+`,"raw_message":"","body_text":"t","message_id":"<z@remote.test>"}`)
	gateway("raw_null", `{`+address+`,"raw_message":null,"body_text":"t","message_id":"<n@remote.test>"}`)
	gateway("raw_only_newlines", `{`+address+`,"raw_message":"\n","message_id":"<l@remote.test>"}`)
	gateway("raw_with_newlines", `{`+address+`,"raw_message":"U3ViamVjdDogeA0K\r\nDQpib2R5DQo="}`)
	gateway("raw_escaped", `{`+address+`,"raw_message":"U3VieW\/jdDogeA0KDQpib2R5DQo="}`)
	gateway("raw_numbers", `{`+address+`,"raw_message":[83,117,98,106,101,99,116,58,32,120,13,10,13,10,98,13,10]}`)
	gateway("raw_numbers_nul", `{`+address+`,"raw_message":[83,0]}`)
	gateway("raw_twice", `{`+address+`,"raw_message":"QQ==","raw_message":"U3ViamVjdDogeA0KDQpiDQo="}`)
	gateway("raw_twice_first_bad", `{`+address+`,"raw_message":"!!","raw_message":"QQ=="}`)
	gateway("raw_twice_then_null", `{`+address+`,"raw_message":"U3ViamVjdDogeA0KDQpiDQo=","raw_message":null}`)
	gateway("raw_not_base64", `{`+address+`,"raw_message":"!!!"}`)
	gateway("raw_number", `{`+address+`,"raw_message":1}`)
	gateway("raw_object", `{`+address+`,"raw_message":{}}`)
	gateway("raw_nul", `{`+address+`,`+raw("Subject: x\r\n\r\nb\x00dy")+`}`)
	gateway("raw_and_recipients_nul", `{`+address+`,"recipients_to":["\u0000"],`+raw("Subject: x\r\n\r\nb\x00dy")+`}`)
	gateway("text_nul", `{`+address+`,"body_text":"a\u0000b"}`)
	gateway("html_nul", `{`+address+`,"body_html":"a\u0000b"}`)
	gateway("text_twice", `{`+address+`,"body_text":"one","body_text":"two","message_id":"<tw@remote.test>"}`)
	gateway("text_twice_nul_first", `{`+address+`,"body_text":"\u0000","body_text":"two"}`)
	gateway("text_twice_then_null", `{`+address+`,"body_text":"one","body_text":null,"message_id":"<tn@remote.test>"}`)
	gateway("text_null", `{`+address+`,"body_text":null,"body_html":null,"message_id":"<nu@remote.test>"}`)
	gateway("text_number", `{`+address+`,"body_text":1}`)
	gateway("text_array", `{`+address+`,"body_html":[]}`)
	gateway("text_bad_escape", `{`+address+`,"body_text":"\q"}`)
	gateway("text_cut_short", `{`+address+`,"body_text":"abc`)
	gateway("subject_nul", `{"address":"owner@spool.test","subject":"\u0000"}`)
	gateway("recipients_nul", `{`+address+`,"recipients_cc":{"\u0000":1}}`)
	gateway("recipients_not_addresses", `{`+address+`,"recipients_to":"x","message_id":"<ra@remote.test>"}`)
	gateway("folder_bad", `{`+address+`,"folder":" x",`+raw(plain)+`}`)
	gateway("folder_dots", `{`+address+`,"folder":"a/../b",`+raw(plain)+`}`)
	gateway("no_such_address", `{"address":"no@spool.test",`+raw(plain)+`}`)
	gateway("no_such_id", `{"mailbox_id":99999999,`+raw(plain)+`}`)
	gateway("cannot_receive", `{"address":"silent@spool.test",`+raw(plain)+`}`)
	gateway("no_mailbox", `{`+raw(plain)+`}`)
	gateway("mailbox_id_bad", `{"mailbox_id":"x",`+raw(plain)+`}`)
	gateway("undeclared", `{`+address+`,"x":"y",`+raw(plain)+`}`)
	gateway("undeclared_case", `{`+address+`,"RAW_MESSAGE":"QQ=="}`)
	gateway("escaped_field_name", `{`+address+`,"raw_message":"U3ViamVjdDogeA0KDQpiDQo="}`)
	gateway("empty", ``)
	gateway("blank", " \n")
	gateway("object_empty", `{}`)
	gateway("array", `[]`)
	gateway("null", `null`)
	gateway("number", `1`)
	gateway("not_json", `{`)
	gateway("trailing", `{`+address+`} {}`)
	gateway("trailing_comma", `{`+address+`,}`)
	gateway("arrival_mail_loop", `{`+address+`,"received_tls":false,`+raw(receivedMessage(rmime.DefaultMaxReceived+1, "owner@spool.test"))+`}`)
	gateway("arrival_mail_loop_no_mailbox", `{"received_tls":false,`+raw(receivedMessage(rmime.DefaultMaxReceived+1, "owner@spool.test"))+`}`)
	gateway("filed_past_the_hop_limit", `{`+address+`,"message_id":"<hops@remote.test>",`+raw(receivedMessage(rmime.DefaultMaxReceived+1, "owner@spool.test"))+`}`)
	gateway("arrival_too_nested", `{`+address+`,"received_tls":false,`+raw(nestedRaw(rmime.DefaultMaxNestingDepth+1, "owner@spool.test"))+`}`)
	gateway("filed_too_nested", `{`+address+`,`+raw(nestedRaw(rmime.DefaultMaxNestingDepth+1, "owner@spool.test"))+`}`)
	gateway("arrival_mail_loop_raw_twice", `{`+address+`,"received_tls":false,"raw_message":"QQ==",`+raw(receivedMessage(rmime.DefaultMaxReceived+1, "owner@spool.test"))+`}`)
	for _, action := range []pipeline.Action{pipeline.ActionReject, pipeline.ActionDefer, pipeline.ActionDiscard, pipeline.ActionQuarantine} {
		cases = append(cases, deliveryWireCase{name: "gateway/pipeline_" + string(action), route: "gateway", action: action,
			body: `{` + address + `,"body_text":"text",` + raw(plain) + `}`})
	}
	cases = append(cases,
		deliveryWireCase{name: "gateway/no_content_type", route: "gateway", contentType: "-", body: `{` + address + `,` + raw(plain) + `}`},
		deliveryWireCase{name: "gateway/content_type_parameter", route: "gateway", contentType: "application/json; charset=utf-8", body: `{` + address + `,` + raw(plain) + `}`},
		deliveryWireCase{name: "gateway/other_content_type", route: "gateway", contentType: "text/plain", body: `{` + address + `,` + raw(plain) + `}`},
		deliveryWireCase{name: "gateway/empty_other_content_type", route: "gateway", contentType: "text/plain"},
	)

	trusted, open, strict := &restmailTrusted, &RestmailDeliverAuthConfig{}, &RestmailDeliverAuthConfig{Strict: true}
	rm := func(name, body string, auth *RestmailDeliverAuthConfig) {
		cases = append(cases, deliveryWireCase{name: "restmail/" + name, route: "restmail", body: body, auth: auth})
	}
	const to = `"to":["owner@spool.test"]`
	rm("plain", `{`+to+`,`+raw(plain)+`}`, trusted)
	rm("parts", `{`+to+`,`+raw(multi)+`}`, trusted)
	rm("calendar", `{`+to+`,`+raw(calendar)+`}`, trusted)
	rm("not_utf8", `{`+to+`,`+raw(latin)+`}`, trusted)
	rm("claims_our_verdicts", `{`+to+`,`+raw(ours)+`}`, trusted)
	rm("only_our_verdicts", `{`+to+`,"from":"someone@remote.test","message_id":"<o@remote.test>",`+raw(only)+`}`, trusted)
	rm("no_message_id", `{`+to+`,`+raw(noID)+`}`, trusted)
	rm("unparseable", `{`+to+`,"from":"someone@remote.test","subject":"s","body_text":"t","message_id":"<u@remote.test>",`+raw(broken)+`}`, trusted)
	rm("no_headers", `{`+to+`,"from":"someone@remote.test","message_id":"<h@remote.test>",`+raw(headless)+`}`, trusted)
	rm("no_empty_line", `{`+raw(noEnd)+`}`, trusted)
	rm("bare_lf", `{`+raw(lfOnly)+`}`, trusted)
	rm("recipients_from_headers", `{`+raw(plain)+`}`, trusted)
	rm("no_recipients_anywhere", `{`+raw(noTo)+`}`, trusted)
	rm("no_sender_anywhere", `{`+raw(noFrom)+`}`, trusted)
	rm("no_message_at_all", `{"from":"someone@remote.test",`+to+`,"subject":"s","date":"Thu, 02 Jan 2020 03:04:05 +0000","body_text":"t","body_html":"<p>h</p>","message_id":"<m@remote.test>","in_reply_to":"<p@remote.test>","references":"<r@remote.test>","headers":{"X-A":["b"]}}`, trusted)
	rm("sender_and_recipients", `{"sender":"someone@remote.test","recipients":["owner@spool.test"],`+raw(plain)+`}`, trusted)
	rm("request_beats_headers", `{"from":"other@remote.test",`+to+`,"subject":"mine","date":"d","message_id":"<mine@remote.test>","in_reply_to":"<i@remote.test>","references":"<f@remote.test>","body_text":"mine","body_html":"<p>mine</p>","headers":{"X-A":["b"]},`+raw(plain)+`}`, trusted)
	rm("text_fills_html", `{`+to+`,"body_text":"mine",`+raw(multi)+`}`, trusted)
	rm("text_with_escapes", `{"from":"someone@remote.test",`+to+`,"body_text":"café 😀 \ud83d \udc00x \\ \" \/ \b\f\n\r\t","message_id":"<e@remote.test>"}`, trusted)
	rm("text_not_utf8", "{\"from\":\"someone@remote.test\","+to+",\"body_text\":\"a\xffb\xc3\",\"message_id\":\"<u8@remote.test>\"}", trusted)
	rm("raw_escaped", `{`+to+`,"from":"someone@remote.test","message_id":"<esc@remote.test>","raw_message":"U3VieW\/jdDogeA0KDQpib2R5DQo="}`, trusted)
	rm("raw_not_base64", `{`+to+`,"from":"someone@remote.test","raw_message":"Subject: x\r\n\r\nbody"}`, trusted)
	rm("raw_not_utf8", `{`+to+`,"from":"someone@remote.test",`+raw("Subject: \xe9\r\nMessage-ID: <n8@remote.test>\r\n\r\nb\xc3\xa9\xf0\r\n")+`}`, trusted)
	rm("raw_nul", `{`+to+`,"from":"someone@remote.test",`+raw("Message-ID: <nul@remote.test>\r\n\r\nb\x00dy")+`}`, trusted)
	rm("raw_empty", `{`+to+`,"from":"someone@remote.test","raw_message":"","message_id":"<z@remote.test>"}`, trusted)
	rm("raw_empty_and_no_recipients", `{"raw_message":""}`, trusted)
	rm("raw_null", `{`+to+`,"from":"someone@remote.test","raw_message":null,"message_id":"<n@remote.test>"}`, trusted)
	rm("raw_number", `{`+to+`,"raw_message":1}`, trusted)
	rm("raw_twice", `{`+to+`,"raw_message":"QQ==",`+raw(plain)+`}`, trusted)
	rm("raw_twice_then_null", `{`+to+`,`+raw(plain)+`,"raw_message":null}`, trusted)
	rm("raw_bad_escape", `{`+to+`,"raw_message":"\q"}`, trusted)
	rm("raw_cut_short", `{`+to+`,"raw_message":"abc`, trusted)
	rm("text_twice", `{"from":"someone@remote.test",`+to+`,"body_text":"one","body_text":"two","message_id":"<tw@remote.test>"}`, trusted)
	rm("text_twice_then_null", `{"from":"someone@remote.test",`+to+`,"body_text":"one","body_text":null,"message_id":"<tn@remote.test>"}`, trusted)
	rm("text_null", `{"from":"someone@remote.test",`+to+`,"body_text":null,"body_html":null,"message_id":"<nu@remote.test>"}`, trusted)
	rm("text_number", `{`+to+`,"body_text":1}`, trusted)
	rm("text_nul", `{"from":"someone@remote.test",`+to+`,"body_text":"a\u0000b","message_id":"<tn0@remote.test>"}`, trusted)
	rm("to_empty", `{"to":[],`+raw(plain)+`}`, trusted)
	rm("to_empty_alone", `{"to":[]}`, trusted)
	rm("to_not_array", `{"to":"x"}`, trusted)
	rm("recipient_unknown", `{"to":["no@spool.test"],`+raw(plain)+`}`, trusted)
	rm("recipient_cannot_receive", `{"to":["silent@spool.test"],`+raw(plain)+`}`, trusted)
	rm("recipient_bad", `{"to":["not an address"],`+raw(plain)+`}`, trusted)
	rm("recipients_mixed", `{"to":["no@spool.test","owner@spool.test","owner@spool.test"],`+raw(plain)+`}`, trusted)
	rm("plus_address", `{"to":["owner+tag@spool.test"],`+raw(plain)+`}`, trusted)
	rm("untrusted_remote", `{`+to+`,`+raw(plain)+`}`, open)
	rm("untrusted_spoof", `{`+to+`,`+raw(spoofed)+`}`, open)
	rm("untrusted_spoof_in_request", `{`+to+`,"from":"x@spool.test",`+raw(plain)+`}`, open)
	rm("untrusted_spoof_badly_signed", `{`+to+`,`+raw(signed)+`}`, open)
	rm("untrusted_strict_unsigned", `{`+to+`,`+raw(plain)+`}`, strict)
	rm("undeclared", `{`+to+`,"x":"y",`+raw(plain)+`}`, trusted)
	rm("empty", ``, trusted)
	rm("object_empty", `{}`, trusted)
	rm("array", `[]`, trusted)
	rm("null", `null`, trusted)
	rm("not_json", `{`, trusted)
	rm("trailing", `{`+to+`} {}`, trusted)
	// The hop and structure limits come before the recipients are required, and before the sender
	// and the recipients are looked up, so each of these is the limit's refusal.
	looping, nested := receivedMessage(rmime.DefaultMaxReceived+1, "owner@spool.test"), nestedRaw(rmime.DefaultMaxNestingDepth+1, "owner@spool.test")
	rm("mail_loop", `{`+to+`,`+raw(looping)+`}`, trusted)
	rm("mail_loop_no_recipients", `{`+raw(receivedMessage(rmime.DefaultMaxReceived+1, ""))+`}`, trusted)
	rm("mail_loop_spoofed", `{`+to+`,"from":"x@spool.test",`+raw(looping)+`}`, open)
	rm("mail_loop_recipient_unknown", `{"to":["no@spool.test"],`+raw(looping)+`}`, trusted)
	rm("too_nested", `{`+to+`,`+raw(nested)+`}`, trusted)
	rm("too_nested_no_recipients", `{`+raw(nestedRaw(rmime.DefaultMaxNestingDepth+1, ""))+`}`, trusted)
	rm("too_nested_spoofed", `{`+to+`,"from":"x@spool.test",`+raw(nested)+`}`, open)
	rm("too_nested_recipient_unknown", `{"to":["no@spool.test"],`+raw(nested)+`}`, trusted)
	rm("mail_loop_raw_twice", `{`+to+`,"raw_message":"QQ==",`+raw(looping)+`}`, trusted)
	rm("too_nested_raw_twice", `{`+to+`,"raw_message":"QQ==",`+raw(nested)+`}`, trusted)
	rm("too_nested_claims_our_verdicts", `{`+to+`,`+raw("Authentication-Results: restmail; spf=pass\r\n"+nested)+`}`, trusted)
	for _, action := range []pipeline.Action{pipeline.ActionReject, pipeline.ActionDefer, pipeline.ActionDiscard, pipeline.ActionQuarantine} {
		cases = append(cases, deliveryWireCase{name: "restmail/pipeline_" + string(action), route: "restmail", action: action, auth: trusted,
			body: `{` + to + `,"body_text":"text",` + raw(plain) + `}`})
	}
	cases = append(cases,
		deliveryWireCase{name: "restmail/no_content_type", route: "restmail", auth: trusted, contentType: "-", body: `{` + to + `,` + raw(plain) + `}`},
		deliveryWireCase{name: "restmail/other_content_type", route: "restmail", auth: trusted, contentType: "text/plain", body: `{` + to + `,` + raw(plain) + `}`},
	)
	return cases
}

var (
	deliveryWireTimes = regexp.MustCompile(`"[a-z_]*_at":"[^"]*"`)
	deliveryWireIDs   = regexp.MustCompile(`"(id|mailbox_id)":\d+`)
)

// deliveryStored is every row a delivery can leave, without what differs from run to run.
func deliveryStored(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var messages []models.Message
	if err := db.Order("id").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	var attachments []models.Attachment
	if err := db.Order("id").Find(&attachments).Error; err != nil {
		t.Fatal(err)
	}
	var usage []models.QuotaUsage
	if err := db.Order("mailbox_id").Find(&usage).Error; err != nil {
		t.Fatal(err)
	}
	var mailboxes []models.Mailbox
	if err := db.Order("id").Find(&mailboxes).Error; err != nil {
		t.Fatal(err)
	}
	var holds []models.Quarantine
	if err := db.Order("id").Find(&holds).Error; err != nil {
		t.Fatal(err)
	}
	var versions []models.CalendarEventVersion
	if err := db.Order("id").Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	stored := map[string]any{}
	rows := []map[string]any{}
	for _, m := range messages {
		rows = append(rows, map[string]any{
			"folder": m.Folder, "message_id": m.MsgID, "in_reply_to": m.InReplyTo, "references": m.References, "thread_id": m.ThreadID,
			"sender": m.Sender, "sender_name": m.SenderName, "to": string(m.RecipientsTo), "cc": string(m.RecipientsCc), "subject": m.Subject,
			"body_text": m.BodyText, "body_html": m.BodyHTML, "headers": string(m.Headers), "raw_is_text": m.RawOctets == nil,
			"raw_sha256": fmt.Sprintf("%x", sha256.Sum256(m.Source())), "raw_size": m.RawSize, "downgraded_size": m.DowngradedSize, "size_bytes": m.SizeBytes,
			"has_attachments": m.HasAttachments, "calendar": string(m.CalendarEventsRaw), "read": m.IsRead, "flagged": m.IsFlagged, "answered": m.IsAnswered,
			"starred": m.IsStarred, "draft": m.IsDraft, "tls": m.ReceivedTLS, "tls_version": m.TLSVersion, "require_tls": m.RequireTLS, "rule_class": m.RuleClass,
			"sealed": m.SealedHash, "internal_date_fixed": m.ReceivedAt.Year() == 2020,
		})
	}
	stored["messages"] = rows
	files := []map[string]any{}
	for _, a := range attachments {
		files = append(files, map[string]any{"name": a.Filename, "type": a.ContentType, "size": a.SizeBytes, "storage": a.StorageType, "ref": filepath.Base(a.StorageRef), "checksum": a.Checksum})
	}
	stored["attachments"] = files
	quotas := []map[string]any{}
	for _, u := range usage {
		quotas = append(quotas, map[string]any{"subject": u.SubjectBytes, "body": u.BodyBytes, "attachment": u.AttachmentBytes, "count": u.MessageCount})
	}
	stored["usage"] = quotas
	used := []int64{}
	for _, m := range mailboxes {
		used = append(used, m.QuotaUsedBytes)
	}
	stored["used"] = used
	kept := []map[string]any{}
	for _, q := range holds {
		kept = append(kept, map[string]any{"sender": q.Sender, "subject": q.Subject, "preview": q.BodyPreview, "raw_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(q.RawMessage))), "reason": q.QuarantineReason})
	}
	stored["holds"] = kept
	events := []map[string]any{}
	for _, v := range versions {
		events = append(events, map[string]any{"uid": v.UID, "sequence": v.Sequence, "method": v.Method, "summary": v.Summary, "linked": v.MessageID != nil})
	}
	stored["calendar"] = events
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// A message ID the server makes up is random, and so is the seal over it.
var (
	deliveryGeneratedID   = regexp.MustCompile(`(<|\\u003c)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}@`)
	deliveryWireEscapedID = regexp.MustCompile(`\\u003c[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}@`)
	deliveryWireSeal      = regexp.MustCompile(`"sealed[a-z_]*":"[^"]*"`)
)

func deliveryAnswer(t *testing.T, base spoolFixture, tc deliveryWireCase) deliveryWireAnswer {
	t.Helper()
	action := tc.action
	if action == "" {
		action = pipeline.ActionContinue
	}
	tx := base.db.Begin()
	defer tx.Rollback()
	d := spoolFixture{db: tx, mailbox: base.mailbox, engine: spoolEngine(t, spoolProbe{action: action})}
	contentType := tc.contentType
	switch contentType {
	case "":
		contentType = "application/json"
	case "-":
		contentType = ""
	}
	body := strings.ReplaceAll(tc.body, "MAILBOX", fmt.Sprint(base.mailbox.ID))
	w := httptest.NewRecorder()
	if tc.route == "gateway" {
		d.gateway(w, bytes.NewReader([]byte(body)), contentType)
	} else {
		d.restmail(w, bytes.NewReader([]byte(body)), contentType, *tc.auth)
	}
	answer := deliveryWireTimes.ReplaceAllString(w.Body.String(), `"at":"…"`)
	answer = deliveryWireIDs.ReplaceAllString(answer, `"$1":0`)
	stored := deliveryStored(t, tx)
	// A seal covers the Message-ID, so one the server made up leaves it differing too.
	if deliveryGeneratedID.MatchString(stored) {
		stored = deliveryWireSeal.ReplaceAllString(deliveryGeneratedID.ReplaceAllString(stored, `<generated@`), `"sealed":"…"`)
		answer = deliveryWireSeal.ReplaceAllString(deliveryGeneratedID.ReplaceAllString(answer, `<generated@`), `"sealed":"…"`)
		answer = deliveryWireEscapedID.ReplaceAllString(answer, `<generated@`)
	}
	return deliveryWireAnswer{Status: w.Code, Answer: answer, Stored: stored}
}

// Each route answers each request as it did when it bound the whole request, byte for byte, and
// stores the same rows: the same raw bytes, sizes, seal, body, headers, calendar events,
// attachments, quota charges and quarantine holds.
func TestDeliveryWireAnswers(t *testing.T) {
	base := newSpoolFixture(t, spoolProbe{action: pipeline.ActionContinue})
	cases := deliveryWireCases()
	recording := os.Getenv("RESTMAIL_RECORD_DELIVERY_ANSWERS") != ""
	want := map[string]deliveryWireAnswer{}
	if !recording {
		data, err := os.ReadFile(deliveryWireAnswers)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &want); err != nil {
			t.Fatal(err)
		}
		if len(want) != len(cases) {
			t.Fatalf("%d recorded answers for %d requests", len(want), len(cases))
		}
	}
	got := map[string]deliveryWireAnswer{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer := deliveryAnswer(t, base, tc)
			got[tc.name] = answer
			if recording {
				return
			}
			if answer != want[tc.name] {
				t.Fatalf("got\n%+v\nwant\n%+v", answer, want[tc.name])
			}
		})
	}
	if recording {
		data, err := json.MarshalIndent(got, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(deliveryWireAnswers), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(deliveryWireAnswers, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
