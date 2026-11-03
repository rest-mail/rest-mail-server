package message

import (
	"fmt"
	"mime"
	"reflect"
	"strings"
	"testing"
)

// FuzzContentTypeComments compares comment-bearing mail syntax with Go's MIME
// parser on the equivalent comment-free type. FormatMediaType supplies quoted
// and RFC 2231 values independently of our comment scanner. Hex comment text is
// ASCII; nested and escaped delimiters are added separately, outside values.
func FuzzContentTypeComments(f *testing.F) {
	for _, value := range []string{"", "plain", "a (b); c", "a\"(b)\\c", "caf\xc3\xa9", "\x00\r\n"} {
		f.Add(value, "comment")
	}
	f.Fuzz(func(t *testing.T, value, comment string) {
		if len(value) > 4096 || len(comment) > 4096 {
			t.Skip()
		}
		// Also exercise arbitrary malformed input for panic safety.
		_, _, _ = parseContentType(value)
		canonical := mime.FormatMediaType("application/x-example", map[string]string{"name": value})
		wantType, wantParams, wantErr := mime.ParseMediaType(canonical)
		if wantErr != nil {
			t.Fatalf("reference rejected formatted value: %v", wantErr)
		}
		note := fmt.Sprintf(`(nested (%x) escaped \) \( \\ ")`, comment)
		_, suffix, _ := strings.Cut(canonical, ";")
		for _, input := range []string{
			note + canonical + note,
			"application" + note + " / " + note + "x-example;" + note + suffix + note,
		} {
			gotType, gotParams, err := parseContentType(input)
			if err != nil || gotType != wantType || !reflect.DeepEqual(gotParams, wantParams) {
				t.Fatalf("parseContentType(%q) = %q, %v, %v; reference = %q, %v", input, gotType, gotParams, err, wantType, wantParams)
			}
		}
	})
}
