package filters

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/pipeline"
)

func TestRecipientCheck_EAI(t *testing.T) {
	db := openRecipientCheckTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	suffix := fmt.Sprintf("eai-%d.test", time.Now().UnixNano())
	unicodeDomain, asciiDomain := "städte."+suffix, "xn--stdte-hra."+suffix
	domain := models.Domain{Name: asciiDomain, Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	seed := func(local string) models.Mailbox {
		t.Helper()
		mb := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + asciiDomain, Active: true}
		if err := tx.Create(&mb).Error; err != nil {
			t.Fatal(err)
		}
		if err := grants.EnsureReceive(tx, &mb); err != nil {
			t.Fatal(err)
		}
		return mb
	}
	mb := seed("münchen")
	seed("cafe\u0301")
	inactive := seed("noe\u0308l")
	seed("noël")
	if err := tx.Model(&inactive).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	seed("a\u0301\u0323")
	seed("a\u0323\u0301")
	seed("ẹ\u0301")
	seed("e\u0301\u0323+tag")
	seed("e\u0323\u0301+tag")
	disabledTagged := seed("münchen+disabled")
	if err := tx.Model(&disabledTagged).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		address string
		want    pipeline.Action
	}{
		{"MU\u0308NCHEN@" + unicodeDomain, pipeline.ActionContinue},
		{"mu\u0308nchen+tag@" + unicodeDomain, pipeline.ActionContinue},
		{"café@" + unicodeDomain, pipeline.ActionContinue},
		{disabledTagged.Address, pipeline.ActionReject},
		{"ẹ\u0301+tag@" + unicodeDomain, pipeline.ActionReject},
		{inactive.Address, pipeline.ActionReject},
		{"noël@" + unicodeDomain, pipeline.ActionContinue},
		{"ạ\u0301@" + unicodeDomain, pipeline.ActionReject},
		{"munchen@" + unicodeDomain, pipeline.ActionReject},
		{"münchen@remote.test", pipeline.ActionReject},
		{"bad@@" + unicodeDomain, pipeline.ActionReject},
		{"münchen+tag@" + unicodeDomain + "\r\n", pipeline.ActionReject},
		{"münchen+bad\r\nInjected: x@" + unicodeDomain, pipeline.ActionReject},
	} {
		t.Run(tc.address, func(t *testing.T) {
			res := runRecipientCheck(t, tx, tc.address)
			if res.Action != tc.want {
				t.Fatalf("action = %v, want %v: %+v", res.Action, tc.want, res.Log)
			}
		})
	}
	if err := tx.Model(&mb).Updates(map[string]interface{}{"quota_bytes": 10, "quota_used_bytes": 10}).Error; err != nil {
		t.Fatal(err)
	}
	res := runRecipientCheck(t, tx, "mu\u0308nchen@"+unicodeDomain)
	if res.Action != pipeline.ActionReject || !strings.HasPrefix(res.RejectMsg, "452 4.2.2") {
		t.Fatalf("equivalent recipient escaped quota: %+v", res)
	}
	// The canonical alias must claim this variant before plus fallback reaches
	// the full base mailbox and rejects it for quota.
	alias := models.Alias{DomainID: domain.ID, SourceAddress: "münchen+shop@" + asciiDomain, DestinationAddress: "elsewhere@remote.test", Active: true}
	if err := tx.Create(&alias).Error; err != nil {
		t.Fatal(err)
	}
	if res := runRecipientCheck(t, tx, "mu\u0308nchen+shop@"+unicodeDomain); res.Action != pipeline.ActionContinue {
		t.Fatalf("explicit alias bypassed: %+v", res)
	}
	if err := tx.Model(&domain).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	if res := runRecipientCheck(t, tx, "münchen@"+unicodeDomain); res.Action != pipeline.ActionReject {
		t.Fatalf("inactive domain accepted: %+v", res)
	}

	// Identity lookup must not mutate the SMTP envelope or UTF-8 headers.
	if err := tx.Model(&domain).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	email := &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{MailFrom: "外部@例え.jp", RcptTo: []string{"café@" + unicodeDomain}},
		Headers:  pipeline.Headers{Subject: "Grüße"},
	}
	f := &recipientCheckFilter{db: tx}
	if res, err := f.Execute(context.Background(), email); err != nil || res.Action != pipeline.ActionContinue {
		t.Fatalf("execute: %+v, %v", res, err)
	}
	if email.Envelope.MailFrom != "外部@例え.jp" || email.Envelope.RcptTo[0] != "café@"+unicodeDomain || email.Headers.Subject != "Grüße" {
		t.Fatal("recipient resolution rewrote message")
	}
}
