# Stage 4: Queue Management Testing Guide

**Quick Reference for Manual Testing**

---

## Prerequisites

1. Backend API running on http://localhost:8025
2. Admin interface running on http://localhost:3000
3. Logged in as admin user
4. Test data: At least 3-5 queue entries in different states

---

## Test Scenarios

### 1. Bulk Operations Test

**Steps:**
1. Navigate to Queue Management page (`/queue`)
2. Click individual checkboxes to select 2-3 entries
3. Verify bulk action bar appears at bottom center
4. Verify selection count displays correctly
5. Click "Retry All" button
6. Verify success toast appears
7. Verify entries are refreshed
8. Select more entries
9. Click "Delete All" button
10. Verify confirmation dialog appears
11. Click "Confirm Delete"
12. Verify success toast appears
13. Verify entries are removed from list

**Expected Results:**
- ✅ Checkboxes work correctly
- ✅ Bulk action bar shows/hides properly
- ✅ Selection count updates
- ✅ Retry all succeeds
- ✅ Delete confirmation works
- ✅ Success toasts display
- ✅ List refreshes after operations

---

### 2. Select All Test

**Steps:**
1. Navigate to Queue Management page
2. Click "Select All" checkbox in table header
3. Verify all visible entries are selected
4. Verify selection count matches total entries
5. Click "Select All" again
6. Verify all selections are cleared

**Expected Results:**
- ✅ Select all works
- ✅ Deselect all works
- ✅ Count updates correctly

---

### 3. Raw Message Viewer Test

**Steps:**
1. Navigate to Queue Management page
2. Click on any queue entry to view details
3. Scroll to bottom of page
4. If raw_message exists, verify "Raw Message" section appears
5. Verify section is collapsed by default
6. Click to expand section
7. Verify raw message displays in monospace
8. Click "Copy to Clipboard" button
9. Verify button text changes to "Copied!"
10. Paste clipboard content to verify copy worked
11. Click collapse button to hide section

**Expected Results:**
- ✅ Section only shows when raw_message exists
- ✅ Collapsed by default
- ✅ Expands/collapses on click
- ✅ Displays properly formatted message
- ✅ Copy to clipboard works
- ✅ Visual feedback on copy

---

### 4. Auto-Refresh Test

**Steps:**
1. Navigate to Queue Management page
2. Note the "Last updated" timestamp
3. Wait 15 seconds
4. Verify list refreshes automatically
5. Verify timestamp updates
6. Switch to another browser tab
7. Wait 20 seconds
8. Switch back to queue tab
9. Verify list refreshes immediately

**Expected Results:**
- ✅ Auto-refresh every 15 seconds
- ✅ Timestamp updates correctly
- ✅ Pauses when tab hidden
- ✅ Resumes when tab visible

---

### 5. Manual Refresh Test

**Steps:**
1. Navigate to Queue Management page
2. Click "Refresh" button
3. Verify button shows "Refreshing..." during load
4. Verify timestamp updates
5. Verify list data refreshes

**Expected Results:**
- ✅ Manual refresh works
- ✅ Loading state displays
- ✅ Data updates correctly

---

### 6. Toast Notifications Test

**Single Operations:**
1. Click "Retry" on any entry
2. Verify success toast appears
3. Wait for toast to auto-dismiss (5 seconds)
4. Click "Delete" on an entry
5. Confirm deletion
6. Verify success toast appears

**Bulk Operations:**
1. Select 3 entries
2. Click "Retry All"
3. Verify success toast with count ("Successfully retried 3 queue entries")
4. Select 2 entries
5. Click "Delete All" and confirm
6. Verify success toast with count

**Error Handling:**
1. Disconnect from backend (stop API)
2. Try to retry an entry
3. Verify error toast appears with message
4. Verify toast is red (error color)

**Expected Results:**
- ✅ Success toasts show for all operations
- ✅ Toasts include counts for bulk operations
- ✅ Error toasts show on failures
- ✅ Toasts auto-dismiss after 5 seconds
- ✅ Manual dismiss works (X button)

---

### 7. Loading States Test

**Steps:**
1. Click any action button (Retry, Delete)
2. Verify button becomes disabled during operation
3. For bulk operations, verify all buttons disabled
4. For manual refresh, verify spinner appears

**Expected Results:**
- ✅ Buttons disabled during operations
- ✅ Loading spinners display
- ✅ No double-click issues

---

### 8. Filter + Bulk Selection Test

**Steps:**
1. Navigate to Queue Management page
2. Select "Pending" filter
3. Select 2 pending entries
4. Switch to "Deferred" filter
5. Verify selection is cleared
6. Select 2 deferred entries
7. Click "Retry All"
8. Verify only selected entries are retried

**Expected Results:**
- ✅ Filter changes clear selection
- ✅ Bulk operations only affect selected entries
- ✅ Filter counts update correctly

---

### 9. Detail View Actions Test

**Steps:**
1. Click on any queue entry
2. Click "Retry Delivery" button
3. Verify success toast appears
4. Verify entry data refreshes
5. Click "Delete Entry" button
6. Verify confirmation buttons appear
7. Click "Confirm Delete"
8. Verify success toast appears
9. Verify redirected to queue list

**Expected Results:**
- ✅ Retry works from detail view
- ✅ Delete confirmation works
- ✅ Navigation back to list works
- ✅ Toasts display correctly

---

### 10. Error Display Test

**Steps:**
1. Find a queue entry with status "deferred" or "bounced"
2. Click to view details
3. Verify error message section appears
4. Verify error displays in red bordered box
5. Verify error message is readable

**Expected Results:**
- ✅ Error section only shows when error exists
- ✅ Red border and background
- ✅ Monospace font for error text
- ✅ Proper styling

---

## Edge Cases to Test

### Empty State
- Visit queue page with no entries
- Verify "No queue entries found" message

### Large Selection
- Select 10+ entries
- Verify bulk operations still work
- Verify no performance issues

### Rapid Actions
- Click retry multiple times quickly
- Verify no duplicate operations
- Verify loading states prevent double-clicks

### Long Messages
- View entry with very long subject
- Verify truncation in list view
- Verify full subject in detail view

### Network Errors
- Disconnect backend during operation
- Verify error toast displays
- Verify error message is helpful

---

## Browser Compatibility

Test in:
- ✅ Chrome (latest)
- ✅ Firefox (latest)
- ✅ Safari (latest)
- ✅ Edge (latest)

---

## Performance Benchmarks

Expected performance:
- Queue list initial load: < 2 seconds
- Filter change: < 500ms
- Detail view load: < 1 second
- Bulk retry (5 entries): < 5 seconds
- Bulk delete (5 entries): < 5 seconds

---

## Known Limitations

1. **Bulk Operations**: Sequential processing (one at a time)
   - Trade-off: Prevents API overload
   - Future: Could parallelize with rate limiting

2. **Auto-Refresh**: 15-second polling
   - Trade-off: Simple implementation
   - Future: WebSocket support for real-time updates

3. **Raw Message**: Optional field
   - Only shows if backend provides raw_message
   - Gracefully handles absence

---

## Troubleshooting

### Toasts Not Appearing
- Check browser console for errors
- Verify AppShell includes NotificationContainer
- Verify uiStore is working

### Auto-Refresh Not Working
- Check browser console for errors
- Verify visibility API is supported
- Check if tab is focused

### Bulk Operations Failing
- Check network tab for API calls
- Verify backend endpoints are accessible
- Check console for error messages

### Checkboxes Not Working
- Verify selectedIds state updates
- Check Redux DevTools for state changes
- Verify toggleSelection method is called

---

## Success Criteria

All features working:
- ✅ Bulk operations (select, retry, delete)
- ✅ Raw message viewer
- ✅ Auto-refresh (15 seconds)
- ✅ Manual refresh
- ✅ Toast notifications
- ✅ Loading states
- ✅ Error handling

No issues:
- ✅ No TypeScript errors
- ✅ No console errors
- ✅ No visual glitches
- ✅ Responsive design works
- ✅ Proper accessibility

---

**Ready for Production:** ✅

All tests passing means Stage 4 is complete and ready for deployment.
