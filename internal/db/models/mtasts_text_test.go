package models

import (
	"regexp"
	"testing"
)

// The policy file lists the mx patterns as DNS names, an internationalised one in A-label
// form, and refuses one that is not a host name (RFC 8461 §3.2).
func TestMTASTSPolicyText(t *testing.T) {
	policy := MTASTSPolicy{Mode: "enforce", MXHosts: "mx.bücher.example, *.Bücher.example,,mx.example.com", MaxAge: 86400}
	got, err := policy.Text()
	want := "version: STSv1\nmode: enforce\nmx: mx.xn--bcher-kva.example\nmx: *.xn--bcher-kva.example\nmx: mx.example.com\nmax_age: 86400\n"
	if err != nil || got != want {
		t.Fatalf("Text = %q, %v; want %q", got, err, want)
	}
	if _, err := (MTASTSPolicy{Mode: "enforce", MXHosts: "mx.example.com,bad_.example", MaxAge: 1}).Text(); err == nil {
		t.Fatal("an mx that is not a host name made a policy file")
	}
}

// The record's id follows what senders fetch: the same policy file has the same id, however
// its mx list was written, and a change to the mode, an mx or max_age changes it. It is 1 to
// 32 letters and digits (RFC 8461 §3.1).
func TestMTASTSPolicyRecordID(t *testing.T) {
	id := func(p MTASTSPolicy) string {
		t.Helper()
		got, err := p.RecordID()
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	base := MTASTSPolicy{Mode: "testing", MXHosts: "mx1.example.com,mx2.example.com", MaxAge: 604800}
	first := id(base)
	if !regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`).MatchString(first) {
		t.Fatalf("id %q is not 1 to 32 letters and digits", first)
	}
	same := base
	same.MXHosts, same.ID, same.Active = " mx1.example.com , mx2.example.com", 9, false
	if id(same) != first {
		t.Errorf("the same policy file has another id: %s, %s", id(same), first)
	}
	for name, changed := range map[string]MTASTSPolicy{
		"mode":    {Mode: "enforce", MXHosts: base.MXHosts, MaxAge: base.MaxAge},
		"mx":      {Mode: base.Mode, MXHosts: "mx1.example.com", MaxAge: base.MaxAge},
		"max_age": {Mode: base.Mode, MXHosts: base.MXHosts, MaxAge: 86400},
	} {
		if id(changed) == first {
			t.Errorf("a new %s kept the id %s", name, first)
		}
	}
}
