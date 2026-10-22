package handlers

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
)

const foldersPath = "/api/v1/accounts/{id}/folders"

func TestFolderCreate_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, foldersPath, "createFolder")
	wantNames := map[string][]string{
		"request": {"created", "name_empty", "name_missing", "name_wrong_type"},
		"201":     {"created"},
		"422":     {"name_empty", "name_missing", "name_wrong_type"},
	}
	gotNames := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		gotNames[status] = names(examples)
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("createFolder example names = %v, want %v", gotNames, wantNames)
	}

	wantRequests := map[string]interface{}{
		"created":         map[string]interface{}{"name": "Receipts"},
		"name_missing":    map[string]interface{}{},
		"name_empty":      map[string]interface{}{"name": ""},
		"name_wrong_type": map[string]interface{}{"name": float64(12)},
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Errorf("createFolder requests = %s, want %s", mustJSON(requests), mustJSON(wantRequests))
	}
}

// Account authorization reads the database before body validation. Run every
// named request through the real handler on an owned mailbox, including the
// successful control, and compare the entire response with its named example.
func TestFolderCreate_DocumentedExamples(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	h := NewMessageHandler(tx, nil, nil, "", nil)
	requests, responses := postExamples(t, foldersPath, "createFolder")

	for status, examples := range responses {
		for name, want := range examples {
			t.Run(status+"/"+name, func(t *testing.T) {
				body, ok := requests[name]
				if !ok {
					t.Fatalf("response %s/%s has no matching request", status, name)
				}
				before := countMessages(t, tx, fx.mailbox.ID)
				r := authzRequest(http.MethodPost, foldersPath, fx.mailbox.ID,
					map[string]string{"id": uintToStr(fx.mailbox.ID)}, string(mustJSON(body)))
				rr := httptest.NewRecorder()
				h.CreateFolder(rr, r)
				if status != strconv.Itoa(rr.Code) {
					t.Fatalf("%s: status %d, want %s; body %s", name, rr.Code, status, rr.Body)
				}
				if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, want) {
					t.Errorf("%s answered %s, want %s", name, rr.Body, mustJSON(want))
				}
				wantCount := before
				if rr.Code == http.StatusCreated {
					wantCount++
				}
				if got := countMessages(t, tx, fx.mailbox.ID); got != wantCount {
					t.Errorf("%s: folder markers = %d, want %d", name, got, wantCount)
				}
			})
		}
	}
}
