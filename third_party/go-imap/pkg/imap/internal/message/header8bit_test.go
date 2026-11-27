package message

import "testing"

// The scanner answers what HeaderHas8bit answers for the whole message, wherever the
// message is cut into pieces.
func TestHeader8bitScannerAgreesWithHeaderHas8bit(t *testing.T) {
	for _, msg := range []string{
		"", "\n", "\r\n", "\r", "\r\r\n", "\xe9", "\r\xe9",
		"Subject: a\r\n\r\n\xe9",
		"Subject: a\n\n\xe9",
		"Subject: \xe9\r\n\r\nbody",
		"Subject: a\r\nFrom: \xc3\xa9\r\n\r\n",
		"Subject: a\r\n\r\r\nX: \xe9\r\n",
		"Subject: a\r\n\r",
		"Subject: a\r\n\r\xe9",
		"Subject: a\r\r\nX: \xe9",
		"Subject: a\r\n \r\nX: \xe9\r\n\r\n",
		"Subject: a",
		"\r\nX: \xe9",
		"\nX: \xe9",
	} {
		want := HeaderHas8bit([]byte(msg))
		for cut := 0; cut <= len(msg); cut++ {
			for second := cut; second <= len(msg); second++ {
				var s Header8bitScanner
				_, _ = s.Write([]byte(msg[:cut]))
				_, _ = s.Write([]byte(msg[cut:second]))
				_, _ = s.Write([]byte(msg[second:]))
				if s.Found() != want {
					t.Fatalf("%q cut at %d and %d: Found %v, HeaderHas8bit %v", msg, cut, second, s.Found(), want)
				}
			}
		}
	}
}

func FuzzHeader8bitScanner(f *testing.F) {
	f.Add([]byte("Subject: a\r\n\r\n\xe9"), uint8(3))
	f.Add([]byte("Subject: \xe9\r\n"), uint8(9))
	f.Fuzz(func(t *testing.T, msg []byte, step uint8) {
		var s Header8bitScanner
		n := int(step) + 1
		for i := 0; i < len(msg); i += n {
			_, _ = s.Write(msg[i:min(i+n, len(msg))])
		}
		if want := HeaderHas8bit(msg); s.Found() != want {
			t.Fatalf("%q in steps of %d: Found %v, HeaderHas8bit %v", msg, n, s.Found(), want)
		}
	})
}
