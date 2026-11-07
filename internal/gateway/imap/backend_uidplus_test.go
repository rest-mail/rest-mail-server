package imap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// ── Stateful fake REST API ────────────────────────────────────────────────
//
// fakeAPI is an in-memory stand-in for the rest-mail REST API, serving just the
// endpoints the IMAP gateway touches: login, folder listing, delivery, message
// detail/raw, flag/folder PATCH and delete. It records PATCH and DELETE calls so
// tests can assert the side effects of COPY/MOVE/UID EXPUNGE, and its nextID is
// the ID (== UID) the next delivery is assigned, letting a test pin the UID an
// APPENDUID / COPYUID must report.

type fakeMsg struct {
	id          uint
	folder      string
	raw         string
	subject     string
	sender      string
	senderNm    string
	isRead      bool
	isFlagged   bool
	isStarred   bool
	isDraft     bool
	imapDeleted bool
	keywords    []string
	receivedAt  time.Time
	// modSeq is the message's mod-sequence, from its folder's (RFC 7162).
	modSeq uint64
}

type patchRecord struct {
	id      uint
	updates map[string]interface{}
}

type fakeAPI struct {
	mu      sync.Mutex
	nextID  uint
	msgs    map[uint]*fakeMsg
	patches []patchRecord
	deletes []uint
	imports []apiclient.ImportRequest
	// marks is the highest ID each folder held before a delete removed it,
	// so the folder listing's uid_next does not fall, as the API keeps it.
	marks map[string]uint
	// modSeqs is each folder's HIGHESTMODSEQ and vanished the UIDs that left
	// each folder with the mod-sequence they left at, kept as the API's
	// triggers keep them: every visible change raises the folder's value.
	modSeqs  map[string]uint64
	vanished map[string]map[uint]uint64
	// importStatus, when non-zero, is the HTTP status the import endpoint
	// returns instead of a normal 200; importBody is the JSON body to send with
	// it. Together they let a test simulate a quarantined/discarded delivery
	// (200 with no data.id) or a hard delivery failure.
	importStatus int
	importBody   interface{}
	// transfers records each set copy or move; transferFailAt, when non-zero,
	// fails a set on that message (1-based) after the ones before it, and the
	// API's transaction leaves every message as it was.
	transfers      []apiclient.TransferRequest
	transferFailAt int
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{nextID: 100, msgs: map[uint]*fakeMsg{}}
}

func (f *fakeAPI) seed(id uint, folder, raw, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(&fakeMsg{
		id: id, folder: folder, raw: raw, subject: subject,
		sender: "sender@example.com", senderNm: "Sender",
	})
}

// put stores m in its folder with the folder's next mod-sequence. f.mu is held.
func (f *fakeAPI) put(m *fakeMsg) {
	f.msgs[m.id] = m
	m.modSeq = f.bump(m.folder)
	delete(f.vanished[m.folder], m.id)
}

// bump raises folder's mod-sequence and returns the new value. f.mu is held.
func (f *fakeAPI) bump(folder string) uint64 {
	if f.modSeqs == nil {
		f.modSeqs = map[string]uint64{}
	}
	f.modSeqs[folder]++
	return f.modSeqs[folder]
}

// leave records that the message id left folder, at a new mod-sequence. f.mu is held.
func (f *fakeAPI) leave(folder string, id uint) {
	if f.vanished == nil {
		f.vanished = map[string]map[uint]uint64{}
	}
	if f.vanished[folder] == nil {
		f.vanished[folder] = map[uint]uint64{}
	}
	f.vanished[folder][id] = f.bump(folder)
}

// modSeqOf is the mod-sequence of message id, 0 when there is none.
func (f *fakeAPI) modSeqOf(id uint) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.msgs[id]; m != nil {
		return m.modSeq
	}
	return 0
}

func (f *fakeAPI) exists(id uint) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.msgs[id]
	return ok
}

func (f *fakeAPI) deleted(id uint) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deletes {
		if d == id {
			return true
		}
	}
	return false
}

// patchesFor returns the update maps PATCHed against id, in call order.
func (f *fakeAPI) patchesFor(id uint) []map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]interface{}
	for _, p := range f.patches {
		if p.id == id {
			out = append(out, p.updates)
		}
	}
	return out
}

// folderPatched returns the last folder a message was PATCHed into, or "".
func folderPatched(api *fakeAPI, id uint) string {
	last := ""
	for _, u := range api.patchesFor(id) {
		if folder, ok := u["folder"].(string); ok {
			last = folder
		}
	}
	return last
}

var fakeReceivedAt = time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC)

func (f *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/gateway-sessions", f.login)
	mux.HandleFunc("DELETE /api/v1/gateway-sessions/current", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/v1/accounts/{acct}/folders", f.listFolders)
	mux.HandleFunc("GET /api/v1/accounts/{acct}/folders/{folder}/messages", f.listMessages)
	mux.HandleFunc("GET /api/v1/accounts/{acct}/folders/{folder}/vanished", f.listVanished)
	mux.HandleFunc("POST /api/v1/accounts/{acct}/messages/import", f.importMessage)
	mux.HandleFunc("POST /api/v1/accounts/{acct}/messages/transfer", f.transfer)
	mux.HandleFunc("GET /api/v1/messages/{id}/raw", f.raw)
	mux.HandleFunc("GET /api/v1/messages/{id}", f.getMessage)
	mux.HandleFunc("PATCH /api/v1/messages/{id}", f.patch)
	mux.HandleFunc("DELETE /api/v1/messages/{id}", f.delete)
	return mux
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func pathID(r *http.Request) uint {
	n, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	return uint(n)
}

func (f *fakeAPI) login(w http.ResponseWriter, _ *http.Request) {
	var lr struct {
		Data apitypes.GatewaySessionOpened `json:"data"`
	}
	lr.Data.Token, lr.Data.Protocol = "rmgs_test", "imap"
	lr.Data.User.ID = 7
	lr.Data.CredentialID, lr.Data.CredentialKind = 1, "password"
	lr.Data.User.Email = "alice@example.com"
	writeJSON(w, lr)
}

func (f *fakeAPI) listMessages(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	f.mu.Lock()
	var out []apiclient.MessageSummary
	for _, m := range f.msgs {
		if m.folder != folder {
			continue
		}
		// An imported message lists with the internal date it was given; a
		// seeded one with the fake's fixed arrival time.
		received := m.receivedAt
		if received.IsZero() {
			received = fakeReceivedAt
		}
		out = append(out, apiclient.MessageSummary{
			ID:          m.id,
			Folder:      m.folder,
			Subject:     m.subject,
			Sender:      m.sender,
			SenderName:  m.senderNm,
			RawSize:     len(m.raw),
			IMAPDeleted: m.imapDeleted,
			Keywords:    m.keywords,
			IsRead:      m.isRead,
			IsFlagged:   m.isFlagged,
			IsStarred:   m.isStarred,
			IsDraft:     m.isDraft,
			ModSeq:      m.modSeq,
			ReceivedAt:  received,
		})
	}
	f.mu.Unlock()
	// The client reverses newest-first into oldest-first, so return descending ID.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	writeJSON(w, apiclient.MessageListResponse{Data: out})
}

func (f *fakeAPI) importMessage(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req apiclient.ImportRequest
	_ = json.Unmarshal(body, &req)
	var wire struct {
		AlwaysStore bool `json:"always_store"`
	}
	_ = json.Unmarshal(body, &wire)
	f.mu.Lock()
	f.imports = append(f.imports, req)
	// A test may force the outcome: a 200 with no data.id models an answer that
	// names no stored message; a >=400 status models a hard failure.
	if f.importStatus != 0 {
		status, body := f.importStatus, f.importBody
		f.mu.Unlock()
		w.WriteHeader(status)
		if body != nil {
			writeJSON(w, body)
		}
		return
	}
	// The message is created directly in its destination folder (empty = INBOX),
	// carrying the flags and internal date the request gave.
	folder := req.Folder
	if folder == "" {
		folder = "INBOX"
	}
	// As the real import does, identical octets in the folder are a rerun: the
	// message already there is the answer, unless the caller asked to store.
	if !wire.AlwaysStore {
		for _, existing := range f.msgs {
			if existing.folder == folder && existing.raw == string(req.RawMessage) {
				f.mu.Unlock()
				var resp apiclient.ImportResponse
				resp.Data.ID, resp.Data.Folder = existing.id, folder
				writeJSON(w, resp)
				return
			}
		}
	}
	id := f.nextID
	f.nextID++
	m := &fakeMsg{id: id, folder: folder, raw: string(req.RawMessage), receivedAt: req.InternalDate}
	m.isRead = slices.Contains(req.Flags, `\Seen`)
	m.isFlagged = slices.Contains(req.Flags, `\Flagged`)
	m.isStarred = m.isFlagged
	m.isDraft = slices.Contains(req.Flags, `\Draft`)
	m.imapDeleted = slices.Contains(req.Flags, `\Deleted`)
	for _, flag := range req.Flags {
		if !strings.HasPrefix(flag, `\`) {
			m.keywords = append(m.keywords, flag)
		}
	}
	f.put(m)
	f.mu.Unlock()
	var resp apiclient.ImportResponse
	resp.Data.ID = id
	resp.Data.Imported = true
	resp.Data.Folder = folder
	writeJSON(w, resp)
}

// transfer copies or moves a set as the API does, in one transaction: every
// message gets a new ID in the destination with its flags and source, or, on
// a failure, nothing changes.
func (f *fakeAPI) transfer(w http.ResponseWriter, r *http.Request) {
	var req apiclient.TransferRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transfers = append(f.transfers, req)
	for i, id := range req.MessageIDs {
		if f.transferFailAt == i+1 {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "internal_error", "message": "Internal server error"}})
			return
		}
		if m := f.msgs[id]; m == nil || m.folder != req.SourceFolder {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "message_not_found", "message": "Message not found"}})
			return
		}
	}
	var ids []uint
	for _, id := range req.MessageIDs {
		copied := *f.msgs[id]
		copied.id, copied.folder = f.nextID, req.Folder
		copied.keywords = slices.Clone(copied.keywords)
		f.msgs[copied.id] = &copied
		ids = append(ids, copied.id)
		f.nextID++
		if req.Move {
			delete(f.msgs, id)
		}
	}
	writeJSON(w, map[string]any{"data": map[string]any{"ids": ids}})
}

// lastImport returns the most recent ImportRequest the gateway sent.
func (f *fakeAPI) lastImport(t *testing.T) apiclient.ImportRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.imports) == 0 {
		t.Fatal("no import was made")
	}
	return f.imports[len(f.imports)-1]
}

// folderOf returns the folder a stored message currently lives in, or "".
func folderOf(api *fakeAPI, id uint) string {
	api.mu.Lock()
	defer api.mu.Unlock()
	if m := api.msgs[id]; m != nil {
		return m.folder
	}
	return ""
}

func (f *fakeAPI) getMessage(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	f.mu.Lock()
	m := f.msgs[id]
	var dr apiclient.MessageDetailResponse
	if m != nil {
		dr = f.detail(m)
	}
	f.mu.Unlock()
	if m == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, dr)
}

// detail is m as the API answers it. f.mu is held.
func (f *fakeAPI) detail(m *fakeMsg) apiclient.MessageDetailResponse {
	received := m.receivedAt
	if received.IsZero() {
		received = fakeReceivedAt
	}
	var dr apiclient.MessageDetailResponse
	dr.Data = apiclient.MessageSummary{
		ID:          m.id,
		MailboxID:   7,
		Folder:      m.folder,
		Subject:     m.subject,
		Sender:      m.sender,
		SenderName:  m.senderNm,
		RawSize:     len(m.raw),
		IMAPDeleted: m.imapDeleted,
		Keywords:    m.keywords,
		IsRead:      m.isRead,
		IsFlagged:   m.isFlagged,
		IsStarred:   m.isStarred,
		IsDraft:     m.isDraft,
		ModSeq:      m.modSeq,
		ReceivedAt:  received,
	}
	return dr
}

func (f *fakeAPI) raw(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	f.mu.Lock()
	m := f.msgs[id]
	f.mu.Unlock()
	if m == nil || m.raw == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	_, _ = w.Write([]byte(m.raw))
}

func (f *fakeAPI) patch(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var updates map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&updates)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches = append(f.patches, patchRecord{id: id, updates: updates})
	m := f.msgs[id]
	if m == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// unchanged_since is checked against the stored mod_seq before anything is
	// written, as the API's conditional UPDATE does (RFC 7162 §3.1.3).
	if since, ok := updates["unchanged_since"].(float64); ok && m.modSeq > uint64(since) {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "message_modified"}})
		return
	}
	before := *m
	setFlag := func(key string, flag *bool) {
		if v, ok := updates[key].(bool); ok {
			*flag = v
		}
	}
	setFlag("imap_deleted", &m.imapDeleted)
	setFlag("is_read", &m.isRead)
	setFlag("is_flagged", &m.isFlagged)
	setFlag("is_starred", &m.isStarred)
	setFlag("is_draft", &m.isDraft)
	if keywords, ok := updates["keywords"].([]interface{}); ok {
		m.keywords = nil
		for _, keyword := range keywords {
			m.keywords = append(m.keywords, keyword.(string))
		}
	}
	if folder, ok := updates["folder"].(string); ok && folder != m.folder {
		f.leave(m.folder, m.id)
		m.folder = folder
		f.put(m)
	} else if m.imapDeleted != before.imapDeleted || m.isRead != before.isRead || m.isFlagged != before.isFlagged ||
		m.isStarred != before.isStarred || m.isDraft != before.isDraft || !slices.Equal(m.keywords, before.keywords) {
		m.modSeq = f.bump(m.folder)
	}
	writeJSON(w, f.detail(m))
}

// listVanished answers listVanishedUIDs from the recorded departures, two at a
// time so the client must follow the pages.
func (f *fakeAPI) listVanished(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	after, _ := strconv.ParseUint(r.URL.Query().Get("after_uid"), 10, 64)
	f.mu.Lock()
	var out []apiclient.VanishedUID
	for uid, modSeq := range f.vanished[r.PathValue("folder")] {
		if modSeq > since && uint64(uid) > after {
			out = append(out, apiclient.VanishedUID{UID: uint64(uid), ModSeq: modSeq})
		}
	}
	f.mu.Unlock()
	slices.SortFunc(out, func(a, b apiclient.VanishedUID) int { return int(a.UID) - int(b.UID) })
	resp := struct {
		Data       []apiclient.VanishedUID `json:"data"`
		Pagination map[string]any          `json:"pagination"`
	}{Data: out, Pagination: map[string]any{"has_more": false}}
	if len(out) > 2 {
		resp.Data = out[:2]
		resp.Pagination = map[string]any{"has_more": true, "cursor": strconv.FormatUint(out[1].UID, 10)}
	}
	writeJSON(w, resp)
}

// listFolders lists the standard folders, as the API always does, and every
// folder a message is in or was in, with uid_next one above the highest ID it
// has held.
func (f *fakeAPI) listFolders(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	highest := map[string]uint{"INBOX": 0, "Sent": 0, "Drafts": 0, "Trash": 0, "Spam": 0, "Archive": 0}
	for folder, id := range f.marks {
		highest[folder] = max(highest[folder], id)
	}
	for _, m := range f.msgs {
		highest[m.folder] = max(highest[m.folder], m.id)
	}
	var resp apiclient.FolderListResponse
	for folder, id := range highest {
		resp.Data = append(resp.Data, apiclient.Folder{Folder: folder, UIDNext: uint64(id) + 1, HighestModSeq: max(f.modSeqs[folder], 1)})
	}
	f.mu.Unlock()
	slices.SortFunc(resp.Data, func(a, b apiclient.Folder) int { return strings.Compare(a.Folder, b.Folder) })
	writeJSON(w, resp)
}

func (f *fakeAPI) delete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	f.mu.Lock()
	f.deletes = append(f.deletes, id)
	if m := f.msgs[id]; m != nil {
		if f.marks == nil {
			f.marks = map[string]uint{}
		}
		f.marks[m.folder] = max(f.marks[m.folder], id)
		f.leave(m.folder, id)
	}
	delete(f.msgs, id)
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// newUnitMailbox wires a *mailbox straight to a fakeAPI-backed server, skipping
// the login round-trip so tests can call the UID methods directly.
func newUnitMailbox(t *testing.T, api *fakeAPI) *mailbox {
	t.Helper()
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	return &mailbox{api: apiclient.New(srv.URL), email: "alice@example.com", token: "tok", accountID: 7}
}

// ── toUID (pure) ──────────────────────────────────────────────────────────

func TestToUID_InRange(t *testing.T) {
	cases := []struct {
		in   uint
		want uint32
	}{
		{0, 0},
		{1, 1},
		{4242, 4242},
		{math.MaxUint32, math.MaxUint32},
	}
	for _, c := range cases {
		if got := toUID(c.in); got != c.want {
			t.Errorf("toUID(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestToUID_ClampsOutOfRange(t *testing.T) {
	if uint64(^uint(0)) <= math.MaxUint32 {
		t.Skip("uint is 32-bit here; no value exceeds the uint32 UID space")
	}
	big := uint(math.MaxUint32) + 1
	if got := toUID(big); got != 0 {
		t.Errorf("toUID(%d) = %d, want 0 (out-of-range must clamp, not wrap)", big, got)
	}
}

// ── UID methods (direct) ──────────────────────────────────────────────────

func TestAppendUID_ReturnsDeliveredUID(t *testing.T) {
	api := newFakeAPI()
	api.nextID = 4242 // the ID (== UID) this delivery will be assigned
	m := newUnitMailbox(t, api)

	uid, err := m.AppendUID("INBOX", goimap.FlagUpdate{}, []byte("Subject: Hi\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("AppendUID: %v", err)
	}
	if uid != 4242 {
		t.Fatalf("AppendUID uid = %d, want 4242 (the delivered message ID)", uid)
	}
	if !api.exists(4242) {
		t.Errorf("AppendUID did not deliver the message")
	}
}

func TestCopyUID_ReturnsDeliveredUID(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", "Subject: Src\r\n\r\nsrc\r\n", "Src")
	api.nextID = 777
	m := newUnitMailbox(t, api)

	uid, err := m.CopyUID(1, "Archive")
	if err != nil {
		t.Fatalf("CopyUID: %v", err)
	}
	if uid != 777 {
		t.Fatalf("CopyUID uid = %d, want 777 (the new copy's delivered ID)", uid)
	}
	if got := folderOf(api, 777); got != "Archive" {
		t.Errorf("copy created in %q, want Archive", got)
	}
}

// TestMoveUID_AssignsFreshDestinationUID proves MOVE gives the moved message a
// fresh, higher UID in the destination (a re-delivery) and removes the source,
// rather than carrying the source UID across. Reusing the source UID produces a
// non-ascending UID in the destination and breaks clients doing
// UID FETCH <lastuid+1>:* incremental sync (RFC 3501 §2.3.1.1).
func TestMoveUID_AssignsFreshDestinationUID(t *testing.T) {
	api := newFakeAPI()
	api.seed(50, "INBOX", "Subject: Mv\r\n\r\nmv\r\n", "Mv")
	// The next delivery is assigned nextID, standing in for a destination that
	// already holds higher UIDs; that ID must become the moved message's new UID.
	api.nextID = 500
	m := newUnitMailbox(t, api)

	uid, err := m.MoveUID(50, "Archive")
	if err != nil {
		t.Fatalf("MoveUID: %v", err)
	}
	if uid == 50 {
		t.Fatalf("MoveUID reused source UID 50; MOVE must assign a fresh destination UID")
	}
	if uid != 500 {
		t.Fatalf("MoveUID uid = %d, want 500 (the freshly delivered ID in the destination)", uid)
	}
	if !api.deleted(50) {
		t.Errorf("MoveUID left source message 50 in place; MOVE must remove it")
	}
	if !api.exists(500) {
		t.Errorf("MoveUID did not deliver the moved message into the destination")
	}
	if got := folderOf(api, 500); got != "Archive" {
		t.Errorf("moved copy created in %q, want Archive", got)
	}
}

func TestUIDValidity_IsConstantOne(t *testing.T) {
	m := &mailbox{}
	for _, folder := range []string{"INBOX", "Archive", "Sent"} {
		v, err := m.UIDValidity(folder)
		if err != nil {
			t.Fatalf("UIDValidity(%q): %v", folder, err)
		}
		if v != 1 {
			t.Errorf("UIDValidity(%q) = %d, want 1", folder, v)
		}
	}
}

// TestBaseMethods_DelegateToUIDForms proves the base Mailbox methods still perform
// their side effects after being reduced to thin wrappers over the UID forms.
func TestBaseMethods_DelegateToUIDForms(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", "Subject: S\r\n\r\ns\r\n", "S")
	api.seed(2, "INBOX", "Subject: T\r\n\r\nt\r\n", "T")
	api.nextID = 600
	m := newUnitMailbox(t, api)

	if err := m.Append("INBOX", goimap.FlagUpdate{}, []byte("Subject: A\r\n\r\na\r\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !api.exists(600) {
		t.Errorf("base Append delivered no message")
	}

	if err := m.Copy(1, "Archive"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if !api.exists(601) {
		t.Errorf("base Copy delivered no message")
	}

	if err := m.Move(2, "Trash"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	// Move is a re-delivery (602, the next ID) into Trash plus deletion of the
	// source, not a folder relabel of message 2.
	if !api.deleted(2) {
		t.Errorf("base Move left source message 2 in place")
	}
	if got := folderOf(api, 602); got != "Trash" {
		t.Errorf("base Move created the moved copy in %q, want Trash", got)
	}
}

// ── End-to-end transcript over a real imapserver.Session ──────────────────────

// transcript drives a real imapserver.Session over net.Pipe against the rest-mail
// Backend, so the wire-level UIDPLUS / MOVE behaviour (capabilities, APPENDUID /
// COPYUID response codes, UID EXPUNGE) is exercised through the actual server.
type transcript struct {
	t    *testing.T
	conn net.Conn
	cr   *bufio.Reader
	cw   *bufio.Writer
	done chan struct{}
}

func newTranscript(t *testing.T, backend goimap.Backend, configure ...func(*imapserver.Session)) *transcript {
	t.Helper()
	client, server := net.Pipe()
	sess := imapserver.NewSession(server, backend, "imap.test", nil, goimap.NopLimiter{})
	for _, configureSession := range configure {
		configureSession(sess)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()

	h := &transcript{
		t:    t,
		conn: client,
		cr:   bufio.NewReader(client),
		cw:   bufio.NewWriter(client),
		done: done,
	}
	t.Cleanup(func() {
		_ = client.Close()
		<-h.done
	})

	if g := h.readLine(); !strings.HasPrefix(g, "* OK") {
		t.Fatalf("greeting = %q, want * OK...", g)
	}
	return h
}

func (h *transcript) readLine() string {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := h.cr.ReadString('\n')
	if err != nil {
		h.t.Fatalf("readLine: %v (partial %q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

func (h *transcript) writeLine(s string) {
	h.t.Helper()
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = h.cw.WriteString(s)
	if err := h.cw.Flush(); err != nil {
		h.t.Fatalf("write: %v", err)
	}
}

func (h *transcript) send(format string, args ...interface{}) {
	h.writeLine(fmt.Sprintf(format, args...) + "\r\n")
}

// readResponse consumes untagged lines until the tagged completion line for tag.
func (h *transcript) readResponse(tag string) (untagged []string, status string) {
	h.t.Helper()
	for {
		line := h.readLine()
		if strings.HasPrefix(line, tag+" ") {
			return untagged, line
		}
		untagged = append(untagged, line)
	}
}

func (h *transcript) command(tag, format string, args ...interface{}) (untagged []string, status string) {
	h.t.Helper()
	h.send("%s %s", tag, fmt.Sprintf(format, args...))
	return h.readResponse(tag)
}

func (h *transcript) login(tag string) string {
	h.t.Helper()
	_, status := h.command(tag, "LOGIN alice@example.com s3cret")
	if !strings.Contains(status, "OK") {
		h.t.Fatalf("LOGIN status = %q", status)
	}
	return status
}

// appendMsg performs an APPEND with a synchronizing literal, returning the final
// response.
func (h *transcript) appendMsg(tag, folder, msg string) (untagged []string, status string) {
	h.t.Helper()
	h.send("%s APPEND %s {%d}", tag, folder, len(msg))
	if cont := h.readLine(); !strings.HasPrefix(cont, "+") {
		h.t.Fatalf("APPEND continuation = %q, want +...", cont)
	}
	h.writeLine(msg + "\r\n")
	return h.readResponse(tag)
}

func TestServer_UIDPlus_CapabilityAndResponseCodes(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", "Subject: Seed\r\n\r\nseed body\r\n", "Seed")
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))

	// LOGIN's tagged OK carries the post-auth capability list, which now includes
	// UIDPLUS because *mailbox implements UIDPlusMailbox.
	status := h.login("a1")
	if !strings.Contains(status, "UIDPLUS") {
		t.Errorf("LOGIN status missing UIDPLUS: %q", status)
	}

	// A fresh CAPABILITY lists UIDPLUS and the always-on extensions.
	unt, _ := h.command("a2", "CAPABILITY")
	caps := strings.Join(unt, "\n")
	for _, want := range []string{"UIDPLUS", "MOVE", "UNSELECT", "ENABLE", "IDLE", "QUOTA"} {
		if !strings.Contains(caps, want) {
			t.Errorf("CAPABILITY missing %q: %q", want, caps)
		}
	}

	// SELECT loads the seeded message as sequence 1 / UID 1.
	if _, st := h.command("a3", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT status = %q", st)
	}

	// APPEND reports APPENDUID with the UID the delivery assigned (100, the fake's
	// first nextID) against UIDVALIDITY 1.
	_, st := h.appendMsg("a4", "INBOX", "Subject: Appended\r\n\r\nhi\r\n")
	if !strings.Contains(st, "[APPENDUID 1 100]") {
		t.Errorf("APPEND status = %q, want [APPENDUID 1 100]", st)
	}

	// COPY reports COPYUID mapping source UID 1 to the new copy's UID (101).
	_, st = h.command("a5", "COPY 1 Archive")
	if !strings.Contains(st, "[COPYUID 1 1 101]") {
		t.Errorf("COPY status = %q, want [COPYUID 1 1 101]", st)
	}
	if got := folderOf(api, 101); got != "Archive" {
		t.Errorf("copy created in %q, want Archive", got)
	}

	// MOVE makes a new message in the destination, so it reports an untagged
	// COPYUID mapping source UID 1 to a fresh, higher destination UID (102) before
	// the EXPUNGE, and removes the source message.
	unt, st = h.command("a6", "MOVE 1 Trash")
	joined := strings.Join(unt, "\n")
	if !strings.Contains(joined, "[COPYUID 1 1 102]") {
		t.Errorf("MOVE untagged = %q, want an [COPYUID 1 1 102] resp-code", joined)
	}
	if !strings.Contains(joined, "1 EXPUNGE") {
		t.Errorf("MOVE untagged = %q, want a * 1 EXPUNGE", joined)
	}
	if !strings.Contains(st, "OK") {
		t.Errorf("MOVE status = %q", st)
	}
	if got := folderOf(api, 102); got != "Trash" {
		t.Errorf("moved copy created in %q, want Trash", got)
	}
	if api.exists(1) {
		t.Errorf("MOVE did not remove the source message 1")
	}
}

func TestServer_UIDExpunge_HonoursDeletedFlag(t *testing.T) {
	api := newFakeAPI()
	api.seed(5, "INBOX", "Subject: Del\r\n\r\nbye\r\n", "Del")
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	h.login("a1")

	if _, st := h.command("a2", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT status = %q", st)
	}
	// Flag sequence 1 (UID 5) \Deleted.
	if _, st := h.command("a3", `STORE 1 +FLAGS (\Deleted)`); !strings.Contains(st, "OK") {
		t.Fatalf("STORE status = %q", st)
	}
	// UID EXPUNGE 5 permanently removes it, emitting an untagged EXPUNGE, and
	// issues the backend Delete.
	unt, st := h.command("a4", "UID EXPUNGE 5")
	if !strings.Contains(strings.Join(unt, "\n"), "1 EXPUNGE") {
		t.Errorf("UID EXPUNGE untagged = %q, want a * 1 EXPUNGE", unt)
	}
	if !strings.Contains(st, "OK") {
		t.Errorf("UID EXPUNGE status = %q", st)
	}
	if !api.deleted(5) {
		t.Errorf("UID EXPUNGE did not delete message 5 via the API")
	}
}
