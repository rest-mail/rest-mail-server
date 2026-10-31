package bootstrap

import "testing"

// TestPasswordTooShortCountsCharacters: the minimum is twelve characters,
// whatever their encoding. Four Japanese characters are twelve bytes and still
// too short; thirteen are long enough.
func TestPasswordTooShortCountsCharacters(t *testing.T) {
	for _, c := range []struct {
		password string
		short    bool
	}{
		{"eleven-char", true},
		{"twelve-chars", false},
		{"秘密の鍵", true},            // 4 characters, 12 bytes
		{"秘密の鍵は誰にも教えないでね", false}, // 14 characters
		{"パスワードを決めてください", false},  // 13 characters
		{"émoji-ÄÖÜ", true},       // 9 characters, 13 bytes
	} {
		if got := PasswordTooShort(c.password); got != c.short {
			t.Errorf("PasswordTooShort(%q) = %v, want %v", c.password, got, c.short)
		}
	}
}
