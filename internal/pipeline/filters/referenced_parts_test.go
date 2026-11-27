package filters

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

var referencedBytes = bytes.Repeat([]byte("referenced \x00\xff part "), 300)

// referencedEmail is a message with one part carried as content and one by reference, and the
// same message with both as content, as the parts were handed to the pipeline before #2107.
func referencedEmail() (byRef, byContent *pipeline.EmailJSON) {
	content := base64.StdEncoding.EncodeToString(referencedBytes)
	byRef = unitEmail()
	byRef.Attachments = []pipeline.Attachment{
		{Filename: "content.bin", ContentType: "application/octet-stream", Disposition: "attachment", Size: 5, Content: base64.StdEncoding.EncodeToString([]byte("plain"))},
		{Filename: "stored.bin", ContentType: "application/octet-stream", Disposition: "attachment", Size: int64(len(referencedBytes)), Storage: "filesystem", Ref: "/objects/stored", Checksum: "sum"},
	}
	byRef.Inline = []pipeline.Attachment{{Filename: "logo.png", ContentType: "image/png", Disposition: "inline", ContentID: "logo", Size: int64(len(referencedBytes)), Storage: "s3", Ref: "objects/logo", Checksum: "sum"}}
	copied := *byRef
	byContent = &copied
	byContent.Attachments = append([]pipeline.Attachment(nil), byRef.Attachments...)
	byContent.Attachments[1].Content, byContent.Attachments[1].Storage, byContent.Attachments[1].Ref = content, "", ""
	byContent.Inline = append([]pipeline.Attachment(nil), byRef.Inline...)
	byContent.Inline[0].Content, byContent.Inline[0].Storage, byContent.Inline[0].Ref = content, "", ""
	return byRef, byContent
}

// testOpener holds the referenced parts' bytes, as the send that hands them in does.
type testOpener struct {
	mu     *sync.Mutex
	opened *int
	held   bool
}

func (o testOpener) Holds(att pipeline.Attachment) bool {
	return o.held && (att.Ref == "/objects/stored" || att.Ref == "objects/logo")
}

func (o testOpener) Open(_ context.Context, att pipeline.Attachment) (io.ReadCloser, error) {
	if !o.Holds(att) {
		return nil, errors.New("not held")
	}
	o.mu.Lock()
	*o.opened++
	o.mu.Unlock()
	return io.NopCloser(bytes.NewReader(referencedBytes)), nil
}

func withOpener(ctx context.Context, opened *int) context.Context {
	return pipeline.WithAttachmentOpener(ctx, testOpener{mu: &sync.Mutex{}, opened: opened, held: true})
}

type capturedRequest struct {
	mu     sync.Mutex
	body   []byte
	length int64
	n      int
}

func captureServer(t *testing.T, path, reply string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	got := &capturedRequest{}
	mux := http.NewServeMux()
	if path != "/" {
		mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.body, got.length, got.n = body, r.ContentLength, got.n+1
		got.mu.Unlock()
		_, _ = w.Write([]byte(reply))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, got
}

var boundaryPattern = regexp.MustCompile(`[0-9a-f]{60}`)

// sameBoundaries renders each of a message's boundaries as the one in the same place in want.
func sameBoundaries(got, want []byte) []byte {
	from, to := boundaryPattern.FindAll(got, -1), boundaryPattern.FindAll(want, -1)
	seen := map[string]string{}
	for i := range min(len(from), len(to)) {
		seen[string(from[i])] = string(to[i])
	}
	return boundaryPattern.ReplaceAllFunc(got, func(b []byte) []byte { return []byte(seen[string(b)]) })
}

// rspamd and ClamAV are sent the message as it always was, the referenced parts read from
// where they are stored as it is sent, with its length given (#2107).
func TestScannersStreamReferencedParts(t *testing.T) {
	for _, tc := range []struct {
		name, path, reply string
		factory           func(int64) pipeline.FilterFactory
	}{
		{"rspamd", "/checkv2", `{"action":"no action","score":0.1,"required_score":15}`, func(l int64) pipeline.FilterFactory { return NewRspamdWithLimits("", l) }},
		{"clamav", "/scan", `{"status":"OK"}`, func(l int64) pipeline.FilterFactory { return NewClamAVWithLimits("", l) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A scanner's verdict headers go on the message it is given.
			byRef, byContent := referencedEmail()
			want, err := rmime.Serialize(byContent)
			if err != nil {
				t.Fatal(err)
			}
			srv, got := captureServer(t, tc.path, tc.reply)
			f, err := tc.factory(int64(len(want)))([]byte(`{"url":"` + srv.URL + `","timeout_ms":2000,"fallback_action":"reject"}`))
			if err != nil {
				t.Fatal(err)
			}
			opened := 0
			res, err := f.Execute(withOpener(context.Background(), &opened), byRef)
			if err != nil || res.Action != pipeline.ActionContinue {
				t.Fatalf("scan: %+v %v", res, err)
			}
			// Measured once and sent once: each referenced part is opened twice.
			if got.n != 1 || got.length != int64(len(got.body)) || opened != 4 {
				t.Fatalf("requests=%d length=%d body=%d opened=%d", got.n, got.length, len(got.body), opened)
			}
			if !bytes.Equal(sameBoundaries(got.body, want), want) {
				t.Fatalf("the scanner was sent other bytes:\n%s\nwant\n%s", got.body, want)
			}

			srv2, got2 := captureServer(t, tc.path, tc.reply)
			small, err := tc.factory(int64(len(want) - 1))([]byte(`{"url":"` + srv2.URL + `","timeout_ms":2000,"fallback_action":"reject"}`))
			if err != nil {
				t.Fatal(err)
			}
			byRef, _ = referencedEmail()
			res, err = small.Execute(withOpener(context.Background(), &opened), byRef)
			if err != nil || got2.n != 0 || res.Action != pipeline.ActionReject || !strings.Contains(res.Log.Detail, "over the limit") {
				t.Fatalf("over the limit: requests=%d %+v %v", got2.n, res, err)
			}
		})
	}
}

// Extraction stores an empty part as the empty object it is, and keeps every part's
// disposition and Content-ID, so the message's rows can send it on as it came (#2107). A
// part whose bytes are not here is left as it is.
func TestExtractAttachmentsStoresEmptyPartsAndKeepsMetadata(t *testing.T) {
	f, err := NewExtractAttachments([]byte(`{"storage_path":"` + t.TempDir() + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	email := unitEmail()
	email.Attachments = []pipeline.Attachment{
		{Filename: "empty.txt", ContentType: "text/plain", Disposition: "attachment"},
		{Filename: "card.vcf", ContentType: "text/vcard", Disposition: "attachment", ContentID: "card", Size: 3, Content: base64.StdEncoding.EncodeToString([]byte("vcf"))},
		{Filename: "absent.bin", ContentType: "application/octet-stream", Disposition: "attachment", Size: 9},
	}
	email.Inline = []pipeline.Attachment{{Filename: "logo.png", ContentType: "image/png", Disposition: "inline", ContentID: "logo", Size: 4, Content: base64.StdEncoding.EncodeToString([]byte("logo"))}}
	res, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	out := res.Message
	empty, card, absent, logo := out.Attachments[0], out.Attachments[1], out.Attachments[2], out.Inline[0]
	if empty.Ref == "" || empty.Size != 0 || empty.Checksum != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty part: %+v", empty)
	}
	if card.Ref == "" || card.ContentID != "card" || logo.Ref == "" || logo.ContentID != "logo" || logo.Disposition != "inline" {
		t.Fatalf("metadata lost: %+v %+v", card, logo)
	}
	if absent.Ref != "" || absent.Size != 9 {
		t.Fatalf("a part without its bytes was stored: %+v", absent)
	}
}

// Webhook and JavaScript filters are sent the referenced parts as content, in the JSON they
// always had, and not past their limits; a reference nobody handed in is sent as it is.
func TestJSONFiltersGetReferencedPartsAsContent(t *testing.T) {
	byRef, byContent := referencedEmail()
	want, err := json.Marshal(byContent)
	if err != nil {
		t.Fatal(err)
	}
	build := func(t *testing.T, limit int64, url string) pipeline.Filter {
		t.Helper()
		f, err := NewWebhookWithLimit(limit)([]byte(`{"url":"` + url + `","timeout_ms":2000}`))
		if err != nil {
			t.Fatal(err)
		}
		f.(*webhookFilter).client.Transport = http.DefaultTransport
		return f
	}
	srv, got := captureServer(t, "/", `{"action":"continue"}`)
	opened := 0
	if _, err := build(t, int64(len(want)), srv.URL).Execute(withOpener(context.Background(), &opened), byRef); err != nil {
		t.Fatal(err)
	}
	if got.n != 1 || !bytes.Equal(got.body, want) || opened != 2 {
		t.Fatalf("webhook body:\n%s\nwant\n%s", got.body, want)
	}
	if byRef.Attachments[1].Content != "" || byRef.Inline[0].Ref == "" {
		t.Fatal("the pipeline's message was changed")
	}

	// The content alone is past the limit: nothing is read or sent.
	srv2, got2 := captureServer(t, "/", `{"action":"continue"}`)
	opened = 0
	res, err := build(t, int64(base64.StdEncoding.EncodedLen(len(referencedBytes))), srv2.URL).Execute(withOpener(context.Background(), &opened), byRef)
	if err != nil || got2.n != 0 || opened != 1 || res.Log.Result != "error" || !strings.Contains(res.Log.Detail, "over the limit") {
		t.Fatalf("over the limit: requests=%d opened=%d %+v %v", got2.n, opened, res, err)
	}

	// No opener, or a part it does not hold: the reference is sent as it is.
	unheld := testOpener{mu: &sync.Mutex{}, opened: &opened}
	for _, ctx := range []context.Context{context.Background(), pipeline.WithAttachmentOpener(context.Background(), unheld)} {
		sent, err := withReferencedContent(ctx, byRef, 1)
		if err != nil || sent != byRef {
			t.Fatalf("unheld parts: %v", err)
		}
	}

	// So is the duplicate filter's copy.
	dup, err := NewDuplicateWithLimit(int64(base64.StdEncoding.EncodedLen(len(referencedBytes))))([]byte(`{"webhook_url":"` + srv2.URL + `","timeout_ms":2000}`))
	if err != nil {
		t.Fatal(err)
	}
	opened = 0
	res, err = dup.Execute(withOpener(context.Background(), &opened), byRef)
	if err != nil || got2.n != 0 || opened != 1 || res.Log.Result != "error" || !strings.Contains(res.Log.Detail, "over the limit") {
		t.Fatalf("duplicate over the limit: requests=%d %+v %v", got2.n, res, err)
	}

	// The JavaScript filter is held to the sidecar's limit the same way.
	js := &jsFilter{sidecar: JSSidecar{URL: "http://127.0.0.1:1", MaxBodyBytes: 16}, onFailure: pipeline.ActionDefer}
	res, err = js.Execute(withOpener(context.Background(), &opened), byRef)
	if err != nil || res.Action != pipeline.ActionDefer || !strings.Contains(res.Log.Detail, "request body too large") {
		t.Fatalf("javascript over the limit: %+v %v", res, err)
	}
}
