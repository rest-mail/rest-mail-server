package smtp

import "testing"

func TestValidateUTF8HeadersBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		wantError bool
	}{
		{"CRLF body with header-like text", "Subject: ASCII\r\n\r\nSubject: Grüß\xff", false},
		{"LF body before later CRLF separator", "Subject: ASCII\n\nGrüß\xff\r\n\r\nbody", false},
		{"empty CRLF header block", "\r\nGrüß\xff", false},
		{"empty LF header block", "\nGrüß\xff", false},
		{"headers without separator", "Subject: Grüß", true},
		{"LF folded headers", "Subject: ASCII\n Grüß\n\nbody", true},
		{"empty message", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateUTF8Headers([]byte(tc.raw), false); (err != nil) != tc.wantError {
				t.Fatalf("validateUTF8Headers = %v, want error=%t", err, tc.wantError)
			}
		})
	}
}
