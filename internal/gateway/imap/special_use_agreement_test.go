package imap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	goimap "github.com/rest-mail/go-imap/pkg/imap"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// The API's folder listing and IMAP LIST read the one definition of folder roles, so they
// agree for every standard folder: the gateway gives each the RFC 6154 attribute of the role
// the API reports for it, INBOX none, and a custom folder neither (#1611).
func TestSpecialUseAgreesWithAPIRole(t *testing.T) {
	attribute := map[string]string{
		"inbox":   "",
		"sent":    goimap.SpecialUseSent,
		"drafts":  goimap.SpecialUseDrafts,
		"trash":   goimap.SpecialUseTrash,
		"junk":    goimap.SpecialUseJunk,
		"archive": goimap.SpecialUseArchive,
	}
	names := append(append([]string{}, rmail.StandardFolders...), "Projects", "sent", "Junk")
	type folder struct {
		Name string  `json:"name"`
		Role *string `json:"role"`
	}
	listing := make([]folder, 0, len(names))
	for _, name := range names {
		item := folder{Name: name}
		if role := rmail.RoleOf(name); role != "" {
			value := string(role)
			item.Role = &value
		}
		listing = append(listing, item)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": listing})
	}))
	t.Cleanup(srv.Close)

	got, err := (&mailbox{api: apiclient.New(srv.URL), token: "tok", accountID: 1}).Folders()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(listing) {
		t.Fatalf("LIST gave %d folders, want %d", len(got), len(listing))
	}
	for i, f := range got {
		want := ""
		if role := listing[i].Role; role != nil {
			var known bool
			if want, known = attribute[*role]; !known {
				t.Fatalf("API role %q of %q has no expected attribute", *role, f.Name)
			}
		} else if rmail.IsStandardFolder(f.Name) {
			t.Errorf("standard folder %q has no API role", f.Name)
		}
		if f.Name != listing[i].Name || f.SpecialUse != want {
			t.Errorf("LIST %q has %q, want %q (API role %v)", f.Name, f.SpecialUse, want, listing[i].Role)
		}
	}
}
