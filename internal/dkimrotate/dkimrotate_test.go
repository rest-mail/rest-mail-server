package dkimrotate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim"
)

func TestNextSelector(t *testing.T) {
	for current, want := range map[string]string{
		"":                              "restmail-1",
		"default":                       "restmail-1",
		"mail2026":                      "restmail-1",
		"restmail-":                     "restmail-1",
		"restmail-x":                    "restmail-1",
		"restmail-07":                   "restmail-1",
		"restmail--1":                   "restmail-1",
		"restmail-1":                    "restmail-2",
		"restmail-9":                    "restmail-10",
		"restmail-41":                   "restmail-42",
		"restmail-99999999999999999999": "restmail-1",
	} {
		if got := NextSelector(current); got != want {
			t.Errorf("NextSelector(%q) = %q, want %q", current, got, want)
		}
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	interval := 90 * 24 * time.Hour
	cases := []struct {
		name      string
		createdAt *time.Time
		interval  time.Duration
		want      bool
	}{
		{"young key", ago(10 * 24 * time.Hour), interval, false},
		{"exactly the interval", ago(interval), interval, true},
		{"old key", ago(200 * 24 * time.Hour), interval, true},
		{"unknown age", nil, interval, true},
		{"rotation off", ago(1000 * 24 * time.Hour), 0, false},
		{"rotation off, unknown age", nil, -time.Hour, false},
		{"installed in the future", ago(-time.Hour), interval, false},
	}
	for _, c := range cases {
		if got := Due(c.createdAt, c.interval, now); got != c.want {
			t.Errorf("%s: Due = %v, want %v", c.name, got, c.want)
		}
	}
}

func newRecord(t *testing.T) string {
	t.Helper()
	_, public, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	record, err := dkim.RecordValue(public)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func lookupReturning(want string, txts []string, err error) LookupTXT {
	return func(_ context.Context, name string) ([]string, error) {
		if name != want {
			return nil, errors.New("asked for " + name)
		}
		return txts, err
	}
}

func TestPublished(t *testing.T) {
	ctx := context.Background()
	record := newRecord(t)
	other := newRecord(t)
	key := record[strings.Index(record, "p=")+2:]
	name := "restmail-2._domainkey.example.com"

	matching := map[string][]string{
		"the same record":       {record},
		"among other TXT":       {"v=spf1 -all", other, record},
		"extra tags and spaces": {"v=DKIM1; t=s; h=sha256; k=rsa;\n p=" + key[:100] + " " + key[100:] + " ;"},
		"k= left to default":    {"v=DKIM1; p=" + key},
		"no v= tag":             {"k=rsa; p=" + key},
	}
	for label, txts := range matching {
		if err := Published(ctx, lookupReturning(name, txts, nil), "example.com", "restmail-2", record); err != nil {
			t.Errorf("%s: Published = %v, want nil", label, err)
		}
	}

	failing := map[string]struct {
		txts []string
		err  error
	}{
		"lookup fails":      {nil, errors.New("no such host")},
		"no records":        {nil, nil},
		"another key":       {[]string{other}, nil},
		"revoked key":       {[]string{"v=DKIM1; k=rsa; p="}, nil},
		"wrong key type":    {[]string{"v=DKIM1; k=ed25519; p=" + key}, nil},
		"wrong version":     {[]string{"v=DKIM2; k=rsa; p=" + key}, nil},
		"duplicated p= tag": {[]string{"v=DKIM1; p=" + key + "; p=" + key}, nil},
		"truncated key":     {[]string{"v=DKIM1; k=rsa; p=" + key[:len(key)-4]}, nil},
	}
	for label, c := range failing {
		err := Published(ctx, lookupReturning(name, c.txts, c.err), "example.com", "restmail-2", record)
		if !errors.Is(err, ErrNotPublished) {
			t.Errorf("%s: Published = %v, want ErrNotPublished", label, err)
		}
	}

	if err := Published(ctx, lookupReturning(name, []string{record}, nil), "example.com", "restmail-2", "v=DKIM1; k=rsa; p="); err == nil || errors.Is(err, ErrNotPublished) {
		t.Errorf("an expected record with no key: Published = %v, want a non-ErrNotPublished error", err)
	}
}
