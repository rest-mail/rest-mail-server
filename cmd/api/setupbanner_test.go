package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/setuplink"
)

const printedCode = "ABCD-EFGH-JKMN-PQRS-TVWX-YZ01"

func TestSetupBanner(t *testing.T) {
	var out bytes.Buffer
	printSetupBanner(&out, setuplink.DefaultBase, printedCode, time.Now())
	s := out.String()
	// The code to type, the link to open, and a QR code of it to scan.
	for _, want := range []string{printedCode, setuplink.Setup(setuplink.DefaultBase, "", printedCode), "▀"} {
		if !strings.Contains(s, want) {
			t.Errorf("banner lacks %q:\n%s", want, s)
		}
	}
}

// A server told to involve no site says so throughout.
func TestSetupBanner_WithoutALinkBase(t *testing.T) {
	var out bytes.Buffer
	printSetupBanner(&out, "", printedCode, time.Now())
	s := out.String()
	if !strings.Contains(s, "restmail://setup?") || strings.Contains(s, "rest-mail.com") {
		t.Errorf("banner:\n%s", s)
	}
}
