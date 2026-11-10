package mime

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	gomessage "github.com/emersion/go-message"
	"golang.org/x/text/encoding/ianaindex"
)

// What charsetUnread answers is what the reader it stands in for does.
func TestCharsetUnreadAgreesWithTheReader(t *testing.T) {
	names := []string{"", " ", "x-none", "X-NONE", "x", "utf-7", "UTF-7", "utf-8", "UTF-8", "us-ascii", "iso-8859-1", "ISO-8859-1", "latin1", "windows-1252", "Windows-1252",
		"cp1252", "utf-16be", "utf-16", "utf-32", "gb18030", "big5", "shift_jis", "iso-2022-jp", "euc-kr", "koi8-r", "ansi_x3.110-1983", "ANSI_X3.110-1983", "x-utf_8j", "X-UTF_8J",
		"csISOLatin1", "isolatin1", "ibm037", "unicode-1-1-utf-7", " utf-8", "utf-8 ", "utf-8*en", "caf\xc3\xa9", "\xff", "K", "\xe2\x84\xaa", strings.Repeat("a", maxCharsetName), strings.Repeat("a", maxCharsetName+1)}
	// Every name the registry has an entry for, by the numbers it's indexed by.
	for _, e := range []string{"ibm", "cp", "iso-8859-", "windows-", "cswindows", "csiso", "csibm", "x-"} {
		for i := range 2000 {
			names = append(names, fmt.Sprintf("%s%d", e, i))
		}
	}
	for _, name := range names {
		_, err := gomessage.CharsetReader(name, strings.NewReader("a"))
		if got, want := charsetUnread(name), err != nil; got != want {
			enc, ianaErr := ianaindex.MIME.Encoding(name)
			t.Errorf("charsetUnread(%q) = %v, the reader says %v (%v; %v, %v)", name, got, want, err, enc, ianaErr)
		}
	}
	for _, name := range []string{"x-none", "X-NONE", "utf-7", "windows-1252"} {
		if allocated := testing.AllocsPerRun(100, func() { charsetUnread(name) }); allocated != 0 {
			t.Errorf("charsetUnread(%q) allocates %v times", name, allocated)
		}
	}
}

// A list of names nothing reads holds the limit of #1915 for a filter asking what filters ask: it was
// refused at its first word, and each word now reads through to its address.
func TestAddressListsOfUnreadNamesStayCheap(t *testing.T) {
	const size = 1 << 20
	for name, one := range map[string]string{
		"short":         "=?x?q?a?=<a@b>,",
		"upper":         "=?X-NONE?Q?A?=<a@b>,",
		"different":     "",
		"one long name": "=?x?q?a?= ",
	} {
		t.Run(name, func(t *testing.T) {
			var field string
			switch name {
			case "different":
				var b strings.Builder
				for i := 0; b.Len() < size; i++ {
					fmt.Fprintf(&b, "=?x%d?q?a?=<a@b>,", i)
				}
				field = b.String() + "last@b"
			case "one long name":
				field = strings.Repeat(one, size/len(one)) + "<last@b>"
			default:
				field = strings.Repeat(one, size/len(one)) + "last@b"
			}
			list := addressList(field)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			count, first := list.Len(), list.FirstAddress()
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("%d addresses, first %q: %d bytes, %.2fx", count, first, allocated, float64(allocated)/float64(len(field)))
			if count == 0 || first == "" {
				t.Fatalf("read %d, %q", count, first)
			}
			if allocated > 8*uint64(len(field)) {
				t.Errorf("reading %d bytes allocated %d", len(field), allocated)
			}
		})
	}
}
