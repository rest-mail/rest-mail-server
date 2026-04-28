# Stage 5: Pipelines & Filter Management - Detailed Implementation Plan

**Status:** 🚧 NOT STARTED (Backend API ready, Frontend pending)
**Priority:** HIGH
**Estimated Effort:** 5-7 days
**Dependencies:** Stage 1-4 (Dashboard, Domains, Mailboxes)

---

## Overview

Implement complete pipeline and custom filter management with a visual filter builder, drag-and-drop ordering, custom JavaScript filter editor with syntax highlighting, filter testing interface, and pipeline execution logs viewer.

**Current State:**
- ✅ Backend API fully implemented (handlers, models, routes)
- ✅ Pipeline engine with 20+ built-in filters
- ✅ Custom filter support (JavaScript execution via sidecar)
- ✅ Pipeline logs tracking
- ❌ Frontend UI not started
- ❌ No Zustand stores for pipelines/filters
- ❌ No visual filter builder

**Key Features:**
- Pipeline list with domain/direction filtering
- Visual filter builder with drag-and-drop ordering
- Custom JavaScript filter editor with Monaco/CodeMirror
- Filter testing interface with sample messages
- Pipeline execution logs viewer with filtering
- Real-time filter validation
- Built-in filter type registry

---

## Backend API Reference

### Pipeline Endpoints (Already Available)

```
GET    /api/v1/admin/pipelines                  # List all pipelines
POST   /api/v1/admin/pipelines                  # Create pipeline
PATCH  /api/v1/admin/pipelines/{id}             # Update pipeline
DELETE /api/v1/admin/pipelines/{id}             # Delete pipeline
POST   /api/v1/admin/pipelines/test             # Test pipeline with sample email
POST   /api/v1/admin/pipelines/test-filter      # Test single filter
GET    /api/v1/admin/pipelines/logs             # List pipeline execution logs
```

**Query Parameters:**
- `domain_id` - Filter by domain
- `limit` - Page size (default 50, max 200)
- `offset` - Pagination offset
- `pipeline_id` - Filter logs by pipeline
- `direction` - Filter by direction (inbound/outbound)
- `action` - Filter logs by action (continue/reject/quarantine/discard)

### Custom Filter Endpoints (Already Available)

```
GET    /api/v1/admin/custom-filters             # List custom filters
POST   /api/v1/admin/custom-filters             # Create custom filter
GET    /api/v1/admin/custom-filters/{id}        # Get custom filter
PATCH  /api/v1/admin/custom-filters/{id}        # Update custom filter
DELETE /api/v1/admin/custom-filters/{id}        # Delete custom filter
POST   /api/v1/admin/custom-filters/validate    # Validate filter script
POST   /api/v1/admin/custom-filters/{id}/test   # Test custom filter
```

### Data Models

**Pipeline:**
```typescript
{
  id: number
  domain_id: number
  direction: "inbound" | "outbound"
  filters: FilterConfig[]  // JSONB array
  active: boolean
  created_at: string
  updated_at: string
  domain?: Domain
}
```

**FilterConfig:**
```typescript
{
  name: string              // Filter name (e.g., "spf_check", "custom:my_filter")
  type: "action" | "transform"
  enabled: boolean
  unskippable?: boolean     // Cannot be disabled or skipped
  config?: Record<string, any>  // Filter-specific config
}
```

**CustomFilter:**
```typescript
{
  id: number
  domain_id: number
  name: string
  description: string
  filter_type: "action" | "transform"
  direction: "inbound" | "outbound" | "both"
  config: {
    script: string          // JavaScript code
  }
  enabled: boolean
  created_at: string
  updated_at: string
}
```

**PipelineLog:**
```typescript
{
  id: number
  pipeline_id: number
  message_id?: number
  direction: "inbound" | "outbound"
  action: "continue" | "reject" | "quarantine" | "discard"
  steps: FilterLogStep[]   // JSONB array
  duration_ms: number
  created_at: string
}
```

**FilterLogStep:**
```typescript
{
  filter: string
  result: string
  detail?: string
  duration_ms?: number
}
```

---

## Available Built-in Filters

### Inbound Filters (Action)
1. **size_check** - Reject messages exceeding max size
   - Config: `{ max_size_mb: number }`
   - Default: 25 MB

2. **spf_check** - SPF validation
   - Config: `{ fail_action: "tag" | "reject" }`
   - Default: tag

3. **dmarc_check** - DMARC validation
   - Config: `{ fail_action: "tag" | "quarantine" | "reject" }`
   - Default: quarantine

4. **domain_allowlist** - Check sender against domain allow/block rules
   - Config: `{}`

5. **contact_whitelist** - Skip spam checks for trusted contacts
   - Config: `{}`

6. **greylist** - Temporary rejection for unknown senders
   - Config: `{ delay_minutes: number, ttl_days: number }`
   - Default: 5 min delay, 36 day TTL

7. **header_validate** - Validate required headers
   - Config: `{}`

8. **recipient_check** - Verify recipient exists
   - Config: `{}`
   - Unskippable: true

9. **rspamd** - Spam scoring via Rspamd
   - Config: `{ host: string, threshold: number }`

10. **clamav** - Virus scanning
    - Config: `{ host: string, port: number }`

11. **duplicate** - Detect duplicate messages
    - Config: `{ window_hours: number }`

### Inbound Filters (Transform)
1. **dkim_verify** - Verify DKIM signatures
   - Config: `{ fail_action: "tag" | "reject" }`

2. **arc_verify** - Verify ARC chain
   - Config: `{}`

3. **extract_attachments** - Extract and store attachments
   - Config: `{ storage_dir: string }`

4. **sieve** - Apply user Sieve scripts
   - Config: `{}`

5. **vacation** - Auto-reply for out-of-office
   - Config: `{}`

### Outbound Filters (Action)
1. **sender_verify** - Verify sender is authorized
   - Config: `{}`
   - Unskippable: true

2. **rate_limit** - Limit sending rate
   - Config: `{ per_sender_per_hour: number }`
   - Default: 100/hour

### Outbound Filters (Transform)
1. **header_cleanup** - Remove internal headers
   - Config: `{}`

2. **arc_seal** - Add ARC-Seal headers
   - Config: `{}`

3. **dkim_sign** - Sign with DKIM
   - Config: `{}`
   - Unskippable: true

### Custom Filters
- **javascript** - User-defined JavaScript filters
  - Config: `{ script: string }`
  - Executes via JS sidecar (Node.js sandbox)

---

## Frontend Implementation

### 1. Route Structure

```
/admin/pipelines                    # Pipeline list
/admin/pipelines/new                # Create pipeline
/admin/pipelines/$id                # View/edit pipeline
/admin/pipelines/$id/test           # Test pipeline
/admin/pipelines/logs               # Pipeline logs viewer

/admin/custom-filters               # Custom filter list
/admin/custom-filters/new           # Create custom filter
/admin/custom-filters/$id           # Edit custom filter
/admin/custom-filters/$id/test      # Test custom filter
```

**File Structure:**
```
admin/src/routes/
├── pipelines/
│   ├── index.tsx                   # List view
│   ├── $id.tsx                     # Pipeline editor
│   ├── new.tsx                     # Create pipeline
│   ├── logs.tsx                    # Logs viewer
│   └── test.tsx                    # Test interface
└── custom-filters/
    ├── index.tsx                   # List view
    ├── $id.tsx                     # Filter editor
    ├── new.tsx                     # Create filter
    └── test.tsx                    # Test interface
```

### 2. Zustand Stores

#### pipelineStore.ts
```typescript
interface PipelineStore {
  pipelines: Pipeline[]
  selectedPipeline: Pipeline | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchPipelines: (domainId?: number) => Promise<void>
  fetchPipeline: (id: number) => Promise<Pipeline>
  createPipeline: (data: CreatePipelineDto) => Promise<Pipeline>
  updatePipeline: (id: number, data: UpdatePipelineDto) => Promise<Pipeline>
  deletePipeline: (id: number) => Promise<void>
  testPipeline: (pipelineId: number, email: EmailJSON) => Promise<PipelineTestResult>
  testFilter: (filterName: string, config: any, email: EmailJSON) => Promise<FilterResult>

  // Pipeline logs
  fetchLogs: (params: LogQueryParams) => Promise<PipelineLog[]>
}
```

#### customFilterStore.ts
```typescript
interface CustomFilterStore {
  filters: CustomFilter[]
  selectedFilter: CustomFilter | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchFilters: (domainId?: number) => Promise<void>
  fetchFilter: (id: number) => Promise<CustomFilter>
  createFilter: (data: CreateCustomFilterDto) => Promise<CustomFilter>
  updateFilter: (id: number, data: UpdateCustomFilterDto) => Promise<CustomFilter>
  deleteFilter: (id: number) => Promise<void>
  validateScript: (script: string, email?: EmailJSON) => Promise<ValidationResult>
  testFilter: (id: number, email?: EmailJSON) => Promise<FilterTestResult>
}
```

#### filterRegistryStore.ts (Static, no API calls)
```typescript
interface FilterRegistryStore {
  builtinFilters: FilterDefinition[]
  getFiltersByType: (type: FilterType) => FilterDefinition[]
  getFiltersByDirection: (direction: Direction) => FilterDefinition[]
  getFilterConfig: (name: string) => FilterDefinition | null
}

interface FilterDefinition {
  name: string
  displayName: string
  description: string
  type: "action" | "transform"
  direction: "inbound" | "outbound" | "both"
  unskippable: boolean
  configSchema?: Record<string, ConfigField>
  icon?: string
  category: "authentication" | "spam" | "content" | "delivery" | "custom"
}
```

### 3. Component Architecture

#### Pipeline List Components
```
components/pipelines/
├── PipelineList.tsx                # Main list table
├── PipelineCard.tsx                # Pipeline summary card
├── PipelineFilters.tsx             # Domain/direction filter
├── PipelineStatusBadge.tsx         # Active/inactive badge
└── PipelineActions.tsx             # Edit/Delete/Test actions
```

#### Visual Filter Builder
```
components/pipelines/editor/
├── FilterBuilder.tsx               # Main filter builder
├── FilterPalette.tsx               # Drag source for built-in filters
├── FilterList.tsx                  # Drag-drop sortable filter list
├── FilterCard.tsx                  # Individual filter in pipeline
├── FilterConfigEditor.tsx          # Config form for selected filter
├── FilterPreview.tsx               # Visual preview of filter
└── CustomFilterSelector.tsx       # Select custom filters to add
```

**Key Features:**
- **Drag-and-drop:** Use `@dnd-kit/core` or `react-beautiful-dnd`
- **Filter ordering:** Visual reordering with drag handles
- **Live validation:** Show errors if config invalid
- **Inline editing:** Click filter card to open config panel
- **Unskippable indicators:** Lock icon for required filters

#### Custom Filter Code Editor
```
components/custom-filters/
├── CodeEditor.tsx                  # Monaco/CodeMirror wrapper
├── FilterScriptEditor.tsx          # Full editor UI
├── FilterTestPanel.tsx             # Side panel for testing
├── SampleEmailBuilder.tsx          # Build test email JSON
└── FilterValidationErrors.tsx      # Display validation errors
```

**Code Editor Recommendation:**
Use **Monaco Editor** (same as VS Code):
- Full TypeScript support
- Syntax highlighting for JavaScript
- IntelliSense and autocomplete
- Error highlighting inline
- Integrated with React: `@monaco-editor/react`

**Alternative:** CodeMirror 6 (lighter weight)
- Smaller bundle size
- Fast rendering
- Good for simpler use cases

**Monaco Editor Setup:**
```tsx
import Editor from '@monaco-editor/react'

<Editor
  height="600px"
  defaultLanguage="javascript"
  theme="vs-dark"
  value={script}
  onChange={(value) => setScript(value || '')}
  options={{
    minimap: { enabled: false },
    fontSize: 14,
    lineNumbers: 'on',
    roundedSelection: false,
    scrollBeyondLastLine: false,
    readOnly: false,
  }}
/>
```

**Filter Script Template:**
```javascript
/**
 * Custom email filter
 * @param {EmailJSON} email - The email object
 * @returns {FilterResult} - The filter result
 */
function filter(email) {
  // Your filter logic here

  // Example: Reject emails with specific subject
  if (email.headers.subject.includes('SPAM')) {
    return {
      action: 'reject',
      message: 'Rejected: Subject contains spam keyword'
    }
  }

  // Example: Add header
  email.headers.extra['X-Custom-Filter'] = 'Processed'

  return {
    action: 'continue',
    message: email
  }
}
```

#### Filter Testing Interface
```
components/pipelines/testing/
├── TestPipelineForm.tsx            # Test full pipeline
├── TestFilterForm.tsx              # Test single filter
├── EmailJSONBuilder.tsx            # Build test email
├── TestResultDisplay.tsx           # Show test results
├── FilterStepTrace.tsx             # Step-by-step execution trace
└── TestEmailPresets.tsx            # Pre-built test emails
```

**Test Email Presets:**
- Clean email (no spam, valid auth)
- Spam email (high spam score)
- Phishing email (domain spoofing)
- Large attachment email
- Invalid headers email
- Greylist trigger email

#### Pipeline Logs Viewer
```
components/pipelines/logs/
├── PipelineLogsList.tsx            # Main logs table
├── LogFilters.tsx                  # Filter by pipeline/action/date
├── LogDetailModal.tsx              # Detailed log entry view
├── LogStepTimeline.tsx             # Visual timeline of steps
└── LogActionBadge.tsx              # Action badge (continue/reject/etc)
```

**Log Features:**
- Real-time polling (every 10s when viewing logs)
- Filter by pipeline, direction, action, date range
- Expandable rows showing step-by-step execution
- Duration histogram showing slow filters
- Export logs to CSV

---

## UI/UX Design Specifications

### Pipeline List View

**Layout:**
- Table with columns: Domain, Direction, Filters, Active, Last Modified
- Filter bar: Domain dropdown, Direction tabs (All/Inbound/Outbound)
- Action buttons: New Pipeline, Refresh
- Per-row actions: View/Edit, Test, Delete

**Filter Count Badge:**
Show enabled/total filters (e.g., "8/12 enabled")

### Pipeline Editor

**Three-Panel Layout:**
1. **Left Panel (240px):** Filter Palette
   - Accordion grouped by category
   - Search bar at top
   - Drag filters to middle panel
   - Show filter type badge (action/transform)

2. **Middle Panel (Flexible):** Pipeline Canvas
   - Drag-drop sortable filter list
   - Visual flow with arrows between filters
   - Filter cards show:
     - Name and description
     - Enable/disable toggle
     - Config summary
     - Lock icon if unskippable
     - Drag handle
     - Delete button
   - Empty state: "Drag filters here to build your pipeline"

3. **Right Panel (360px):** Config Editor
   - Opens when filter card clicked
   - Form fields based on filter's configSchema
   - Real-time validation
   - Save/Cancel buttons

**Top Bar:**
- Pipeline name (editable inline)
- Domain selector
- Direction selector (inbound/outbound)
- Active toggle
- Actions: Save, Test, Delete

### Custom Filter Editor

**Two-Panel Layout:**
1. **Left Panel (60%):** Code Editor
   - Monaco editor with JavaScript syntax
   - Line numbers, error highlighting
   - Auto-save draft (localStorage)
   - Template selector dropdown

2. **Right Panel (40%):** Test Panel
   - Sample email builder (collapsible)
   - Test button
   - Result display:
     - Action taken
     - Modified email (diff view)
     - Execution time
     - Logs/errors

**Top Bar:**
- Filter name (editable)
- Description
- Type selector (action/transform)
- Direction selector
- Enable toggle
- Actions: Save, Validate, Test, Delete

### Pipeline Logs Viewer

**Layout:**
- Filter bar: Pipeline, Direction, Action, Date range
- Summary cards: Total executions, Success rate, Avg duration, Errors
- Logs table:
  - Timestamp
  - Pipeline
  - Direction
  - Action badge (color-coded)
  - Steps count
  - Duration
  - Expand button
- Expanded row shows:
  - Step-by-step timeline with durations
  - Filter logs with details
  - Email metadata (sender, subject, size)

**Features:**
- Auto-refresh toggle (10s interval)
- Export to CSV button
- "Jump to pipeline" link

---

## Implementation Plan

### Day 1: Setup & Stores
**Tasks:**
- [ ] Create route files for pipelines and custom filters
- [ ] Implement `pipelineStore.ts` with all actions
- [ ] Implement `customFilterStore.ts` with all actions
- [ ] Create `filterRegistryStore.ts` with built-in filter definitions
- [ ] Add API client functions for pipelines and custom filters
- [ ] Test API integration with backend

**Deliverables:**
- Zustand stores functional
- API client methods working
- Basic route structure

### Day 2: Pipeline List & Basic Editor
**Tasks:**
- [ ] Build PipelineList component with table
- [ ] Add domain/direction filtering
- [ ] Implement PipelineCard component
- [ ] Create basic pipeline editor layout (3 panels)
- [ ] Implement FilterPalette with accordion
- [ ] Add filter search in palette

**Deliverables:**
- Pipeline list view functional
- Basic editor layout complete
- Filter palette showing built-in filters

### Day 3: Visual Filter Builder (Drag & Drop)
**Tasks:**
- [ ] Install `@dnd-kit/core` and `@dnd-kit/sortable`
- [ ] Implement drag-drop from palette to canvas
- [ ] Implement sortable filter list with reordering
- [ ] Create FilterCard component with toggle/delete
- [ ] Add empty state for pipeline canvas
- [ ] Implement filter config editor panel

**Deliverables:**
- Drag-drop filter builder working
- Filter reordering functional
- Config editor opens on click

### Day 4: Custom Filter Editor with Monaco
**Tasks:**
- [ ] Install `@monaco-editor/react`
- [ ] Create CodeEditor wrapper component
- [ ] Implement FilterScriptEditor with 2-panel layout
- [ ] Add script validation API integration
- [ ] Create filter script templates
- [ ] Implement syntax error highlighting

**Deliverables:**
- Code editor functional
- Real-time validation working
- Template selector implemented

### Day 5: Filter Testing Interface
**Tasks:**
- [ ] Build TestPipelineForm component
- [ ] Implement EmailJSONBuilder for sample emails
- [ ] Create TestResultDisplay with step trace
- [ ] Add pre-built test email presets
- [ ] Implement TestFilterForm for single filters
- [ ] Add diff view for transformed emails

**Deliverables:**
- Pipeline testing functional
- Filter testing working
- Test email builder complete

### Day 6: Pipeline Logs Viewer
**Tasks:**
- [ ] Create PipelineLogsList component
- [ ] Implement log filtering (pipeline/action/date)
- [ ] Add expandable rows with step timeline
- [ ] Create LogStepTimeline visualization
- [ ] Implement auto-refresh toggle
- [ ] Add export to CSV feature

**Deliverables:**
- Logs viewer functional
- Filtering and pagination working
- Step-by-step trace display

### Day 7: Polish & Integration
**Tasks:**
- [ ] Add loading states and error handling
- [ ] Implement toast notifications for actions
- [ ] Add confirmation modals (delete, unsaved changes)
- [ ] Create help tooltips for filters
- [ ] Add keyboard shortcuts (Ctrl+S to save)
- [ ] Test end-to-end workflows
- [ ] Fix responsive layout issues
- [ ] Add RBAC capability checks (`pipelines:read`, `pipelines:write`, etc.)

**Deliverables:**
- Feature complete and polished
- Error handling robust
- RBAC integration working

---

## Filter Type Registry (Static Data)

The filter registry should be hardcoded in `filterRegistryStore.ts` with metadata for all built-in filters. This enables the UI to display filter information without API calls.

**Filter Categories:**
- 🔐 **Authentication:** SPF, DKIM, DMARC, ARC
- 🛡️ **Spam & Security:** Rspamd, ClamAV, Greylist, Header validation
- 🚫 **Access Control:** Domain allowlist, Contact whitelist, Sender verify
- 📎 **Content Processing:** Attachment extraction, Size check, Duplicate detection
- 📧 **Delivery:** Recipient check, Sieve, Vacation, Rate limit
- 🔧 **Technical:** Header cleanup, DKIM signing, ARC seal
- ⚙️ **Custom:** JavaScript filters

**Example Filter Definition:**
```typescript
{
  name: "spf_check",
  displayName: "SPF Verification",
  description: "Validates sender's IP against SPF records",
  type: "action",
  direction: "inbound",
  unskippable: false,
  category: "authentication",
  icon: "ShieldCheck",
  configSchema: {
    fail_action: {
      type: "select",
      label: "Action on SPF Fail",
      options: ["tag", "reject", "quarantine"],
      default: "tag",
      required: true
    }
  }
}
```

---

## Code Editor Library Comparison

| Feature | Monaco Editor | CodeMirror 6 |
|---------|--------------|--------------|
| **Bundle Size** | ~3MB (chunked) | ~500KB |
| **Language Support** | Excellent (100+ languages) | Good (via packages) |
| **IntelliSense** | Yes (TypeScript) | Limited |
| **Performance** | Good (heavy initial load) | Excellent |
| **Customization** | Moderate | High |
| **React Integration** | `@monaco-editor/react` | `@uiw/react-codemirror` |
| **Theme Support** | VS Code themes | Custom themes |
| **Debugging** | Built-in breakpoints | Minimal |

**Recommendation:** **Monaco Editor**
- Better for JavaScript editing (IntelliSense, error detection)
- Familiar VS Code UI
- Better developer experience
- Worth the bundle size for this feature

---

## Testing Checklist

### Pipeline Management
- [ ] List pipelines with domain filter
- [ ] Create new pipeline with default filters
- [ ] Edit pipeline filters (add, remove, reorder)
- [ ] Update filter configuration
- [ ] Toggle pipeline active status
- [ ] Delete pipeline with confirmation
- [ ] Test pipeline with sample email
- [ ] View pipeline execution logs

### Filter Builder
- [ ] Drag filter from palette to canvas
- [ ] Reorder filters via drag-drop
- [ ] Enable/disable individual filters
- [ ] Edit filter configuration
- [ ] Validation errors for invalid config
- [ ] Cannot remove unskippable filters
- [ ] Save pipeline with new filter order

### Custom Filters
- [ ] List custom filters with domain filter
- [ ] Create new custom filter with script
- [ ] Edit filter script with syntax highlighting
- [ ] Validate script (API call)
- [ ] Test custom filter with sample email
- [ ] View execution errors in test panel
- [ ] Enable/disable custom filter
- [ ] Delete custom filter

### Code Editor
- [ ] Syntax highlighting working
- [ ] Line numbers displayed
- [ ] Error highlighting for invalid syntax
- [ ] Auto-save to localStorage
- [ ] Load filter script template
- [ ] Ctrl+S saves filter
- [ ] Validation errors displayed inline

### Pipeline Logs
- [ ] List logs with pagination
- [ ] Filter by pipeline
- [ ] Filter by direction
- [ ] Filter by action
- [ ] Filter by date range
- [ ] Expand row shows step details
- [ ] Step timeline with durations
- [ ] Auto-refresh toggle works
- [ ] Export to CSV

---

## RBAC Capabilities

Protect pipeline routes with these capabilities:

| Action | Required Capability | Fallback |
|--------|---------------------|----------|
| View pipelines | `pipelines:read` | Hide route |
| Create/edit pipeline | `pipelines:write` | Button disabled |
| Delete pipeline | `pipelines:delete` | Button hidden |
| Test pipeline | `pipelines:write` | Button disabled |
| View logs | `pipelines:read` | Hide route |
| View custom filters | `pipelines:read` | Hide route |
| Create/edit custom filter | `pipelines:write` | Button disabled |
| Delete custom filter | `pipelines:delete` | Button hidden |

**Wildcard Superadmin:**
Users with `*` capability bypass all checks.

---

## Success Criteria

### Functionality
- [x] Backend API endpoints exist and functional
- [ ] Pipeline list displays all pipelines with filters
- [ ] Visual filter builder with drag-drop working
- [ ] Filter config editor with validation
- [ ] Custom filter code editor with Monaco
- [ ] Filter testing with sample emails
- [ ] Pipeline logs viewer with filtering
- [ ] Real-time script validation

### User Experience
- [ ] Intuitive drag-drop interface
- [ ] Clear visual feedback (hover, drag states)
- [ ] Responsive layout (3-panel editor adapts)
- [ ] Fast filter reordering (no lag)
- [ ] Helpful error messages
- [ ] Keyboard shortcuts work

### Performance
- [ ] Filter palette search instant (<50ms)
- [ ] Pipeline save completes <500ms
- [ ] Code editor loads <1s
- [ ] Test execution shows progress
- [ ] Logs load in <1s
- [ ] No UI freezing during drag-drop

---

## Future Enhancements (Post-MVP)

1. **Pipeline Templates:** Pre-built pipelines for common use cases
2. **Filter Marketplace:** Share custom filters between domains
3. **Visual Pipeline Graph:** Flow diagram showing filter connections
4. **A/B Testing:** Compare pipeline variants
5. **Pipeline Analytics:** Charts showing filter effectiveness
6. **Webhook Filter:** Trigger external APIs from pipelines
7. **Machine Learning Filter:** AI-powered spam/phishing detection
8. **Pipeline Cloning:** Duplicate pipeline across domains
9. **Filter Performance Profiling:** Identify slow filters
10. **Visual Script Builder:** No-code custom filter builder

---

## Resources & References

### Monaco Editor
- Docs: https://microsoft.github.io/monaco-editor/
- React wrapper: https://github.com/suren-atoyan/monaco-react
- TypeScript definitions: Built-in

### Drag & Drop
- dnd-kit: https://dndkit.com/
- react-beautiful-dnd: https://github.com/atlassian/react-beautiful-dnd

### Filter Documentation
- Rspamd: https://rspamd.com/doc/
- SpamAssassin: https://spamassassin.apache.org/
- ClamAV: https://www.clamav.net/
- Sieve: https://www.rfc-editor.org/rfc/rfc5228

### Email Testing
- MXToolbox: https://mxtoolbox.com/
- Mail-tester: https://www.mail-tester.com/

---

**Plan Created:** 2026-02-23
**Last Updated:** 2026-02-23
**Status:** Ready for implementation
**Next Steps:** Begin Day 1 tasks (setup & stores)
