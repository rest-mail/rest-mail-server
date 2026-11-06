package handlers

import (
	"reflect"
	"regexp"
	"testing"
)

func restmailDiscoverDomainPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	field, ok := reflect.TypeOf(restmailDiscoverQuery{}).FieldByName("Domain")
	if !ok {
		t.Fatal("restmailDiscoverQuery has no Domain")
	}
	pattern := field.Tag.Get("pattern")
	if pattern == "" {
		t.Fatal("restmailDiscoverQuery.Domain states no pattern")
	}
	return regexp.MustCompile(pattern)
}

// The description must never refuse a domain the handler looks up (#1748). A pattern cannot
// say everything IDNA checks, so it may permit a name the handler refuses, but not the
// reverse. Every code point goes through every position a name can hold it in, so a
// character IDNA maps to a dot, or removes, cannot be missed by a list of remembered cases.
func TestRestmailDiscoverPatternNeverRefusesWhatTheHandlerLooksUp(t *testing.T) {
	re := restmailDiscoverDomainPattern(t)
	shapes := []func(string) string{
		func(c string) string { return c },
		func(c string) string { return "a" + c },
		func(c string) string { return c + "a" },
		func(c string) string { return "a" + c + "b" },
		func(c string) string { return "a." + c },
		func(c string) string { return c + ".a" },
		func(c string) string { return "a" + c + "." },
		func(c string) string { return "." + c + "." },
		func(c string) string { return "a.b" + c },
		func(c string) string { return c + "a.b" },
	}
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		for _, shape := range shapes {
			name := shape(string(r))
			if _, err := validateDomainName(name); err == nil && !re.MatchString(name) {
				t.Errorf("the pattern refuses %q, which the handler looks up", name)
			}
		}
	}
}

func TestRestmailDiscoverPatternStatesTheShape(t *testing.T) {
	re := restmailDiscoverDomainPattern(t)
	// A name with an empty label is refused by the handler (#1762). The pattern still permits
	// these, which it may: a pattern cannot say everything the handler checks.
	for _, name := range []string{"a。", "。", ".a", "a..", "..", "a..b"} {
		if _, err := validateDomainName(name); err == nil {
			t.Errorf("%q: the handler now looks up a name with an empty label", name)
		}
	}
	for _, name := range []string{
		"restmail.test", "RESTMAIL.TEST", "restmail.test.", " restmail.test ", "\trestmail.test\n",
		"münchen.test", "MÜNCHEN.TEST", "xn--mnchen-3ya.test", "ß.test", "😀.test", "例え.テスト",
		"restmail。test", "restmail．test", "restmail｡test",
		"a.a", "1.2", "127.0.0.1",
	} {
		if _, err := validateDomainName(name); err != nil {
			t.Errorf("%q: the handler no longer looks this up: %v", name, err)
		}
		if !re.MatchString(name) {
			t.Errorf("the pattern refuses %q, which the handler looks up", name)
		}
	}
	for _, name := range []string{"", " ", "a", "A", "ü", "a.", "a. ", " a. ", ".", ". ", "localhost", "münchen", "a b", "a. b"} {
		if _, err := validateDomainName(name); err == nil {
			t.Errorf("%q: the handler now looks this up", name)
		}
		if re.MatchString(name) {
			t.Errorf("the pattern permits %q, which the handler refuses", name)
		}
	}
}
