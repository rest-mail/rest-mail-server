package server

import (
	"strconv"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// The ESEARCH result options (RFC 4731 §3.1), in the order a response gives
// them.
var searchReturnOptions = []string{"MIN", "MAX", "ALL", "COUNT"}

// esearchWanted reports whether returns asks for an ESEARCH response: any
// ESEARCH result option. SAVE alone asks for none (RFC 5182 §2.2, example 1).
func esearchWanted(returns map[string]bool) bool {
	for _, opt := range searchReturnOptions {
		if returns[opt] {
			return true
		}
	}
	return false
}

// parseSearchReturn takes SEARCH's optional result options, SP "RETURN" SP "("
// [search-return-opt *(SP search-return-opt)] ")" before the criteria (RFC
// 4466 §2.6), off args. returns is nil without them; an empty list asks for
// ALL (RFC 4731 §3.1). ok is false for a malformed list or an option this
// server does not know.
func parseSearchReturn(args string) (returns map[string]bool, rest string, ok bool) {
	if len(args) < 7 || !strings.EqualFold(args[:7], "RETURN ") {
		return nil, args, true
	}
	list, rest, found := strings.Cut(args[7:], ")")
	if !found || !strings.HasPrefix(list, "(") {
		return nil, "", false
	}
	rest, ok = wire.CutSingleSP(rest)
	if !ok {
		return nil, "", false
	}
	returns = map[string]bool{}
	if list = list[1:]; list == "" {
		returns["ALL"] = true
		return returns, rest, true
	}
	for _, opt := range strings.Split(list, " ") {
		opt = strings.ToUpper(opt)
		known := opt == "SAVE" // SEARCHRES (RFC 5182 §2.1)
		for _, o := range searchReturnOptions {
			known = known || o == opt
		}
		if !known {
			return nil, "", false
		}
		returns[opt] = true
	}
	return returns, rest, true
}

// esearchResponse is the one untagged ESEARCH answering a SEARCH with result
// options: "ESEARCH" (TAG tag) [UID] then each requested option's data (RFC
// 4466 §2.6.2, RFC 4731 §3.1). MIN, MAX and ALL are left out when nothing
// matched; COUNT never is. found is ascending.
func esearchResponse(tag string, uid bool, returns map[string]bool, found []uint32) string {
	var b strings.Builder
	// A tag holds no DQUOTE or backslash (RFC 3501 §9), so it quotes as it is.
	b.WriteString(`* ESEARCH (TAG "` + tag + `")`)
	if uid {
		b.WriteString(" UID")
	}
	for _, opt := range searchReturnOptions {
		if !returns[opt] || (opt != "COUNT" && len(found) == 0) {
			continue
		}
		switch opt {
		case "MIN":
			b.WriteString(" MIN " + strconv.FormatUint(uint64(found[0]), 10))
		case "MAX":
			b.WriteString(" MAX " + strconv.FormatUint(uint64(found[len(found)-1]), 10))
		case "ALL":
			b.WriteString(" ALL " + sequenceSet(found))
		case "COUNT":
			b.WriteString(" COUNT " + strconv.Itoa(len(found)))
		}
	}
	return b.String()
}

// sequenceSet writes ascending numbers as an RFC 3501 §9 sequence-set, each
// run of consecutive numbers as one range: 2,10:11.
func sequenceSet(numbers []uint32) string {
	var b strings.Builder
	for i := 0; i < len(numbers); {
		j := i
		for j+1 < len(numbers) && numbers[j+1] == numbers[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatUint(uint64(numbers[i]), 10))
		if j > i {
			b.WriteString(":" + strconv.FormatUint(uint64(numbers[j]), 10))
		}
		i = j + 1
	}
	return b.String()
}
