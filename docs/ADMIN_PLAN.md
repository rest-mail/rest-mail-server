# REST Mail Admin Website - Implementation Plan

## Executive Summary

The REST Mail Admin Website is a standalone frontend application that provides a comprehensive web-based interface for administering the REST Mail email server. It is built as a separate application (similar to the webmail) that uses the REST API as its backend, with no separate server-side code required.

**Tech Stack:**
- **Framework:** TanStack Start (React-based meta-framework)
- **UI Library:** shadcn/ui (accessible React component library)
- **Styling:** Tailwind CSS
- **State Management:**
  - **Zustand** for global state stores (domains, mailboxes, users, etc.)
  - **TanStack Query** for API data fetching and caching
- **Routing:** TanStack Router (built into TanStack Start)
- **Form Handling:** React Hook Form + Zod validation
- **Authentication:** JWT tokens (access + refresh tokens)

---

## 1. Project Structure

```
admin/                          # Standalone admin app directory
├── app/
│   ├── routes/                 # TanStack Start routes
│   │   ├── __root.tsx         # Root layout with auth check
│   │   ├── index.tsx          # Redirect to /dashboard or /login
│   │   ├── login.tsx          # Login page (public)
│   │   ├── dashboard/
│   │   │   └── index.tsx      # Dashboard overview
│   │   ├── domains/
│   │   │   ├── index.tsx      # Domain list
│   │   │   ├── $id.tsx        # Domain details
│   │   │   └── new.tsx        # Create domain
│   │   ├── mailboxes/
│   │   │   ├── index.tsx      # Mailbox list
│   │   │   ├── $id.tsx        # Mailbox details
│   │   │   └── new.tsx        # Create mailbox
│   │   ├── aliases/
│   │   │   ├── index.tsx      # Alias list
│   │   │   └── new.tsx        # Create alias
│   │   ├── queue/
│   │   │   ├── index.tsx      # Queue management
│   │   │   └── $id.tsx        # Queue entry details
│   │   ├── pipelines/
│   │   │   ├── index.tsx      # Pipeline list
│   │   │   ├── $id.tsx        # Pipeline details
│   │   │   └── new.tsx        # Create pipeline
│   │   ├── admin-users/
│   │   │   ├── index.tsx      # Admin user management
│   │   │   ├── $id.tsx        # Admin user details
│   │   │   └── new.tsx        # Create admin user
│   │   ├── settings/
│   │   │   ├── index.tsx      # Settings overview
│   │   │   ├── certificates.tsx
│   │   │   ├── dkim.tsx
│   │   │   └── bans.tsx
│   │   └── logs/
│   │       ├── activity.tsx   # Activity logs
│   │       └── delivery.tsx   # Delivery logs
│   ├── components/
│   │   ├── ui/                # shadcn/ui components
│   │   ├── layout/
│   │   │   ├── AppShell.tsx   # Main layout with sidebar
│   │   │   ├── Sidebar.tsx    # Navigation sidebar
│   │   │   └── Header.tsx     # Page header
│   │   ├── auth/
│   │   │   └── RequireAuth.tsx # Auth guard component
│   │   └── domains/
│   │       ├── DomainList.tsx
│   │       ├── DomainForm.tsx
│   │       └── DomainDNS.tsx
│   ├── lib/
│   │   ├── api/               # API client functions
│   │   │   ├── client.ts      # Axios instance with auth
│   │   │   ├── auth.ts        # Auth API calls
│   │   │   ├── domains.ts     # Domain API calls
│   │   │   ├── mailboxes.ts   # Mailbox API calls
│   │   │   └── ...            # Other API modules
│   │   ├── stores/            # Zustand state stores
│   │   │   ├── authStore.ts   # Auth state (user, token)
│   │   │   ├── domainStore.ts # Domain data cache
│   │   │   ├── mailboxStore.ts # Mailbox data cache
│   │   │   ├── aliasStore.ts  # Alias data cache
│   │   │   ├── queueStore.ts  # Queue data cache
│   │   │   └── uiStore.ts     # UI state (sidebar, modals)
│   │   ├── hooks/
│   │   │   ├── useAuth.ts     # Auth state hook
│   │   │   ├── useCapabilities.ts # RBAC hook
│   │   │   └── useApiMutation.ts # Mutation wrapper
│   │   ├── utils/
│   │   │   ├── format.ts      # Formatting utilities
│   │   │   └── validation.ts  # Zod schemas
│   │   └── types/
│   │       └── api.ts         # TypeScript types for API
│   ├── router.tsx             # Router configuration
│   └── ssr.tsx                # SSR entry point
├── public/
│   └── favicon.ico
├── package.json
├── tailwind.config.ts
├── tsconfig.json
└── vite.config.ts
```

---

## 2. State Management Architecture

### 2.1 Zustand Stores

The application uses **Zustand** for managing global state and caching API data. Each major data entity has its own store that:
- Caches data fetched from the REST API
- Provides methods for CRUD operations
- Hydrates React components with normalized data
- Manages loading and error states

**Store Pattern:**
```typescript
// Example: domainStore.ts
import { create } from 'zustand'

interface DomainStore {
  domains: Domain[]
  isLoading: boolean
  error: string | null

  // Actions
  setDomains: (domains: Domain[]) => void
  addDomain: (domain: Domain) => void
  updateDomain: (id: string, updates: Partial<Domain>) => void
  removeDomain: (id: string) => void

  // Async actions with API calls
  fetchDomains: () => Promise<void>
  createDomain: (data: CreateDomainRequest) => Promise<void>
  deleteDomain: (id: string) => Promise<void>
}
```

**Store List:**
- **authStore.ts** - User authentication state, JWT tokens, capabilities
- **domainStore.ts** - Domain list, CRUD operations, DNS status cache
- **mailboxStore.ts** - Mailbox data, quota usage, filtering
- **aliasStore.ts** - Email aliases and forwarding rules
- **queueStore.ts** - Queue entries, retry operations
- **pipelineStore.ts** - Email pipelines and filter configurations
- **adminUserStore.ts** - Admin user management, role assignments
- **uiStore.ts** - UI state (sidebar open/closed, active modals, notifications)

### 2.2 Integration with TanStack Query

While Zustand manages the state, **TanStack Query** can be used for:
- Server state synchronization
- Background refetching
- Optimistic updates
- Cache invalidation strategies

**Hybrid Approach:**
1. **TanStack Query** fetches data from API
2. On successful fetch, data is stored in **Zustand store**
3. React components consume data from **Zustand hooks**
4. Mutations update both Zustand store and invalidate TanStack Query cache

**Benefits:**
- Single source of truth (Zustand stores)
- Automatic UI updates when store changes
- Server-state sync with TanStack Query
- Predictable data flow
- Easy debugging with Zustand DevTools

---

## 3. Complete Feature Set

Based on the REST API exploration, the admin website will provide the following functionality:

### 2.1 Dashboard (Overview)
**Route:** `/dashboard`
**Capabilities Required:** None (all admin users can view)

**Features:**
- **System Metrics Cards:**
  - Total domains (active/inactive)
  - Total mailboxes (active/inactive)
  - Queue status (pending/deferred/bounced)
  - Messages today/this week
  - Total storage used
  - Certificate expiration warnings

- **Charts & Visualizations:**
  - Message volume (last 7 days bar chart)
  - Delivery success rate
  - Queue health status
  - Top domains by message count

- **Recent Activity:**
  - Recently created domains
  - Recently added mailboxes
  - Recent admin actions (from activity logs)
  - Queue failures requiring attention

### 2.2 Domain Management
**Route:** `/domains`
**Capabilities Required:** `domains:read`, `domains:write`, `domains:delete`

**Features:**
- **Domain List:**
  - Searchable/filterable table
  - Columns: Name, Type (traditional/restmail), Active status, Mailbox count, Created date
  - Actions: View, Edit, Delete (with confirmation)
  - Bulk actions: Activate/Deactivate multiple

- **Domain Details:**
  - Basic info (name, type, active status)
  - Default quota settings
  - Mailbox count and list
  - Alias count and list
  - DNS status check:
    - MX records
    - SPF record
    - DKIM record (with public key display)
    - DMARC record
    - MTA-STS policy status
    - TLS-RPT configuration

- **Domain Creation/Edit Form:**
  - Domain name (validation)
  - Server type (traditional/restmail)
  - Active status toggle
  - Default quota (bytes)
  - DKIM configuration:
    - Selector
    - Generate new DKIM key button
    - View public key

- **Domain Sender Rules (Allow/Block Lists):**
  - Add sender to allowlist (pattern: email or @domain)
  - Add sender to blocklist
  - List all rules with delete option

- **MTA-STS Policy Management:**
  - Create/update policy
  - Mode: none/testing/enforce
  - MX hosts list
  - Max age
  - View generated policy file

### 2.3 Mailbox Management
**Route:** `/mailboxes`
**Capabilities Required:** `mailboxes:read`, `mailboxes:write`, `mailboxes:delete`

**Features:**
- **Mailbox List:**
  - Filter by domain
  - Search by email address or name
  - Columns: Email, Display name, Domain, Quota used/total, Active, Last login
  - Sort by quota usage, last login, created date

- **Mailbox Details:**
  - Email address
  - Display name
  - Domain
  - Password reset option
  - Quota: Used/Total (GB)
  - Quota breakdown (via quota_usage table):
    - Subject bytes
    - Body bytes
    - Attachment bytes
    - Message count
  - Active status
  - Last login timestamp
  - Created/updated dates
  - Message statistics by folder

- **Mailbox Creation/Edit Form:**
  - Domain selection
  - Local part (username)
  - Display name
  - Password (with strength indicator)
  - Quota (bytes, with GB converter)
  - Active status toggle

- **Bulk Operations:**
  - Import mailboxes from CSV
  - Export mailbox list
  - Bulk quota updates

### 2.4 Alias Management
**Route:** `/aliases`
**Capabilities Required:** `domains:read`, `domains:write`

**Features:**
- **Alias List:**
  - Filter by domain
  - Search by source or destination
  - Columns: Source address, Destination address, Domain, Active
  - Quick toggle active status

- **Alias Creation/Edit Form:**
  - Domain selection
  - Source address
  - Destination address (with validation)
  - Active status toggle

- **Catch-all Configuration:**
  - Per-domain catch-all alias setup

### 2.5 Outbound Queue Management
**Route:** `/queue`
**Capabilities Required:** `queue:read`, `queue:manage`

**Features:**
- **Queue Dashboard:**
  - Status overview cards:
    - Pending count
    - Delivering count
    - Deferred count (with retry schedule)
    - Bounced count
    - Delivered count (last 24h)
  - Average delivery time
  - Failure rate

- **Queue List:**
  - Filter by:
    - Status (pending/delivering/deferred/delivered/bounced/expired)
    - Domain
    - Sender email
    - Recipient email
    - Date range
  - Columns: ID, Sender, Recipient, Domain, Status, Attempts, Last attempt, Next attempt, Last error
  - Actions per entry:
    - View raw message
    - Retry now (if deferred/bounced)
    - Bounce message (send bounce notification)
    - Delete from queue

- **Bulk Queue Actions:**
  - Select multiple entries
  - Bulk retry
  - Bulk bounce
  - Bulk delete

- **Queue Entry Details:**
  - Full delivery history
  - Error log with codes
  - Raw message preview
  - SMTP conversation log (if available)

### 2.6 Pipeline & Filter Management
**Route:** `/pipelines`
**Capabilities Required:** `pipelines:read`, `pipelines:write`, `pipelines:delete`

**Features:**
- **Pipeline List:**
  - Filter by domain and direction (inbound/outbound)
  - Columns: Domain, Direction, Filter count, Active, Last modified
  - Toggle active status

- **Pipeline Details & Editor:**
  - Domain selection
  - Direction (inbound/outbound)
  - Filter configuration (JSONB):
    - Visual filter builder UI
    - Drag-and-drop filter ordering
    - Filter types: spam_check, virus_scan, spf_check, dkim_verify, custom, etc.
  - Active status

- **Custom Filter Management:**
  - Create custom filters (JavaScript/Lua)
  - Filter types: action (continue/reject/quarantine) or transform
  - Direction: inbound/outbound
  - Code editor with syntax highlighting
  - Test filter with sample message
  - Validate filter syntax

- **Pipeline Logs:**
  - Filter by pipeline, message ID, date range
  - Columns: Message ID, Pipeline, Direction, Action taken, Duration (ms), Steps executed
  - View detailed execution trace

### 2.7 Admin User Management
**Route:** `/admin-users`
**Capabilities Required:** `users:read`, `users:write`, `users:delete`

**Features:**
- **Admin User List:**
  - Columns: Username, Email, Roles, Active, Last password change, Created date
  - Actions: Edit, Deactivate, Delete (with confirmation)

- **Admin User Details/Edit:**
  - Username
  - Email
  - Password reset (with confirmation)
  - Active status
  - Password change required flag
  - Role assignments (multi-select)
  - View assigned capabilities (computed from roles)

- **Role Management:**
  - List all roles with capability counts
  - Create custom roles
  - Assign capabilities to roles
  - View role assignments
  - System roles (superadmin, admin, readonly) are protected from deletion

- **Capability Overview:**
  - View all available capabilities
  - Capability breakdown by resource (domains, mailboxes, users, etc.)
  - Action types (read, write, delete, manage)

### 2.8 Settings & Configuration
**Route:** `/settings`

#### 2.8.1 DKIM Key Management
**Capabilities Required:** `domains:write`

**Features:**
- List DKIM keys per domain
- Columns: Domain, Selector, Algorithm, Key size, Active, Created date
- Create new DKIM key:
  - Domain selection
  - Selector
  - Algorithm (RSA, ED25519)
  - Key size (2048, 4096)
  - Auto-generate key pair
  - Display public key for DNS TXT record
- Delete DKIM key
- View DKIM key details (public key only, private key redacted)

#### 2.8.2 Certificate Management
**Capabilities Required:** `domains:write` (or dedicated cert capability)

**Features:**
- List certificates
- Columns: Domain, Issuer, Valid from, Valid until, Days remaining, Auto-renew
- Upload certificate:
  - Domain selection
  - Certificate PEM upload
  - Private key PEM upload (encrypted at rest)
  - Issuer (auto-detected)
  - Auto-renew toggle
- Certificate expiration warnings (< 30 days)
- Delete certificate

#### 2.8.3 IP Ban Management
**Capabilities Required:** `bans:read`, `bans:write`, `bans:delete`

**Features:**
- List IP bans
- Filter by:
  - Protocol (smtp/imap/pop3/all)
  - Active status (check expiration)
- Columns: IP address, Reason, Protocol, Created by, Expires at, Created date
- Add IP ban:
  - IP address (with validation)
  - Reason
  - Protocol selection
  - Expiration (optional, datetime picker)
- Remove ban (by ID or by IP address)
- Auto-expire bans (show expired bans in gray)

#### 2.8.4 TLS-RPT Reports
**Capabilities Required:** `domains:read`

**Features:**
- List TLS-RPT reports
- Filter by domain, date range
- Columns: Domain, Reporting org, Start date, End date, Total successful, Total failure, Received date
- View report details:
  - Policy type and domain
  - Failure details (JSONB) with chart
  - Raw report (JSON)

### 2.9 Logging & Monitoring
**Route:** `/logs`

#### 2.9.1 Activity Logs
**Capabilities Required:** `users:read` or admin-only

**Features:**
- List all admin actions
- Filter by:
  - Actor (admin user email/system)
  - Action (create, update, delete)
  - Resource type (domain, mailbox, user, etc.)
  - Date range
- Columns: Timestamp, Actor, Action, Resource type, Resource ID, Detail, IP address
- View metadata (JSONB) for complex changes
- Export logs to CSV

#### 2.9.2 Delivery Logs
**Capabilities Required:** `queue:read`

**Features:**
- List outbound delivery attempts
- Filter by:
  - Status (success/failure)
  - Sender email
  - Recipient email
  - Recipient domain
  - Date range
- Columns: Timestamp, Sender, Recipient, Status, SMTP code, Error message
- View full delivery attempt details

### 2.10 Webmail Account Management
**Route:** `/webmail-accounts`
**Capabilities Required:** `mailboxes:read`, `mailboxes:write`

**Features:**
- List webmail accounts
- Link primary mailbox to webmail account
- View linked accounts per webmail user
- Delete webmail account (soft delete)

---

## 3. RBAC Integration

### 3.1 Authentication Flow

1. **Login** (`POST /api/v1/auth/login`):
   - User enters username and password
   - API returns access token (JWT) and sets refresh token cookie
   - JWT contains `capabilities[]` array and `userType: "admin"`
   - Store access token in memory (React state)

2. **Token Refresh** (`POST /api/v1/auth/refresh`):
   - Automatically refresh access token when it expires (15 min)
   - Use refresh token from HTTP-only cookie
   - Update access token in memory

3. **Logout** (`POST /api/v1/auth/logout`):
   - Clear access token from memory
   - API invalidates refresh token
   - Redirect to login page

### 3.2 Capability-Based Access Control

**Implementation:**

```typescript
// lib/hooks/useCapabilities.ts
export function useCapabilities() {
  const { user } = useAuth();

  const hasCapability = (capability: string): boolean => {
    if (!user || user.userType !== 'admin') return false;
    return user.capabilities.includes('*') || user.capabilities.includes(capability);
  };

  const hasAnyCapability = (capabilities: string[]): boolean => {
    return capabilities.some(cap => hasCapability(cap));
  };

  return { hasCapability, hasAnyCapability, capabilities: user?.capabilities || [] };
}

// Usage in components:
function DomainList() {
  const { hasCapability } = useCapabilities();

  const canCreate = hasCapability('domains:write');
  const canDelete = hasCapability('domains:delete');

  return (
    <>
      {canCreate && <Button onClick={handleCreate}>New Domain</Button>}
      {/* ... */}
    </>
  );
}
```

**Route Protection:**

```typescript
// app/routes/__root.tsx
export default function Root() {
  const { user, isLoading } = useAuth();

  if (isLoading) return <LoadingSpinner />;

  if (!user) {
    // Redirect to login
    return <Navigate to="/login" />;
  }

  if (user.userType !== 'admin') {
    return <div>Access denied. Admin users only.</div>;
  }

  return <AppShell><Outlet /></AppShell>;
}
```

**Capability Matrix:**

| Feature | Required Capability | Fallback |
|---------|---------------------|----------|
| View domains | `domains:read` | Hidden |
| Create/edit domain | `domains:write` | Button disabled |
| Delete domain | `domains:delete` | Button hidden |
| View mailboxes | `mailboxes:read` | Hidden |
| Create/edit mailbox | `mailboxes:write` | Button disabled |
| Delete mailbox | `mailboxes:delete` | Button hidden |
| View queue | `queue:read` | Hidden |
| Manage queue (retry/bounce) | `queue:manage` | Actions disabled |
| View pipelines | `pipelines:read` | Hidden |
| Create/edit pipeline | `pipelines:write` | Button disabled |
| Delete pipeline | `pipelines:delete` | Button hidden |
| View admin users | `users:read` | Hidden |
| Create/edit admin user | `users:write` | Button disabled |
| Delete admin user | `users:delete` | Button hidden |
| Manage bans | `bans:write`, `bans:delete` | Button disabled |
| Send bulk messages | `messages:send_bulk` | Feature hidden |
| Read messages (mail-admin) | `messages:read` | Feature hidden |

**Wildcard Superadmin:**
- Users with `*` capability bypass all checks
- Superadmin role automatically grants `*`
- UI shows "Superadmin" badge in user dropdown

---

## 4. API Client Architecture

### 4.1 Axios Instance with Interceptors

```typescript
// lib/api/client.ts
import axios from 'axios';

const apiClient = axios.create({
  baseURL: import.meta.env.VITE_API_URL || 'http://localhost:3000/api/v1',
  withCredentials: true, // Send cookies for refresh token
});

// Request interceptor: Add access token
apiClient.interceptors.request.use((config) => {
  const token = getAccessToken(); // From memory/state
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

// Response interceptor: Handle 401 and refresh token
apiClient.interceptors.response.use(
  (response) => response,
  async (error) => {
    if (error.response?.status === 401 && !error.config._retry) {
      error.config._retry = true;

      try {
        // Refresh token
        const { data } = await axios.post('/api/v1/auth/refresh', {}, {
          withCredentials: true,
        });

        setAccessToken(data.access_token);
        error.config.headers.Authorization = `Bearer ${data.access_token}`;

        return apiClient(error.config);
      } catch (refreshError) {
        // Refresh failed, logout
        clearAuth();
        window.location.href = '/login';
        return Promise.reject(refreshError);
      }
    }

    return Promise.reject(error);
  }
);

export default apiClient;
```

### 4.2 API Function Modules

Each resource has its own API module:

```typescript
// lib/api/domains.ts
import apiClient from './client';
import type { Domain, CreateDomainDto, UpdateDomainDto } from '../types/api';

export const domainsApi = {
  list: async () => {
    const { data } = await apiClient.get<Domain[]>('/admin/domains');
    return data;
  },

  get: async (id: number) => {
    const { data } = await apiClient.get<Domain>(`/admin/domains/${id}`);
    return data;
  },

  create: async (domain: CreateDomainDto) => {
    const { data } = await apiClient.post<Domain>('/admin/domains', domain);
    return data;
  },

  update: async (id: number, domain: UpdateDomainDto) => {
    const { data } = await apiClient.patch<Domain>(`/admin/domains/${id}`, domain);
    return data;
  },

  delete: async (id: number) => {
    await apiClient.delete(`/admin/domains/${id}`);
  },

  checkDNS: async (id: number) => {
    const { data } = await apiClient.get(`/admin/domains/${id}/dns`);
    return data;
  },
};
```

### 4.3 TanStack Query Integration

```typescript
// Example usage with TanStack Query
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { domainsApi } from '../lib/api/domains';

function DomainList() {
  const queryClient = useQueryClient();

  // Fetch domains
  const { data: domains, isLoading } = useQuery({
    queryKey: ['domains'],
    queryFn: domainsApi.list,
  });

  // Delete domain mutation
  const deleteMutation = useMutation({
    mutationFn: domainsApi.delete,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['domains'] });
      toast.success('Domain deleted successfully');
    },
  });

  // ... component code
}
```

---

## 5. Component Architecture

### 5.1 Layout Components

**AppShell** - Main layout wrapper
- Sidebar (240px fixed width)
- Content area (flexible)
- Responsive (collapse sidebar on mobile)

**Sidebar** - Navigation menu
- Logo and app name
- Navigation items (with active state)
- User section at bottom (avatar, name, logout)
- Capability-based filtering (hide items user can't access)

**Header** - Page header
- Page title
- Breadcrumbs
- Action buttons (contextual)

### 5.2 UI Components (shadcn/ui)

Install and use the following shadcn/ui components:
- `button` - Primary, secondary, destructive variants
- `input` - Text inputs
- `label` - Form labels
- `select` - Dropdowns
- `table` - Data tables
- `dialog` - Modals
- `dropdown-menu` - Action menus
- `card` - Metric cards and containers
- `badge` - Status badges
- `form` - React Hook Form integration
- `toast` - Notifications
- `tabs` - Tab navigation
- `switch` - Toggle switches
- `alert` - Alerts and warnings
- `skeleton` - Loading states
- `pagination` - Table pagination
- `command` - Command palette (optional)

### 5.3 Domain-Specific Components

Each feature area has its own components:

```
components/
├── domains/
│   ├── DomainList.tsx          # Domain table
│   ├── DomainForm.tsx          # Create/edit form
│   ├── DomainDNS.tsx           # DNS check display
│   └── DomainAllowlist.tsx     # Allow/block list
├── mailboxes/
│   ├── MailboxList.tsx
│   ├── MailboxForm.tsx
│   └── MailboxQuota.tsx        # Quota visualization
├── queue/
│   ├── QueueList.tsx
│   ├── QueueStats.tsx
│   └── QueueActions.tsx        # Bulk actions
└── ... (other features)
```

---

## 6. Implementation Phases

### Phase 1: Foundation (Week 1)
- [x] Designs completed in Pencil
- [x] Set up TanStack Router project (React 19 + TanStack Router)
- [x] Configure Tailwind CSS v4 with Swiss Clean Design System
- [x] Create API client with versioned endpoints (apiV1)
- [x] Implement authentication (login, logout, refresh) - Login functional
- [x] Create AppShell layout with sidebar
- [ ] Implement RBAC hooks (`useCapabilities`) - **TODO: Backend needs capability validation**
- [x] Create route protection with router basepath

**Deliverables:**
- Login page (functional) ✓
- Protected dashboard route (basic layout) ✓
- Sidebar navigation (complete) ✓

**Tech Stack Differences:**
- Using **Zustand** for state management (not TanStack Query)
- Using **Swiss Clean Design System** (not shadcn/ui)
- Using **TanStack Router** (not TanStack Start)

### Phase 2: Dashboard & Domain Management (Week 2)
- [x] Implement dashboard overview:
  - [x] Metric cards (domains, mailboxes, queue, messages)
  - [ ] Message volume chart - **TODO: Add chart library**
  - [ ] Recent activity list - **TODO: Integrate activity logs API**
- [x] Domain management:
  - [x] Domain list with search/filter
  - [x] Create domain form
  - [x] Edit domain form (via $id route)
  - [x] Delete domain capability
  - [ ] DNS check display - **TODO: Implement DNS status component**

**Deliverables:**
- Functional dashboard with metric cards ✓
- Domain list UI complete ✓
- Domain stores implemented ✓

### Phase 3: Mailbox & Alias Management (Week 3)
- [x] Mailbox management:
  - [x] Mailbox list with filter by domain
  - [x] Create mailbox form
  - [x] Edit mailbox route ($id)
  - [x] Delete mailbox capability
  - [ ] Quota visualization - **TODO: Add quota charts**
- [ ] Alias management:
  - [ ] Alias list - **TODO: Not yet implemented**
  - [ ] Create/edit alias form
  - [ ] Delete alias

**Deliverables:**
- Mailbox list UI complete ✓
- Mailbox stores implemented ✓
- Aliases not yet started ⚠️

### Phase 4: Queue Management (Week 4)
- [x] Queue dashboard:
  - [x] Status overview cards
  - [x] Queue list with filters
  - [x] Queue entry details route ($id)
- [x] Queue actions:
  - [x] Retry single/bulk capability
  - [x] Bounce single/bulk capability
  - [x] Delete single/bulk capability

**Deliverables:**
- Queue list UI complete ✓
- Queue stores implemented ✓
- Queue actions need testing ⚠️

### Phase 5: Pipelines & Filters (Week 5)
- [ ] Pipeline list and management
- [ ] Pipeline editor (filter configuration)
- [ ] Custom filter creation (code editor)
- [ ] Filter testing interface
- [ ] Pipeline logs viewer

**Deliverables:**
- Complete pipeline and filter management
- Filter testing functional

### Phase 6: Admin Users & RBAC (Week 6)
- [x] Admin user list UI
- [x] Create/edit admin user routes
- [ ] Role management interface - **TODO: UI exists, API missing**
- [ ] Capability viewer - **TODO: UI planned, API missing**
- [ ] Role assignment - **TODO: API missing**
- [ ] Activity log viewer - **TODO: Not started**

**Deliverables:**
- Admin user list UI complete ✓
- Admin user stores implemented ✓
- **CRITICAL: Backend API endpoints missing** ❌

**Missing Backend Endpoints:**
- `GET /api/v1/admin/admin-users` - List admin users
- `POST /api/v1/admin/admin-users` - Create admin user
- `GET /api/v1/admin/admin-users/{id}` - Get admin user
- `PUT /api/v1/admin/admin-users/{id}` - Update admin user
- `DELETE /api/v1/admin/admin-users/{id}` - Delete admin user
- `GET /api/v1/admin/roles` - List roles
- `GET /api/v1/admin/capabilities` - List capabilities

### Phase 7: Settings & Configuration (Week 7)
- [ ] DKIM key management
- [ ] Certificate management (upload, expiration warnings)
- [ ] IP ban management
- [ ] TLS-RPT report viewer
- [ ] MTA-STS policy editor

**Deliverables:**
- All settings pages functional
- Certificate warnings working

### Phase 8: Polish & Testing (Week 8)
- [ ] Error handling improvements
- [ ] Loading states and skeletons
- [ ] Form validation (all forms)
- [ ] Toast notifications (success/error)
- [ ] Responsive design (mobile/tablet)
- [ ] Accessibility audit (WCAG AA)
- [ ] End-to-end testing (Playwright)
- [ ] Documentation (user guide)

**Deliverables:**
- Production-ready admin website
- Complete test coverage
- User documentation

---

## 7. Key Technical Decisions

### 7.1 Why TanStack Start?
- Full-stack React framework with file-based routing
- Built-in SSR/SSG capabilities
- TanStack Query and Router integration
- TypeScript-first
- Similar to webmail architecture (consistency)

### 7.2 Why shadcn/ui?
- Not a component library - copy components into your codebase
- Full control and customization
- Built on Radix UI (accessible)
- Styled with Tailwind CSS
- TypeScript support

### 7.3 State Management Strategy
- **Server State:** TanStack Query (automatic caching, refetching, optimistic updates)
- **Auth State:** React Context + localStorage for persistence
- **Form State:** React Hook Form (local component state)
- **UI State:** Component state (no global store needed)

### 7.4 Data Fetching Patterns
- Use TanStack Query for all API calls
- Query keys: `['resource', id?, filters?]`
- Automatic refetching on window focus
- Optimistic updates for mutations
- Error boundaries for API errors

---

## 8. Development Environment Setup

### 8.1 Prerequisites
- Node.js 20+
- pnpm (or npm/yarn)
- REST Mail API running locally or in dev environment

### 8.2 Environment Variables

```env
# .env.local
VITE_API_URL=http://localhost:3000/api/v1
VITE_API_BASE_URL=http://localhost:3000
```

### 8.3 Getting Started

```bash
# Create admin directory
mkdir admin
cd admin

# Initialize TanStack Start project
pnpm create @tanstack/start

# Install dependencies
pnpm add @tanstack/react-query axios react-hook-form zod @hookform/resolvers
pnpm add -D tailwindcss postcss autoprefixer

# Install shadcn/ui CLI
pnpm add -D shadcn-ui

# Initialize shadcn/ui
npx shadcn-ui@latest init

# Install components
npx shadcn-ui@latest add button input label table dialog dropdown-menu card badge form toast tabs switch alert

# Start dev server
pnpm dev
```

---

## 9. Deployment Strategy

### 9.1 Build Output
- TanStack Start builds to static files (SSG) or server-side (SSR)
- For admin app, use SSG for simplicity
- Output: `dist/` directory with HTML, CSS, JS

### 9.2 Hosting Options
1. **Same Server as API:**
   - Serve `admin/dist/` from Express static middleware
   - Route: `/admin` → admin app
   - Route: `/api` → REST API

2. **Separate Static Hosting:**
   - Deploy to Vercel, Netlify, or Cloudflare Pages
   - Configure CORS on API to allow admin domain
   - Use environment variable for API URL

3. **Docker Container:**
   - Nginx serving static files
   - Docker Compose with API container

### 9.3 Nginx Configuration (Example)

```nginx
server {
  listen 80;
  server_name admin.restmail.test;

  root /var/www/admin/dist;
  index index.html;

  # SPA routing (fallback to index.html)
  location / {
    try_files $uri $uri/ /index.html;
  }

  # Proxy API requests
  location /api {
    proxy_pass http://localhost:3000;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection 'upgrade';
    proxy_set_header Host $host;
    proxy_cache_bypass $http_upgrade;
  }
}
```

---

## 10. Security Considerations

### 10.1 Authentication Security
- **JWT Access Token:** Short-lived (15 min), stored in memory
- **Refresh Token:** Long-lived (7 days), HTTP-only cookie (secure, SameSite=Strict)
- **CSRF Protection:** SameSite cookies + custom header
- **XSS Protection:** Content Security Policy headers

### 10.2 Authorization
- Check capabilities on every protected action
- Server-side validation (API enforces RBAC)
- Client-side checks are for UX only (hide/disable buttons)

### 10.3 Input Validation
- Use Zod schemas for all forms
- Sanitize user input before API calls
- Display validation errors clearly

### 10.4 API Security
- HTTPS in production
- CORS configured to allow only admin domain
- Rate limiting on API (handled by API server)

---

## 11. Monitoring & Analytics

### 11.1 Error Tracking
- Integrate Sentry (or similar) for error reporting
- Track API errors, rendering errors, network failures

### 11.2 Analytics
- Optional: Google Analytics or Plausible
- Track page views, user actions (create domain, delete mailbox, etc.)

### 11.3 Performance Monitoring
- Web Vitals (LCP, FID, CLS)
- API response time tracking
- TanStack Query DevTools (dev mode)

---

## 12. Testing Strategy

### 12.1 Unit Tests
- Vitest for unit tests
- Test utility functions (formatting, validation)
- Test React hooks (useCapabilities, useAuth)

### 12.2 Integration Tests
- React Testing Library for component tests
- Mock API calls with MSW (Mock Service Worker)
- Test user interactions (form submission, navigation)

### 12.3 End-to-End Tests
- Playwright for E2E tests
- Test critical user flows:
  - Login → Create domain → Create mailbox
  - Queue management (retry, bounce)
  - Admin user creation → role assignment

---

## 13. Success Metrics

### 13.1 Functionality
- [ ] All REST API endpoints accessible via UI
- [ ] RBAC working correctly (capability-based access)
- [ ] Real-time data updates (queue status, metrics)
- [ ] Error handling (user-friendly messages)

### 13.2 Performance
- [ ] First Contentful Paint < 1.5s
- [ ] Time to Interactive < 3s
- [ ] API calls < 500ms (avg)
- [ ] No layout shifts (CLS = 0)

### 13.3 User Experience
- [ ] Responsive on all screen sizes
- [ ] Accessible (keyboard navigation, screen readers)
- [ ] Clear visual hierarchy
- [ ] Consistent design language

---

## Appendix A: API Endpoint Reference

See the comprehensive API exploration report for the complete list of 200+ endpoints organized by category:
- Authentication
- Domains
- Mailboxes
- Aliases
- Queue
- Pipelines
- Admin Users
- Settings (DKIM, Certificates, Bans)
- Logs

---

## Appendix B: Capability Reference

See RBAC exploration report for the complete capability matrix:
- `*` - Wildcard (superadmin)
- `domains:read`, `domains:write`, `domains:delete`
- `mailboxes:read`, `mailboxes:write`, `mailboxes:delete`
- `users:read`, `users:write`, `users:delete`
- `pipelines:read`, `pipelines:write`, `pipelines:delete`
- `queue:read`, `queue:manage`
- `bans:read`, `bans:write`, `bans:delete`
- `messages:read`, `messages:send_bulk`

---

## Next Steps

1. **Review this plan** with stakeholders
2. **Set up the admin project** (Phase 1)
3. **Begin implementation** following the 8-week schedule
4. **Iterate based on feedback** from early testing

---

**Plan Created:** 2026-02-22
**Last Updated:** 2026-02-23
**Status:** Phase 1-4 partially complete, Phase 6 blocked
**Current Progress:** ~60% complete (frontend), backend gaps identified
**Tech Stack:** React 19 + TanStack Router + Zustand + Tailwind CSS v4 + Swiss Clean Design System

## Current Implementation Status (2026-02-23)

### ✅ Completed
- Admin website structure with TanStack Router
- Authentication flow (login, JWT token handling)
- API client with environment variable configuration
- Dashboard with metric cards
- Domain management UI and stores
- Mailbox management UI and stores
- Queue management UI and stores
- Admin users UI and stores (frontend only)
- Layout components (AppShell, Sidebar, Header)
- Router basepath configuration for `/admin` deployment
- Autofill styling fixes

### ⚠️ In Progress / Issues
- RBAC capability validation needs backend integration
- Admin users API endpoints **completely missing from backend**
- Charts and visualizations not yet added
- Quota visualization components needed

### ❌ Not Started
- Alias management
- Pipeline & filter management (Phase 5)
- Settings pages (DKIM, certificates, bans) (Phase 7)
- Activity logs viewer
- Delivery logs viewer
- Testing and polish (Phase 8)

### 🚨 Immediate Action Required
**Backend API Implementation Needed:**
The admin-users management feature is blocked because the Go backend is missing all admin user CRUD endpoints. The frontend is complete and ready, but returns 404 errors when trying to access `/api/v1/admin/admin-users`.

**Required Handler:** Create `AdminUserHandler` in `internal/api/handlers/` with methods for:
- ListAdminUsers
- GetAdminUser
- CreateAdminUser
- UpdateAdminUser
- DeleteAdminUser
- ListRoles
- ListCapabilities

These handlers should be added to the admin routes group in `internal/api/routes.go` (line 246+).
