package mail

import "testing"

func TestCanonicalMailboxAddress(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{" User@Example.TEST ", "user@example.test"},
		{"用户@例え.jp", "用户@xn--r8jz45g.jp"},
		{"MU\u0308NCHEN@STA\u0308DTE.DE", "münchen@xn--stdte-hra.de"},
		{"münchen@XN--STDTE-HRA.DE", "münchen@xn--stdte-hra.de"},
		{"user@faß.de", "user@xn--fa-hia.de"},
		{"user+tag@example.test", "user+tag@example.test"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := CanonicalMailboxAddress(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("canonical = %q, %v; want %q", got, err, tc.want)
			}
			if again, err := CanonicalMailboxAddress(got); err != nil || again != got {
				t.Fatalf("canonicalization is not idempotent: %q, %v", again, err)
			}
		})
	}
	for _, input := range []string{
		"", "not-an-address", "@example.test", "a@", "a@b@example.test",
		"Name <user@example.test>", `"quoted"@example.test`, "user(comment)@example.test",
		"a b@example.test", "a..b@example.test", "user@bad_domain.test",
		"user@example.test.", "user@[127.0.0.1]", "user@-bad.test",
		"a\r\nBcc: b@example.test", "a\x00@example.test", "a\x7f@example.test",
		"us\xffer@example.test", "user@ex\xffample.test",
	} {
		t.Run(input, func(t *testing.T) {
			if got, err := CanonicalMailboxAddress(input); err == nil || got != "" {
				t.Fatalf("invalid input canonicalized to %q, %v", got, err)
			}
		})
	}
}
