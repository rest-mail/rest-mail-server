package imap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	imapsrv "github.com/rest-mail/go-imap"

	"github.com/restmail/restmail/internal/gateway/apiclient"
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
	id         uint
	folder     string
	raw        string
	subject    string
	sender     string
	senderNm   string
	isRead     bool
	isFlagged  bool
	isStarred  bool
	isDraft    bool
	receivedAt time.Time
}

type patchRecord struct {
	id      uint
	updates map[string]interface{}
}

type fakeAPI struct {
	mu       sync.Mutex
	nextID   uint
	msgs     map[uint]*fakeMsg
	patches  []patchRecord
	deletes  []uint
	delivers []apiclient.DeliverRequest
	// deliverStatus, when non-zero, is the HTTP status the deliver endpoint
	// returns instead of a normal 200; deliverBody is the JSON body to send with
	// it. Together they let a test simulate a quarantined/discarded delivery
	// (200 with no data.id) or a hard delivery failure.
	deliverStatus int
	deliverBody   interface{}
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{nextID: 100, msgs: map[uint]*fakeMsg{}}
}

func (f *fakeAPI) seed(id uint, folder, raw, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs[id] = &fakeMsg{
		id: id, folder: folder, raw: raw, subject: subject,
		sender: "sender@example.com", senderNm: "Sender",
	}
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
	mux.HandleFunc("POST /api/v1/auth/login", f.login)
	mux.HandleFunc("GET /api/v1/accounts/{acct}/folders/{folder}/messages", f.listMessages)
	mux.HandleFunc("POST /api/v1/messages/deliver", f.deliver)
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
	var lr apiclient.LoginResponse
	lr.Data.AccessToken = "tok"
	lr.Data.ExpiresIn = 3600
	lr.Data.User.ID = 7
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
		out = append(out, apiclient.MessageSummary{
			ID:         m.id,
			Folder:     m.folder,
			Subject:    m.subject,
			Sender:     m.sender,
			SenderName: m.senderNm,
			RawSize:    len(m.raw),
			ReceivedAt: fakeReceivedAt,
		})
	}
	f.mu.Unlock()
	// The client reverses newest-first into oldest-first, so return descending ID.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	writeJSON(w, apiclient.MessageListResponse{Data: out})
}

func (f *fakeAPI) deliver(w http.ResponseWriter, r *http.Request) {
	var req apiclient.DeliverRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.delivers = append(f.delivers, req)
	// A test may force the delivery outcome: a 200 with no data.id models a
	// pipeline quarantine/discard; a >=400 status models a hard failure.
	if f.deliverStatus != 0 {
		status, body := f.deliverStatus, f.deliverBody
		f.mu.Unlock()
		w.WriteHeader(status)
		if body != nil {
			writeJSON(w, body)
		}
		return
	}
	id := f.nextID
	f.nextID++
	// The message is created directly in its destination folder (empty = INBOX),
	// carrying whatever flags / internal date the delivery specified.
	folder := req.Folder
	if folder == "" {
		folder = "INBOX"
	}
	m := &fakeMsg{
		id: id, folder: folder, raw: string(req.RawMessage),
		subject: req.Subject, sender: req.Sender, senderNm: req.SenderName,
	}
	if req.IsRead != nil {
		m.isRead = *req.IsRead
	}
	if req.IsFlagged != nil {
		m.isFlagged = *req.IsFlagged
	}
	if req.IsStarred != nil {
		m.isStarred = *req.IsStarred
	}
	if req.IsDraft != nil {
		m.isDraft = *req.IsDraft
	}
	if req.ReceivedAt != nil {
		m.receivedAt = *req.ReceivedAt
	}
	f.msgs[id] = m
	f.mu.Unlock()
	var dr apiclient.DeliverResponse
	dr.Data.ID = id
	dr.Data.Subject = req.Subject
	writeJSON(w, dr)
}

// lastDeliver returns the most recent DeliverRequest the gateway sent, for
// asserting COPY/APPEND carried the folder, flags and internal date.
func (f *fakeAPI) lastDeliver(t *testing.T) apiclient.DeliverRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.delivers) == 0 {
		t.Fatal("no delivery was made")
	}
	return f.delivers[len(f.delivers)-1]
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
	f.mu.Unlock()
	if m == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	received := m.receivedAt
	if received.IsZero() {
		received = fakeReceivedAt
	}
	var dr apiclient.MessageDetailResponse
	dr.Data.MessageSummary = apiclient.MessageSummary{
		ID:         m.id,
		Folder:     m.folder,
		Subject:    m.subject,
		Sender:     m.sender,
		SenderName: m.senderNm,
		RawSize:    len(m.raw),
		IsRead:     m.isRead,
		IsFlagged:  m.isFlagged,
		IsStarred:  m.isStarred,
		IsDraft:    m.isDraft,
		ReceivedAt: received,
	}
	writeJSON(w, dr)
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
	f.patches = append(f.patches, patchRecord{id: id, updates: updates})
	if m := f.msgs[id]; m != nil {
		if folder, ok := updates["folder"].(string); ok {
			m.folder = folder
		}
	}
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (f *fakeAPI) delete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	f.mu.Lock()
	f.deletes = append(f.deletes, id)
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

	uid, err := m.AppendUID("INBOX", imapsrv.FlagUpdate{}, []byte("Subject: Hi\r\n\r\nbody\r\n"))
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

	if err := m.Append("INBOX", imapsrv.FlagUpdate{}, []byte("Subject: A\r\n\r\na\r\n")); err != nil {
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

// ── End-to-end transcript over a real imapsrv.Session ──────────────────────

// transcript drives a real imapsrv.Session over net.Pipe against the rest-mail
// Backend, so the wire-level UIDPLUS / MOVE behaviour (capabilities, APPENDUID /
// COPYUID response codes, UID EXPUNGE) is exercised through the actual server.
type transcript struct {
	t    *testing.T
	conn net.Conn
	cr   *bufio.Reader
	cw   *bufio.Writer
	done chan struct{}
}

func newTranscript(t *testing.T, backend imapsrv.Backend) *transcript {
	t.Helper()
	client, server := net.Pipe()
	sess := imapsrv.NewSession(server, backend, "imap.test", nil, imapsrv.NopLimiter{})

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

	// MOVE re-delivers the message into the destination, so it reports an untagged
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
	if !api.deleted(1) {
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
