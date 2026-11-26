package apiclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestIMAPListingIncludesSnoozedOnEveryPage(t *testing.T) {
	for _, imap := range []bool{false, true} {
		t.Run(fmt.Sprintf("imap=%t", imap), func(t *testing.T) {
			var cursors []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/accounts/5/folders/INBOX/messages" {
					t.Errorf("unexpected route: %s", r.URL.Path)
				}
				query := r.URL.Query()
				for _, key := range []string{"include_snoozed", "include_imap_deleted"} {
					want := ""
					if imap {
						want = "true"
					}
					if query.Get(key) != want {
						t.Errorf("%s = %q, want %q", key, query.Get(key), want)
					}
				}
				cursor := query.Get("cursor")
				cursors = append(cursors, cursor)
				w.Header().Set("Content-Type", "application/json")
				if cursor == "" {
					_, _ = fmt.Fprint(w, `{"data":[{"id":7}],"pagination":{"has_more":true,"cursor":"next+page"}}`)
				} else {
					_, _ = fmt.Fprint(w, `{"data":[{"id":3}],"pagination":{"has_more":false}}`)
				}
			}))
			t.Cleanup(server.Close)
			client := New(server.URL)
			list := client.ListMessages
			if imap {
				list = client.ListIMAPMessages
			}
			response, err := list("token", 5, "INBOX")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cursors, []string{"", "next+page"}) {
				t.Errorf("cursors = %q", cursors)
			}
			if len(response.Data) != 2 || response.Data[0].ID != 3 || response.Data[1].ID != 7 {
				t.Errorf("messages are not complete and UID-ordered: %+v", response.Data)
			}
		})
	}
}
