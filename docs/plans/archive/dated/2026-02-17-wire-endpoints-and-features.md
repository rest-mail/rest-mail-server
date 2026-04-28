# Wire Dead-Code Handlers & Add Missing Endpoints

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Wire up existing but unreachable handlers (attachments, contacts) into the router, add draft CRUD endpoints, add thread retrieval endpoint, and extend SSE events.

**Architecture:** All handlers already exist as dead code—tasks mostly involve adding route registrations in `routes.go` and writing thin new handler methods where needed. Draft endpoints reuse the existing `Message` model (with `is_draft` flag). Thread endpoint is a query grouped by `thread_id`.

**Tech Stack:** Go, chi router, GORM/PostgreSQL, existing `respond` package.

---

### Task 1: Wire attachment routes into the router

**Files:**
- Modify: `internal/api/routes.go`

**Step 1: Write the route registrations**

In `internal/api/routes.go`, add `attachmentH` initialization and routes inside the authenticated group:

```go
// In NewRouter, after other handler initializations:
attachmentH := handlers.NewAttachmentHandler(db)

// Inside the authenticated r.Group:
r.Get("/api/v1/attachments/{id}", attachmentH.GetAttachment)
r.Get("/api/v1/messages/{id}/attachments", attachmentH.ListAttachments)
```

**Step 2: Verify compilation**

Run: `go build ./...`
Expected: clean build, no errors.

**Step 3: Commit**

```bash
git add internal/api/routes.go
git commit -m "Wire attachment endpoints into API router"
```

---

### Task 2: Wire contact routes into the router

**Files:**
- Modify: `internal/api/routes.go`

**Step 1: Write the route registrations**

In `internal/api/routes.go`, add `contactH` initialization and routes inside the authenticated group:

```go
// In NewRouter, after other handler initializations:
contactH := handlers.NewContactHandler(db)

// Inside the authenticated r.Group:
r.Get("/api/v1/accounts/{id}/contacts", contactH.ListContacts)
r.Post("/api/v1/accounts/{id}/contacts", contactH.CreateContact)
r.Patch("/api/v1/accounts/{id}/contacts/{cid}", contactH.UpdateContact)
r.Delete("/api/v1/accounts/{id}/contacts/{cid}", contactH.DeleteContact)
r.Post("/api/v1/accounts/{id}/contacts/block", contactH.BlockSender)
r.Post("/api/v1/accounts/{id}/contacts/import", contactH.ImportContacts)
```

**Step 2: Verify compilation**

Run: `go build ./...`
Expected: clean build, no errors.

**Step 3: Commit**

```bash
git add internal/api/routes.go
git commit -m "Wire contact CRUD and import endpoints into API router"
```

---

### Task 3: Add draft endpoints (save, update, send-from-draft)

**Files:**
- Modify: `internal/api/handlers/messages.go` — add `SaveDraft`, `UpdateDraft`, `SendDraft` methods
- Modify: `internal/api/routes.go` — register draft routes

**Step 1: Implement SaveDraft handler**

Add to `internal/api/handlers/messages.go`:

```go
// SaveDraft creates a new draft message.
// POST /api/v1/messages/draft
func (h *MessageHandler) SaveDraft(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req struct {
		From     string   `json:"from"`
		To       []string `json:"to"`
		Cc       []string `json:"cc"`
		Subject  string   `json:"subject"`
		BodyText string   `json:"body_text"`
		BodyHTML string   `json:"body_html"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Resolve sender mailbox (if from is set, validate; otherwise use primary)
	var mailboxID uint
	if req.From != "" {
		mb, err := h.resolveSenderMailbox(req.From, claims.WebmailAccountID)
		if err != nil {
			respond.Error(w, http.StatusForbidden, "forbidden", "You are not authorized to send from this address")
			return
		}
		mailboxID = mb.ID
	} else {
		var account models.WebmailAccount
		if err := h.db.First(&account, claims.WebmailAccountID).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to resolve account")
			return
		}
		mailboxID = account.PrimaryMailboxID
	}

	toJSON, _ := json.Marshal(req.To)
	if req.To == nil {
		toJSON = []byte("[]")
	}
	ccJSON, _ := json.Marshal(req.Cc)
	if req.Cc == nil {
		ccJSON = []byte("[]")
	}

	draft := models.Message{
		MailboxID:    mailboxID,
		Folder:       "Drafts",
		Sender:       req.From,
		RecipientsTo: models.JSONB(toJSON),
		RecipientsCc: models.JSONB(ccJSON),
		Subject:      req.Subject,
		BodyText:     req.BodyText,
		BodyHTML:     req.BodyHTML,
		IsDraft:      true,
		IsRead:       true,
		SizeBytes:    len(req.Subject) + len(req.BodyText) + len(req.BodyHTML),
		ReceivedAt:   time.Now(),
	}

	if err := h.db.Create(&draft).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save draft")
		return
	}

	respond.Data(w, http.StatusCreated, draft)
}
```

**Step 2: Implement UpdateDraft handler**

```go
// UpdateDraft updates an existing draft message.
// PUT /api/v1/messages/draft/{id}
func (h *MessageHandler) UpdateDraft(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var draft models.Message
	if err := h.db.Where("id = ? AND is_draft = ?", id, true).First(&draft).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Draft not found")
		return
	}

	var req struct {
		From     *string  `json:"from"`
		To       []string `json:"to"`
		Cc       []string `json:"cc"`
		Subject  *string  `json:"subject"`
		BodyText *string  `json:"body_text"`
		BodyHTML *string  `json:"body_html"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.From != nil {
		updates["sender"] = *req.From
	}
	if req.To != nil {
		toJSON, _ := json.Marshal(req.To)
		updates["recipients_to"] = models.JSONB(toJSON)
	}
	if req.Cc != nil {
		ccJSON, _ := json.Marshal(req.Cc)
		updates["recipients_cc"] = models.JSONB(ccJSON)
	}
	if req.Subject != nil {
		updates["subject"] = *req.Subject
	}
	if req.BodyText != nil {
		updates["body_text"] = *req.BodyText
	}
	if req.BodyHTML != nil {
		updates["body_html"] = *req.BodyHTML
	}

	if len(updates) > 0 {
		h.db.Model(&draft).Updates(updates)
	}

	h.db.First(&draft, id)
	respond.Data(w, http.StatusOK, draft)
}
```

**Step 3: Implement SendDraft handler**

```go
// SendDraft converts a draft into a sent message by deleting the draft and
// delegating to SendMessage logic.
// POST /api/v1/messages/draft/{id}/send
func (h *MessageHandler) SendDraft(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var draft models.Message
	if err := h.db.Where("id = ? AND is_draft = ?", id, true).First(&draft).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Draft not found")
		return
	}

	// Unmarshal recipients from the draft
	var toList []string
	json.Unmarshal(draft.RecipientsTo, &toList)
	if len(toList) == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Draft has no recipients")
		return
	}

	// Build a SendMessage-compatible request body and inject it
	sendBody := struct {
		From     string   `json:"from"`
		To       []string `json:"to"`
		Cc       []string `json:"cc"`
		Subject  string   `json:"subject"`
		BodyText string   `json:"body_text"`
		BodyHTML string   `json:"body_html"`
	}{
		From:     draft.Sender,
		To:       toList,
		Subject:  draft.Subject,
		BodyText: draft.BodyText,
		BodyHTML: draft.BodyHTML,
	}
	var ccList []string
	json.Unmarshal(draft.RecipientsCc, &ccList)
	sendBody.Cc = ccList

	bodyBytes, _ := json.Marshal(sendBody)

	// Delete the draft before sending (so it doesn't remain)
	h.db.Delete(&draft)

	// Create a new request with the draft's content and delegate to SendMessage
	newReq, _ := http.NewRequestWithContext(r.Context(), "POST", "/api/v1/messages/send", strings.NewReader(string(bodyBytes)))
	newReq.Header.Set("Content-Type", "application/json")
	// Copy auth context
	for key, vals := range r.Header {
		for _, v := range vals {
			newReq.Header.Add(key, v)
		}
	}
	// Preserve chi context and middleware claims
	newReq = newReq.WithContext(r.Context())

	h.SendMessage(w, newReq)
}
```

**Step 4: Add `resolveSenderMailbox` helper**

This helper is extracted from the duplicated sender-verification logic in `SendMessage`:

```go
// resolveSenderMailbox checks that the sender address belongs to the webmail account.
func (h *MessageHandler) resolveSenderMailbox(from string, webmailAccountID uint) (*models.Mailbox, error) {
	// Check primary
	var account models.WebmailAccount
	if err := h.db.Preload("PrimaryMailbox").First(&account, webmailAccountID).Error; err == nil {
		if account.PrimaryMailbox.Address == from {
			return &account.PrimaryMailbox, nil
		}
	}
	// Check linked
	var linked models.LinkedAccount
	if err := h.db.Joins("Mailbox").Where("linked_accounts.webmail_account_id = ? AND \"Mailbox\".address = ?", webmailAccountID, from).First(&linked).Error; err == nil {
		return &linked.Mailbox, nil
	}
	return nil, fmt.Errorf("sender not authorized")
}
```

**Step 5: Register draft routes**

In `internal/api/routes.go`, inside the authenticated group:

```go
// Drafts
r.Post("/api/v1/messages/draft", messageH.SaveDraft)
r.Put("/api/v1/messages/draft/{id}", messageH.UpdateDraft)
r.Post("/api/v1/messages/draft/{id}/send", messageH.SendDraft)
```

**Step 6: Verify compilation**

Run: `go build ./...`
Expected: clean build.

**Step 7: Commit**

```bash
git add internal/api/handlers/messages.go internal/api/routes.go
git commit -m "Add draft save, update, and send-from-draft endpoints"
```

---

### Task 4: Add thread retrieval endpoint

**Files:**
- Modify: `internal/api/handlers/messages.go` — add `GetThread` method
- Modify: `internal/api/routes.go` — register thread route

**Step 1: Implement GetThread handler**

Add to `internal/api/handlers/messages.go`:

```go
// GetThread returns all messages sharing the same thread_id.
// GET /api/v1/accounts/{id}/threads/{threadID}
func (h *MessageHandler) GetThread(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	threadID := chi.URLParam(r, "threadID")
	if threadID == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Thread ID required")
		return
	}

	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var messages []models.Message
	if err := h.db.Where("mailbox_id = ? AND thread_id = ? AND is_deleted = ?", mailboxID, threadID, false).
		Order("received_at ASC").
		Find(&messages).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve thread")
		return
	}

	respond.List(w, messages, nil)
}
```

**Step 2: Register the route**

In `internal/api/routes.go`, inside the authenticated group:

```go
// Threads
r.Get("/api/v1/accounts/{id}/threads/{threadID}", messageH.GetThread)
```

**Step 3: Verify compilation**

Run: `go build ./...`
Expected: clean build.

**Step 4: Commit**

```bash
git add internal/api/handlers/messages.go internal/api/routes.go
git commit -m "Add thread retrieval endpoint to group messages by thread_id"
```

---

### Task 5: Extend SSE events for flag changes and folder moves

**Files:**
- Modify: `internal/api/handlers/messages.go` — add SSE events to `UpdateMessage` and `DeleteMessage`

**Step 1: Add SSE event to UpdateMessage**

In the `UpdateMessage` handler, after the updates are applied and before the response, publish an event:

```go
// After h.db.First(&msg, id) reload and before respond.Data:
if h.broker != nil {
	h.broker.Publish(msg.MailboxID, SSEEvent{
		Type: "message_updated",
		Data: map[string]interface{}{
			"message_id": msg.ID,
			"folder":     msg.Folder,
			"is_read":    msg.IsRead,
			"is_flagged": msg.IsFlagged,
			"is_starred": msg.IsStarred,
		},
	})
}
```

**Step 2: Add SSE event to DeleteMessage**

In the `DeleteMessage` handler, after the delete/trash operation and before `w.WriteHeader`:

```go
if h.broker != nil {
	h.broker.Publish(msg.MailboxID, SSEEvent{
		Type: "message_deleted",
		Data: map[string]interface{}{
			"message_id": msg.ID,
		},
	})
}
```

**Step 3: Verify compilation**

Run: `go build ./...`
Expected: clean build.

**Step 4: Commit**

```bash
git add internal/api/handlers/messages.go
git commit -m "Extend SSE events for message updates and deletions"
```

---

## Summary of Changes

| Task | What | New Routes |
|------|------|------------|
| 1 | Wire attachments | `GET /api/v1/attachments/{id}`, `GET /api/v1/messages/{id}/attachments` |
| 2 | Wire contacts | 6 contact endpoints under `/api/v1/accounts/{id}/contacts` |
| 3 | Draft CRUD | `POST /draft`, `PUT /draft/{id}`, `POST /draft/{id}/send` |
| 4 | Thread view | `GET /api/v1/accounts/{id}/threads/{threadID}` |
| 5 | SSE events | Events for `message_updated` and `message_deleted` |
