package api

import (
	"os"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

// openapi-example.json is written as go-oapifly writes an example file, so a sync of it
// changes only what it adds (#625).
func TestExampleFileIsWrittenAsOapiflyWritesIt(t *testing.T) {
	file, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := oapifly.MarshalExamples(file)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		for i := 0; i < len(got) && i < len(want); i++ {
			if got[i] != want[i] {
				lo := i - 80
				if lo < 0 {
					lo = 0
				}
				t.Fatalf("openapi-example.json differs from MarshalExamples at byte %d:\n got %q\nwant %q", i, got[lo:min(i+80, len(got))], want[lo:min(i+80, len(want))])
			}
		}
		t.Fatalf("openapi-example.json is %d bytes, MarshalExamples gives %d", len(got), len(want))
	}
}
