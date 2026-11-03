package imap

import (
	"fmt"
	"strings"
	"testing"
)

func TestImportFlagSyntaxAndBounds(t *testing.T) {
	for _, value := range []string{"", "bad keyword", "bad]", "bad(", "bad)", "bad{", "bad%", "bad*", "bad\"", `bad\`, "bad\x00", "bad\x7f", "Grüße", `\Unknown`, strings.Repeat("a", 256)} {
		if _, err := NormalizeImportFlags([]string{value}); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []string{"$Forwarded", "[allowed}", strings.Repeat("a", 255)} {
		if _, err := NormalizeImportFlags([]string{value}); err != nil {
			t.Fatal(value, err)
		}
	}
	keywords := make([]string, 100)
	for index := range keywords {
		keywords[index] = fmt.Sprintf("$keyword%d", index)
	}
	if _, err := NormalizeImportFlags(keywords); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeImportFlags(append(keywords, "$extra")); err == nil {
		t.Fatal("accepted 101 keywords")
	}
	normalized, err := NormalizeImportFlags([]string{"$Junk", "$junk", `\seen`, `\Deleted`})
	if err != nil || strings.Join(normalized, ",") != `$Junk,\Seen,\Deleted` {
		t.Fatal(normalized, err)
	}
}
