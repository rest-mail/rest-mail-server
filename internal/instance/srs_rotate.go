package instance

import (
	"bytes"
)

const (
	srsSecretKey         = "RESTMAIL_SRS_SECRET="
	srsPreviousSecretKey = "RESTMAIL_SRS_PREVIOUS_SECRET="
)

// RotateSRSSecret returns the contents of an instance's secrets.env with a new SRS
// secret. The current one becomes the previous one, which is still accepted when
// checking an address, so a rotation invalidates nothing in flight (#775). The old
// previous one is dropped. An instance scaffolded before SRS has no secret yet, and
// gets its first one this way, with no previous. Every other line is left alone.
func RotateSRSSecret(secrets []byte) ([]byte, error) {
	secret, err := genSecret()
	if err != nil {
		return nil, err
	}
	var kept [][]byte
	var current []byte
	for _, line := range bytes.Split(bytes.TrimRight(secrets, "\n"), []byte("\n")) {
		switch {
		case bytes.HasPrefix(line, []byte(srsSecretKey)):
			current = bytes.TrimPrefix(line, []byte(srsSecretKey))
		case bytes.HasPrefix(line, []byte(srsPreviousSecretKey)):
		default:
			kept = append(kept, line)
		}
	}
	kept = append(kept, []byte(srsSecretKey+secret))
	if len(current) > 0 {
		kept = append(kept, append([]byte(srsPreviousSecretKey), current...))
	}
	return append(bytes.Join(kept, []byte("\n")), '\n'), nil
}
