package mail

import "testing"

func TestBareAddress(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{" Anchors@EXAMPLE.test ", "Anchors@example.test"},
		{"mu\u0308nchen@bücher.test", "mu\u0308nchen@xn--bcher-kva.test"},
		{`"quoted@local"@example.test`, `"quoted@local"@example.test`},
	} {
		got, err := BareAddress(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("BareAddress(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"", "bad", "Someone <a@example.test>", "a@example.test,b@example.test", "a@example.test\r\nBcc: x@example.test", "a@example.test (comment)"} {
		if got, err := BareAddress(input); err == nil {
			t.Errorf("BareAddress(%q) = %q, want error", input, got)
		}
	}
}
