package mailimport

import "testing"

const (
	nfcCafe = "Caf\u00e9"
	nfdCafe = "Cafe\u0301"
)

// Two source folders that match after conversion are neither refused nor mixed, and the same
// source order gives the same names on a rerun.
func TestSourceFoldersNumberAMatchInOneRun(t *testing.T) {
	for run := 0; run < 2; run++ {
		names := newSourceFolders()
		for _, tc := range []struct{ name, want string }{
			{nfdCafe, nfdCafe}, // Free so far, and the API converts it.
			{nfcCafe, nfcCafe + " (2)"},
			{nfdCafe, nfdCafe}, // A later message of the same folder.
			{"INBOX", "INBOX"},
			{"Work", "Work"},
		} {
			if got := names.destination(tc.name); got != tc.want {
				t.Errorf("run %d: destination(%q) = %q, want %q", run, tc.name, got, tc.want)
			}
		}
	}
}

// The API may answer a converted name with a numbered folder. The rest of that source folder
// follows it, and any other answer is refused.
func TestDestinationsFollowTheFolderTheAPINames(t *testing.T) {
	d := NewDestinations()
	if got := d.For(nfdCafe); got != nfdCafe {
		t.Fatalf("first message goes to %q", got)
	}
	if !d.Record(nfdCafe, nfcCafe+" (2)") {
		t.Fatal("a numbered answer for a converted name was refused")
	}
	if got := d.For(nfdCafe); got != nfcCafe+" (2)" {
		t.Fatalf("next message goes to %q", got)
	}
	if !d.Record(nfdCafe, nfcCafe+" (2)") {
		t.Fatal("the same answer again was refused")
	}
	for _, tc := range []struct{ supplied, stored string }{
		{"Work", "Work (2)"}, // A name in NFC is never numbered.
		{"Work", "Other"},
		{nfdCafe, "Other (2)"},
		{nfdCafe, nfdCafe},
	} {
		if NewDestinations().Record(tc.supplied, tc.stored) {
			t.Errorf("Record(%q, %q) accepted", tc.supplied, tc.stored)
		}
	}
	if !NewDestinations().Record("Work", "Work") || !NewDestinations().Record(nfdCafe, nfcCafe) || !NewDestinations().Record("inbox", "INBOX") {
		t.Error("a plain answer was refused")
	}
}
