# Stage 4: Queue Management Testing & Refinement

**Status:** 🟡 READY FOR TESTING
**Priority:** HIGH
**Estimated Effort:** 3-4 days (focused on testing, UX improvements, and polish)

---

## Overview

The queue management UI is complete and functional, but requires thorough testing and refinement to ensure reliability and excellent user experience. This stage focuses on validating all queue operations against the actual API, improving the detail view, adding real-time updates, and implementing proper feedback mechanisms.

**Current State:**
- ✅ Queue list UI implemented (`/queue/index.tsx`)
- ✅ Queue detail view implemented (`/queue/$id.tsx`)
- ✅ Zustand store with actions implemented (`queueStore.ts`)
- ✅ Basic retry, delete, and filter operations functional
- ⚠️ No comprehensive testing with real API
- ⚠️ Missing bulk operations
- ⚠️ Missing raw message viewer
- ⚠️ Missing real-time status updates
- ⚠️ Missing proper toast notifications
- ⚠️ Error handling needs improvement

---

## Testing Plan

### 1. Queue List Operations Testing

**Test Cases:**

#### 1.1 Initial Load
- [ ] Queue list loads successfully with `GET /api/v1/admin/queue`
- [ ] Loading state displays correctly
- [ ] Empty state displays when no queue entries exist
- [ ] Error state displays on API failure
- [ ] Table displays all required columns: Recipient, Sender, Subject, Status, Attempts, Next Attempt, Actions

#### 1.2 Status Filtering
- [ ] "All" filter shows all queue entries
- [ ] "Pending" filter shows only pending entries (`status=pending`)
- [ ] "Deferred" filter shows only deferred entries (`status=deferred`)
- [ ] "Bounced" filter shows only bounced entries (`status=bounced`)
- [ ] Filter badge counts update correctly
- [ ] Active filter tab is visually highlighted
- [ ] API calls include correct status query parameter

#### 1.3 Single Entry Actions
- [ ] Click on recipient email navigates to detail view (`/queue/$id`)
- [ ] "Retry" button calls `POST /api/v1/admin/queue/{id}/retry`
- [ ] "Retry" button is disabled during loading
- [ ] Successful retry refreshes the queue list
- [ ] Failed retry shows error message
- [ ] "Delete" button shows confirmation inline
- [ ] "Delete" → "Confirm" calls `DELETE /api/v1/admin/queue/{id}`
- [ ] "Delete" → "Cancel" hides confirmation
- [ ] Successful delete removes entry from list
- [ ] Failed delete shows error message

#### 1.4 Status Badge Colors
- [ ] Pending status: Yellow background (#FEF3C7), dark text (#92400E)
- [ ] Deferred status: Orange background (#FFEDD5), dark text (#9A3412)
- [ ] Bounced status: Red background (#FEE2E2), dark text (#991B1B)
- [ ] Status text is uppercase

#### 1.5 Timestamp Formatting
- [ ] "Next Attempt" column formats timestamps correctly (e.g., "Feb 23, 02:30 PM")
- [ ] Shows "-" when next_attempt_at is null
- [ ] Timestamps are in user's local timezone

---

### 2. Queue Detail View Testing

**Test Cases:**

#### 2.1 Entry Loading
- [ ] Detail view loads with `GET /api/v1/admin/queue/{id}`
- [ ] Loading state displays correctly
- [ ] "Not found" state displays when entry doesn't exist
- [ ] "Back to Queue" link navigates correctly

#### 2.2 Information Display
- [ ] Header shows recipient email
- [ ] Status badge displays with correct color
- [ ] Delivery attempts count is shown
- [ ] Created timestamp is formatted (long format with seconds)
- [ ] Last updated timestamp is formatted
- [ ] Next attempt scheduled timestamp displays (when present)
- [ ] Subject displays correctly (or "(no subject)")
- [ ] Sender email displays correctly

#### 2.3 Error Display
- [ ] Error message section only appears when error_message is not null
- [ ] Error message displays in red-bordered container
- [ ] Error text is monospaced and readable
- [ ] Error container has correct styling (red border, light red background)

#### 2.4 Action Buttons
- [ ] "Retry Delivery" button calls `POST /api/v1/admin/queue/{id}/retry`
- [ ] Successful retry updates entry details
- [ ] "Delete Entry" shows confirmation buttons
- [ ] "Confirm Delete" calls `DELETE /api/v1/admin/queue/{id}`
- [ ] Successful delete navigates back to queue list
- [ ] "Cancel" hides delete confirmation
- [ ] Buttons are disabled during loading

---

### 3. API Integration Testing

**Endpoints to Verify:**

#### 3.1 List Queue Entries
```http
GET /api/v1/admin/queue
GET /api/v1/admin/queue?status=pending
GET /api/v1/admin/queue?status=deferred
GET /api/v1/admin/queue?status=bounced
```

**Expected Response:**
```json
{
  "entries": [
    {
      "id": "q1234",
      "recipient": "user@example.com",
      "sender": "sender@restmail.test",
      "subject": "Test Email",
      "status": "pending",
      "attempts": 1,
      "next_attempt_at": "2026-02-23T15:30:00Z",
      "error_message": null,
      "created_at": "2026-02-23T14:00:00Z",
      "updated_at": "2026-02-23T14:30:00Z"
    }
  ]
}
```

#### 3.2 Get Queue Entry Details
```http
GET /api/v1/admin/queue/{id}
```

**Expected Response:**
```json
{
  "id": "q1234",
  "recipient": "user@example.com",
  "sender": "sender@restmail.test",
  "subject": "Test Email",
  "status": "deferred",
  "attempts": 2,
  "next_attempt_at": "2026-02-23T16:00:00Z",
  "error_message": "SMTP 451: Temporary failure, please try again",
  "created_at": "2026-02-23T14:00:00Z",
  "updated_at": "2026-02-23T15:00:00Z",
  "raw_message": "From: sender@restmail.test\r\nTo: user@example.com\r\n..."
}
```

#### 3.3 Retry Queue Entry
```http
POST /api/v1/admin/queue/{id}/retry
```

**Expected Response:**
```json
{
  "message": "Queue entry retry initiated",
  "entry_id": "q1234",
  "status": "pending"
}
```

#### 3.4 Delete Queue Entry
```http
DELETE /api/v1/admin/queue/{id}
```

**Expected Response:**
```http
204 No Content
```

#### 3.5 Bounce Queue Entry (if API supports)
```http
POST /api/v1/admin/queue/{id}/bounce
```

**Expected Response:**
```json
{
  "message": "Bounce notification sent",
  "entry_id": "q1234"
}
```

---

## UX Improvements

### 4. Raw Message Viewer Component

**File:** `admin/src/components/queue/RawMessageViewer.tsx` (new file)

**Purpose:** Display the raw email message content with proper formatting

**Features:**
- Collapsible section (collapsed by default)
- Monospace font for raw message display
- Syntax highlighting for headers (optional)
- Copy to clipboard button
- Line numbers (optional)
- Scrollable container with fixed height

**Implementation:**

```typescript
import { useState } from 'react'

interface RawMessageViewerProps {
  rawMessage: string
}

export function RawMessageViewer({ rawMessage }: RawMessageViewerProps) {
  const [isExpanded, setIsExpanded] = useState(false)
  const [copied, setCopied] = useState(false)

  const handleCopy = async () => {
    await navigator.clipboard.writeText(rawMessage)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
      <button
        onClick={() => setIsExpanded(!isExpanded)}
        className="w-full flex items-center justify-between px-6 py-4 hover:bg-gray-50 transition-colors"
        style={{ backgroundColor: 'var(--bg-surface)' }}
      >
        <h2 className="text-lg font-semibold" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
          Raw Message
        </h2>
        <span className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
          {isExpanded ? '▼' : '▶'}
        </span>
      </button>

      {isExpanded && (
        <div className="p-6 border-t" style={{ borderColor: 'var(--gray-border)' }}>
          <div className="flex justify-end mb-2">
            <button
              onClick={handleCopy}
              className="px-3 py-1 text-xs font-medium border hover:bg-gray-50 transition-colors"
              style={{
                color: 'var(--black-soft)',
                borderColor: 'var(--gray-border)',
                fontFamily: 'Space Grotesk',
              }}
            >
              {copied ? 'Copied!' : 'Copy to Clipboard'}
            </button>
          </div>
          <div
            className="overflow-auto p-4 border text-xs font-mono"
            style={{
              maxHeight: '500px',
              backgroundColor: '#F9FAFB',
              borderColor: 'var(--gray-border)',
              color: 'var(--black-soft)',
            }}
          >
            <pre className="whitespace-pre-wrap">{rawMessage}</pre>
          </div>
        </div>
      )}
    </div>
  )
}
```

**Integration:**
- Add to queue detail view (`/queue/$id.tsx`)
- Fetch raw_message from API response
- Place below error message section

---

### 5. Delivery Log/History Component

**File:** `admin/src/components/queue/DeliveryHistory.tsx` (new file)

**Purpose:** Display chronological list of delivery attempts with errors

**Features:**
- Timeline-style display
- Attempt number, timestamp, result (success/failure)
- Error code and message for failed attempts
- SMTP conversation log (if available)
- Visual indicators for success/failure

**API Extension Needed:**
The detail endpoint may need to return delivery history:

```json
{
  "id": "q1234",
  "delivery_history": [
    {
      "attempt": 1,
      "timestamp": "2026-02-23T14:30:00Z",
      "result": "failed",
      "smtp_code": 451,
      "error_message": "Temporary failure, please try again",
      "smtp_log": "220 mx.example.com ESMTP\n250 EHLO\n..."
    },
    {
      "attempt": 2,
      "timestamp": "2026-02-23T15:00:00Z",
      "result": "failed",
      "smtp_code": 451,
      "error_message": "Temporary failure, please try again"
    }
  ]
}
```

**Implementation:**

```typescript
interface DeliveryAttempt {
  attempt: number
  timestamp: string
  result: 'success' | 'failed'
  smtp_code?: number
  error_message?: string
  smtp_log?: string
}

interface DeliveryHistoryProps {
  history: DeliveryAttempt[]
}

export function DeliveryHistory({ history }: DeliveryHistoryProps) {
  const [expandedAttempt, setExpandedAttempt] = useState<number | null>(null)

  const formatTimestamp = (timestamp: string) => {
    return new Date(timestamp).toLocaleString('en-US', {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
  }

  return (
    <div className="border p-6" style={{ borderColor: 'var(--gray-border)' }}>
      <h2 className="text-lg font-semibold mb-4" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
        Delivery History
      </h2>

      <div className="space-y-4">
        {history.map((attempt) => (
          <div
            key={attempt.attempt}
            className="border-l-2 pl-4"
            style={{
              borderColor: attempt.result === 'success' ? '#10B981' : '#EF4444',
            }}
          >
            <div className="flex items-center justify-between mb-1">
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                  Attempt #{attempt.attempt}
                </span>
                <span
                  className="px-2 py-0.5 text-xs font-medium border"
                  style={{
                    backgroundColor: attempt.result === 'success' ? '#D1FAE5' : '#FEE2E2',
                    color: attempt.result === 'success' ? '#065F46' : '#991B1B',
                    borderColor: attempt.result === 'success' ? '#A7F3D0' : '#FECACA',
                  }}
                >
                  {attempt.result.toUpperCase()}
                </span>
              </div>
              <span className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                {formatTimestamp(attempt.timestamp)}
              </span>
            </div>

            {attempt.smtp_code && (
              <div className="text-sm mb-1" style={{ color: 'var(--gray-secondary)' }}>
                SMTP {attempt.smtp_code}
              </div>
            )}

            {attempt.error_message && (
              <div className="text-sm mb-2" style={{ color: '#DC2626' }}>
                {attempt.error_message}
              </div>
            )}

            {attempt.smtp_log && (
              <>
                <button
                  onClick={() => setExpandedAttempt(
                    expandedAttempt === attempt.attempt ? null : attempt.attempt
                  )}
                  className="text-xs hover:underline"
                  style={{ color: 'var(--red-primary)' }}
                >
                  {expandedAttempt === attempt.attempt ? 'Hide SMTP Log' : 'View SMTP Log'}
                </button>

                {expandedAttempt === attempt.attempt && (
                  <div
                    className="mt-2 p-3 text-xs font-mono overflow-auto border"
                    style={{
                      maxHeight: '200px',
                      backgroundColor: '#F9FAFB',
                      borderColor: 'var(--gray-border)',
                    }}
                  >
                    <pre className="whitespace-pre-wrap">{attempt.smtp_log}</pre>
                  </div>
                )}
              </>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
```

---

### 6. Bulk Operations

**File:** `admin/src/routes/queue/index.tsx` (update)

**Features to Add:**
- Checkbox selection for multiple entries
- "Select All" checkbox in table header
- Bulk action bar appears when entries are selected
- Bulk retry button (retries all selected)
- Bulk delete button (deletes all selected)
- Bulk bounce button (if API supports)
- Confirmation modal for destructive bulk actions
- Progress indicator during bulk operations

**Store Update:**

**File:** `admin/src/lib/stores/queueStore.ts` (update)

Add bulk operation methods:

```typescript
interface QueueState {
  // ... existing state
  selectedIds: string[]

  // ... existing actions
  toggleSelection: (id: string) => void
  selectAll: (ids: string[]) => void
  clearSelection: () => void
  retryBulk: (ids: string[], accessToken: string) => Promise<void>
  deleteBulk: (ids: string[], accessToken: string) => Promise<void>
}

// Implementation
toggleSelection: (id: string) => {
  set((state) => ({
    selectedIds: state.selectedIds.includes(id)
      ? state.selectedIds.filter((i) => i !== id)
      : [...state.selectedIds, id],
  }))
},

selectAll: (ids: string[]) => {
  set({ selectedIds: ids })
},

clearSelection: () => {
  set({ selectedIds: [] })
},

retryBulk: async (ids: string[], accessToken: string) => {
  set({ isLoading: true, error: null })

  try {
    // Retry each entry sequentially
    for (const id of ids) {
      await apiV1.request(`/admin/queue/${id}/retry`, { method: 'POST' }, accessToken)
    }

    // Refresh queue and clear selection
    await get().fetchQueue(accessToken, get().filter)
    set({ selectedIds: [], isLoading: false })
  } catch (error) {
    set({
      error: error instanceof Error ? error.message : 'Bulk retry failed',
      isLoading: false,
    })
    throw error
  }
},

deleteBulk: async (ids: string[], accessToken: string) => {
  set({ isLoading: true, error: null })

  try {
    // Delete each entry sequentially
    for (const id of ids) {
      await apiV1.request(`/admin/queue/${id}`, { method: 'DELETE' }, accessToken)
    }

    // Refresh queue and clear selection
    await get().fetchQueue(accessToken, get().filter)
    set({ selectedIds: [], isLoading: false })
  } catch (error) {
    set({
      error: error instanceof Error ? error.message : 'Bulk delete failed',
      isLoading: false,
    })
    throw error
  }
},
```

**UI Update:**

Add checkbox column to table and bulk action bar:

```typescript
// In QueueListPage component
const { selectedIds, toggleSelection, selectAll, clearSelection, retryBulk, deleteBulk } = useQueueStore()
const [showBulkConfirm, setShowBulkConfirm] = useState<'delete' | null>(null)

const handleSelectAll = () => {
  if (selectedIds.length === filteredEntries.length) {
    clearSelection()
  } else {
    selectAll(filteredEntries.map(e => e.id))
  }
}

const handleBulkRetry = async () => {
  if (!accessToken) return
  try {
    await retryBulk(selectedIds, accessToken)
  } catch (err) {
    console.error('Bulk retry failed:', err)
  }
}

const handleBulkDelete = async () => {
  if (!accessToken) return
  try {
    await deleteBulk(selectedIds, accessToken)
    setShowBulkConfirm(null)
  } catch (err) {
    console.error('Bulk delete failed:', err)
  }
}

// Add to table header:
<th className="px-6 py-3 text-xs">
  <input
    type="checkbox"
    checked={selectedIds.length === filteredEntries.length && filteredEntries.length > 0}
    onChange={handleSelectAll}
  />
</th>

// Add to table rows:
<td className="px-6 py-4">
  <input
    type="checkbox"
    checked={selectedIds.includes(entry.id)}
    onChange={() => toggleSelection(entry.id)}
  />
</td>

// Add bulk action bar (conditional):
{selectedIds.length > 0 && (
  <div
    className="fixed bottom-8 left-1/2 transform -translate-x-1/2 px-6 py-4 border shadow-lg flex items-center gap-4"
    style={{
      backgroundColor: 'var(--bg-surface)',
      borderColor: 'var(--gray-border)',
      zIndex: 50,
    }}
  >
    <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
      {selectedIds.length} selected
    </span>
    <button
      onClick={handleBulkRetry}
      disabled={isLoading}
      className="px-4 py-2 text-sm font-medium border hover:bg-gray-50"
      style={{ borderColor: 'var(--gray-border)', fontFamily: 'Space Grotesk' }}
    >
      Retry All
    </button>
    <button
      onClick={() => setShowBulkConfirm('delete')}
      disabled={isLoading}
      className="px-4 py-2 text-sm font-medium border hover:bg-red-50"
      style={{ color: '#DC2626', borderColor: 'var(--gray-border)', fontFamily: 'Space Grotesk' }}
    >
      Delete All
    </button>
    <button
      onClick={clearSelection}
      className="px-4 py-2 text-sm font-medium"
      style={{ color: 'var(--gray-secondary)', fontFamily: 'Space Grotesk' }}
    >
      Cancel
    </button>
  </div>
)}
```

---

### 7. Toast Notifications

**File:** `admin/src/components/ui/Toast.tsx` (new file)

**Purpose:** Provide user feedback for operations (success, error, info)

**Features:**
- Auto-dismiss after 3-5 seconds
- Manual dismiss button
- Stacked multiple toasts
- Success (green), Error (red), Info (blue) variants
- Slide-in animation
- Position: top-right corner

**Implementation using Zustand:**

**File:** `admin/src/lib/stores/toastStore.ts` (new file)

```typescript
import { create } from 'zustand'

export type ToastType = 'success' | 'error' | 'info'

interface Toast {
  id: string
  type: ToastType
  message: string
}

interface ToastState {
  toasts: Toast[]
  addToast: (type: ToastType, message: string) => void
  removeToast: (id: string) => void
}

export const useToastStore = create<ToastState>((set) => ({
  toasts: [],

  addToast: (type: ToastType, message: string) => {
    const id = Math.random().toString(36).substring(7)
    set((state) => ({
      toasts: [...state.toasts, { id, type, message }],
    }))

    // Auto-dismiss after 4 seconds
    setTimeout(() => {
      set((state) => ({
        toasts: state.toasts.filter((t) => t.id !== id),
      }))
    }, 4000)
  },

  removeToast: (id: string) => {
    set((state) => ({
      toasts: state.toasts.filter((t) => t.id !== id),
    }))
  },
}))
```

**File:** `admin/src/components/ui/Toast.tsx`

```typescript
import { useToastStore } from '../../lib/stores/toastStore'

export function ToastContainer() {
  const { toasts, removeToast } = useToastStore()

  const getToastStyle = (type: string) => {
    switch (type) {
      case 'success':
        return { bg: '#D1FAE5', text: '#065F46', border: '#A7F3D0' }
      case 'error':
        return { bg: '#FEE2E2', text: '#991B1B', border: '#FECACA' }
      case 'info':
        return { bg: '#DBEAFE', text: '#1E40AF', border: '#BFDBFE' }
      default:
        return { bg: '#F3F4F6', text: '#374151', border: '#D1D5DB' }
    }
  }

  return (
    <div className="fixed top-4 right-4 z-50 space-y-2" style={{ maxWidth: '400px' }}>
      {toasts.map((toast) => {
        const style = getToastStyle(toast.type)
        return (
          <div
            key={toast.id}
            className="p-4 border shadow-lg flex items-start gap-3 animate-slide-in"
            style={{
              backgroundColor: style.bg,
              borderColor: style.border,
              color: style.text,
            }}
          >
            <span className="flex-1 text-sm font-medium">{toast.message}</span>
            <button
              onClick={() => removeToast(toast.id)}
              className="text-lg leading-none hover:opacity-70"
              style={{ color: style.text }}
            >
              ×
            </button>
          </div>
        )
      })}
    </div>
  )
}
```

**CSS Animation (add to global styles):**

```css
@keyframes slide-in {
  from {
    transform: translateX(100%);
    opacity: 0;
  }
  to {
    transform: translateX(0);
    opacity: 1;
  }
}

.animate-slide-in {
  animation: slide-in 0.3s ease-out;
}
```

**Integration:**
- Add `<ToastContainer />` to `AppShell` component
- Use in queue operations:

```typescript
const { addToast } = useToastStore()

// After successful retry:
addToast('success', 'Queue entry retry initiated')

// After successful delete:
addToast('success', 'Queue entry deleted')

// After error:
addToast('error', 'Failed to retry entry: ' + error.message)
```

---

### 8. Real-Time Queue Status Updates

**Purpose:** Automatically refresh queue list to show latest status without manual refresh

**Implementation Options:**

#### Option A: Polling (Simple)

Add auto-refresh every 10 seconds:

```typescript
useEffect(() => {
  if (!accessToken) return

  // Initial fetch
  fetchQueue(accessToken)

  // Polling interval
  const interval = setInterval(() => {
    fetchQueue(accessToken)
  }, 10000) // 10 seconds

  return () => clearInterval(interval)
}, [accessToken, filter])
```

#### Option B: WebSocket (Advanced)

If backend supports WebSocket updates:

```typescript
useEffect(() => {
  if (!accessToken) return

  const ws = new WebSocket('ws://localhost:3000/api/v1/admin/queue/events')

  ws.onopen = () => {
    ws.send(JSON.stringify({ type: 'subscribe', token: accessToken }))
  }

  ws.onmessage = (event) => {
    const update = JSON.parse(event.data)

    if (update.type === 'queue_update') {
      // Update specific entry in store
      set((state) => ({
        entries: state.entries.map((e) =>
          e.id === update.entry.id ? update.entry : e
        ),
      }))
    }
  }

  return () => ws.close()
}, [accessToken])
```

**Recommendation:** Start with polling (Option A), add WebSocket later if needed.

---

### 9. Improved Error Display

**Current Issue:**
- Errors only show in detail view
- No distinction between different error types
- No error codes displayed prominently

**Improvements:**

#### 9.1 Error Categories

Categorize errors by SMTP code:

```typescript
const getErrorCategory = (errorMessage: string | null): string => {
  if (!errorMessage) return 'unknown'

  // Extract SMTP code
  const match = errorMessage.match(/^(\d{3})/)
  if (!match) return 'unknown'

  const code = parseInt(match[1])

  if (code >= 200 && code < 300) return 'success'
  if (code >= 400 && code < 500) return 'permanent' // Bounce
  if (code >= 500 && code < 600) return 'temporary' // Retry
  return 'unknown'
}
```

#### 9.2 Error Icon Indicators

Add visual icons to error messages:

```typescript
const getErrorIcon = (category: string): string => {
  switch (category) {
    case 'permanent':
      return '⚠️' // Permanent failure
    case 'temporary':
      return '🔄' // Temporary failure, will retry
    case 'unknown':
      return '❓' // Unknown error
    default:
      return ''
  }
}
```

#### 9.3 Enhanced Error Display in Detail View

```typescript
{currentEntry.error_message && (
  <div className="border p-6" style={{ borderColor: '#EF4444', backgroundColor: '#FEF2F2' }}>
    <div className="flex items-start gap-3 mb-4">
      <span className="text-2xl">
        {getErrorIcon(getErrorCategory(currentEntry.error_message))}
      </span>
      <div>
        <h2 className="text-lg font-semibold" style={{ fontFamily: 'Space Grotesk', color: '#DC2626' }}>
          Delivery Failed
        </h2>
        <p className="text-sm mt-1" style={{ color: '#991B1B' }}>
          {getErrorCategory(currentEntry.error_message) === 'permanent'
            ? 'Permanent failure - will not retry automatically'
            : 'Temporary failure - will retry automatically'}
        </p>
      </div>
    </div>

    <div className="text-sm font-mono p-4 border" style={{
      color: '#DC2626',
      borderColor: '#FECACA',
      backgroundColor: '#FFFFFF',
    }}>
      {currentEntry.error_message}
    </div>
  </div>
)}
```

---

### 10. Bounce Operation (Optional)

If the API supports sending bounce notifications:

**API Endpoint:**
```http
POST /api/v1/admin/queue/{id}/bounce
```

**Store Update:**

```typescript
bounceEntry: async (id: string, accessToken: string) => {
  set({ isLoading: true, error: null })

  try {
    const response = await apiV1.request(`/admin/queue/${id}/bounce`, { method: 'POST' }, accessToken)

    if (!response.ok) {
      const error = await response.json()
      throw new Error(error.error || 'Failed to bounce queue entry')
    }

    // Refresh the queue list
    await get().fetchQueue(accessToken, get().filter)

    set({ isLoading: false, error: null })
  } catch (error) {
    set({
      error: error instanceof Error ? error.message : 'Failed to bounce queue entry',
      isLoading: false,
    })
    throw error
  }
},
```

**UI Update:**

Add "Bounce" button in both list and detail views:

```typescript
<button
  onClick={() => handleBounce(entry.id)}
  disabled={isLoading}
  className="px-3 py-1 text-xs font-medium border hover:bg-orange-50 transition-colors"
  style={{
    color: '#EA580C',
    borderColor: 'var(--gray-border)',
    fontFamily: 'Space Grotesk',
  }}
>
  Bounce
</button>
```

---

## Testing Checklist

### Integration Testing:

- [ ] Test with actual backend API
- [ ] Verify all API responses match expected format
- [ ] Test error handling with various API failures
- [ ] Test with empty queue
- [ ] Test with large queue (100+ entries)
- [ ] Test filtering with mixed status entries
- [ ] Test rapid retry/delete operations
- [ ] Test bulk operations with 10+ selected entries

### Performance Testing:

- [ ] Queue list loads in < 2 seconds
- [ ] Filter changes are instant
- [ ] Detail view loads in < 1 second
- [ ] No UI lag when selecting multiple entries
- [ ] Toasts don't block interactions

### Accessibility Testing:

- [ ] All buttons have proper labels
- [ ] Keyboard navigation works correctly
- [ ] Tab order is logical
- [ ] Status colors have sufficient contrast
- [ ] Error messages are announced to screen readers

### Browser Testing:

- [ ] Chrome (latest)
- [ ] Firefox (latest)
- [ ] Safari (latest)
- [ ] Edge (latest)

---

## Success Criteria

1. ✅ All queue operations tested with real API
2. ✅ Bulk operations functional
3. ✅ Raw message viewer implemented
4. ✅ Delivery history component implemented (if API supports)
5. ✅ Toast notifications working for all operations
6. ✅ Real-time updates via polling
7. ✅ Enhanced error display with categories
8. ✅ All edge cases handled gracefully
9. ✅ Zero console errors or warnings
10. ✅ Professional UX with smooth interactions

---

## Next Steps After Completion

1. Document queue management in user guide
2. Add queue metrics to dashboard
3. Implement email search/filtering by sender domain
4. Add export functionality (CSV/JSON)
5. Move to Stage 5: Pipeline & Filter Management

---

## Notes

- Backend API endpoints already exist and are functional
- Focus is on frontend testing, refinement, and UX improvements
- No backend changes required unless delivery history is desired
- Toast notifications will be reusable in other admin sections
- Bulk operations pattern can be replicated for domains/mailboxes

---

**Plan Created:** 2026-02-23
**Status:** Ready for implementation
**Dependencies:** None (backend complete)
**Estimated Completion:** 3-4 days with thorough testing
