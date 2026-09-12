package tlsutil

import "errors"

// ErrNotServedHere reports that the name a client asked for by SNI is not one
// this server is responsible for. It separates the two reasons a loader refuses:
// a name that is not ours, which the next loader in the chain may still know
// about, and a domain served here whose certificate is missing or unusable,
// which is a fault on this side and must not fall through to anything that would
// answer under a different name (issue #290).
var ErrNotServedHere = errors.New("not a domain served here")
