package main

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSetupBanner(t *testing.T) {
	var out bytes.Buffer
	printSetupBanner(&out, "ABCD-EFGH-JKMN-PQRS-TVWX-YZ01", time.Now())
	s := out.String()
	for _, want := range []string{"ABCD-EFGH-JKMN-PQRS-TVWX-YZ01", "▀"} {
		if !strings.Contains(s, want) {
			t.Errorf("banner lacks %q:\n%s", want, s)
		}
	}
}

func TestSetupLink(t *testing.T) {
	u, _ := url.Parse(setupLink("ABCD-EFGH"))
	if u.Scheme != "restmail" || u.Host != "setup" || u.Query().Get("token") != "ABCD-EFGH" || u.Query().Has("server") {
		t.Errorf("link %v", u)
	}
}
