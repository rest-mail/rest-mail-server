package mail

import "strings"

// StripSubaddress applies RFC 5233 subaddressing ("plus addressing") to an
// email address: it splits a "+detail" tag off the local part and returns the
// base address with the tag removed, the detail, and whether a tag was present.
//
//	"user+amazon@example.com" -> ("user@example.com", "amazon", true)
//
// The detail is everything after the FIRST "+", so "user+a+b@d" yields base
// "user@d" and detail "a+b". ok is false — and the address is returned
// unchanged — when the local part has no "+", when the base local part would be
// empty ("+tag@domain"), or when the address has no "@".
func StripSubaddress(address string) (base, detail string, ok bool) {
	at := strings.LastIndex(address, "@")
	if at <= 0 {
		return address, "", false
	}
	local, domain := address[:at], address[at+1:]
	plus := strings.IndexByte(local, '+')
	if plus <= 0 {
		// plus < 0: no tag; plus == 0: empty base local part ("+tag").
		return address, "", false
	}
	return local[:plus] + "@" + domain, local[plus+1:], true
}
