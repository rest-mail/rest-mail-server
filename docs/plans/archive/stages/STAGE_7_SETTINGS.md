# Stage 7: Settings & Configuration - Detailed Implementation Plan

**Status:** NOT STARTED
**Priority:** HIGH
**Estimated Effort:** 4-5 days
**Dependencies:** Backend APIs exist and functional

---

## Overview

Implement comprehensive settings and configuration management interface for system-level settings. This stage covers DKIM key management, TLS certificate management, IP ban management, TLS-RPT report viewing, and MTA-STS policy editing.

**Current State:**
- Backend APIs exist and are functional
- Settings index route exists with basic layout
- No subsection pages implemented
- No Zustand stores for settings entities

**Backend APIs Available:**
- DKIM: `/api/v1/admin/dkim` (GET, PUT /{id}, DELETE /{id})
- Certificates: `/api/v1/admin/certificates` (GET, POST, GET /{id}, DELETE /{id})
- Bans: `/api/v1/admin/bans` (GET, POST, DELETE /{id}, DELETE /ip/{ip})
- TLS Reports: `/api/v1/admin/tls-reports` (GET with filters)
- MTA-STS: `/api/v1/admin/domains/{id}/mta-sts` (GET, PUT, DELETE)

---

## Architecture

### Route Structure

```
/settings
├── /settings/index.tsx          # Settings overview with navigation cards
├── /settings/dkim.tsx            # DKIM key management
├── /settings/certificates.tsx   # Certificate management
├── /settings/bans.tsx            # IP ban management
├── /settings/tls-reports.tsx    # TLS-RPT report viewer
└── /settings/mta-sts.tsx         # MTA-STS policy editor
```

### Zustand Stores

Create the following stores in `/admin/src/lib/stores/`:

1. **dkimStore.ts** - DKIM key management state
2. **certificateStore.ts** - Certificate management state
3. **banStore.ts** - IP ban management state
4. **tlsReportStore.ts** - TLS-RPT reports state
5. **mtastsStore.ts** - MTA-STS policy state

---

## 1. DKIM Key Management

### 1.1 Route: `/settings/dkim`

**Purpose:** Manage DKIM signing keys for domains

**Features:**
- List all DKIM configurations by domain
- View public key for DNS record creation
- Create/update DKIM key for a domain
- Delete DKIM key from a domain

### 1.2 API Endpoints

**Backend Handler:** `internal/api/handlers/dkim.go` (DKIMHandler)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/admin/dkim` | List DKIM keys (private keys redacted) |
| PUT | `/api/v1/admin/dkim/{id}` | Set/update DKIM key for domain |
| DELETE | `/api/v1/admin/dkim/{id}` | Remove DKIM key from domain |

**Request/Response Types:**

```typescript
// GET /api/v1/admin/dkim response
interface DkimEntry {
  domain_id: number
  domain: string
  selector: string
  has_key: boolean
}

// PUT /api/v1/admin/dkim/{id} request
interface SetDkimKeyRequest {
  selector: string      // e.g., "mail"
  private_key: string   // PEM-encoded RSA private key
}

// PUT /api/v1/admin/dkim/{id} response
interface SetDkimKeyResponse {
  domain_id: number
  domain: string
  selector: string
  has_key: boolean
}
```

### 1.3 Zustand Store: `dkimStore.ts`

```typescript
import { create } from 'zustand'
import { apiV1 } from '../api'

interface DkimEntry {
  domain_id: number
  domain: string
  selector: string
  has_key: boolean
}

interface DkimState {
  entries: DkimEntry[]
  currentEntry: DkimEntry | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchDkimKeys: (accessToken: string) => Promise<void>
  setDkimKey: (domainId: number, data: { selector: string; private_key: string }, accessToken: string) => Promise<void>
  deleteDkimKey: (domainId: number, accessToken: string) => Promise<void>
  clearError: () => void
}

export const useDkimStore = create<DkimState>((set) => ({
  entries: [],
  currentEntry: null,
  isLoading: false,
  error: null,

  fetchDkimKeys: async (accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request('/admin/dkim', { method: 'GET' }, accessToken)
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch DKIM keys')
      }
      const data = await response.json()
      set({ entries: data.items || data, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch DKIM keys',
        isLoading: false,
      })
      throw error
    }
  },

  setDkimKey: async (domainId: number, data: { selector: string; private_key: string }, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/dkim/${domainId}`,
        {
          method: 'PUT',
          body: JSON.stringify(data),
          headers: { 'Content-Type': 'application/json' },
        },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to set DKIM key')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to set DKIM key',
        isLoading: false,
      })
      throw error
    }
  },

  deleteDkimKey: async (domainId: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/dkim/${domainId}`,
        { method: 'DELETE' },
        accessToken
      )
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete DKIM key')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete DKIM key',
        isLoading: false,
      })
      throw error
    }
  },

  clearError: () => set({ error: null }),
}))
```

### 1.4 Component: `/settings/dkim.tsx`

**Layout:**

```
+----------------------------------------------------------+
| Settings > DKIM Key Management                            |
+----------------------------------------------------------+
| [New DKIM Key]                                            |
+----------------------------------------------------------+
| Domain              | Selector | Has Key | Actions        |
+----------------------------------------------------------+
| example.com         | mail     | Yes     | [View] [Del]   |
| test.example.com    | mail     | Yes     | [View] [Del]   |
| noreply.example.com | -        | No      | [Create]       |
+----------------------------------------------------------+
```

**Features:**
- Table listing all domains with DKIM configuration
- "Create" button for domains without DKIM keys
- "View Public Key" dialog showing DNS TXT record to create
- "Delete" button with confirmation modal
- Form for creating/updating DKIM key:
  - Domain selection (dropdown of domains)
  - Selector input (default: "mail")
  - Generate key button (client-side RSA key generation)
  - Manual key paste textarea
  - Submit button

**Key Generation (Client-Side):**

Use the Web Crypto API or a library like `node-forge` to generate RSA keys in the browser:

```typescript
async function generateDkimKey(): Promise<{ publicKey: string; privateKey: string }> {
  // Use Web Crypto API or node-forge to generate 2048-bit RSA key pair
  // Export as PEM format
  // Return both public (for DNS) and private (for API submission)
}
```

**Public Key Display:**

When viewing a DKIM key, fetch the domain details from the domain store and parse the public key for display:

```
DNS TXT Record:
mail._domainkey.example.com IN TXT "v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3DQEBAQUAA4..."
```

---

## 2. Certificate Management

### 2.1 Route: `/settings/certificates`

**Purpose:** Manage TLS certificates for mail server domains

**Features:**
- List all certificates with expiration dates
- Upload new certificate (PEM format)
- View certificate details (issuer, validity dates)
- Delete certificate
- Warning system for certificates expiring within 30 days

### 2.2 API Endpoints

**Backend Handler:** `internal/api/handlers/certificates.go` (CertificateHandler)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/admin/certificates` | List all certificates |
| GET | `/api/v1/admin/certificates/{id}` | Get certificate details (includes cert_pem) |
| POST | `/api/v1/admin/certificates` | Upload new certificate |
| DELETE | `/api/v1/admin/certificates/{id}` | Delete certificate |

**Request/Response Types:**

```typescript
// GET /api/v1/admin/certificates response
interface Certificate {
  id: number
  domain_id: number
  issuer: string
  not_before: string      // ISO 8601 date
  not_after: string       // ISO 8601 date
  auto_renew: boolean
  created_at: string
  updated_at: string
  domain?: {
    id: number
    name: string
  }
  // Note: cert_pem and key_pem are omitted in list view
}

// POST /api/v1/admin/certificates request
interface CreateCertificateRequest {
  domain_id: number
  cert_pem: string        // PEM-encoded certificate
  key_pem: string         // PEM-encoded private key
  auto_renew?: boolean    // Default: true
}

// GET /api/v1/admin/certificates/{id} response includes cert_pem
interface CertificateDetail extends Certificate {
  cert_pem: string        // key_pem is never returned
}
```

### 2.3 Zustand Store: `certificateStore.ts`

```typescript
import { create } from 'zustand'
import { apiV1 } from '../api'

interface Certificate {
  id: number
  domain_id: number
  issuer: string
  not_before: string
  not_after: string
  auto_renew: boolean
  created_at: string
  updated_at: string
  domain?: {
    id: number
    name: string
  }
}

interface CertificateState {
  certificates: Certificate[]
  currentCertificate: Certificate | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchCertificates: (accessToken: string) => Promise<void>
  fetchCertificate: (id: number, accessToken: string) => Promise<void>
  uploadCertificate: (data: { domain_id: number; cert_pem: string; key_pem: string; auto_renew?: boolean }, accessToken: string) => Promise<void>
  deleteCertificate: (id: number, accessToken: string) => Promise<void>
  getExpiringCertificates: (days?: number) => Certificate[]
  clearError: () => void
}

export const useCertificateStore = create<CertificateState>((set, get) => ({
  certificates: [],
  currentCertificate: null,
  isLoading: false,
  error: null,

  fetchCertificates: async (accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request('/admin/certificates', { method: 'GET' }, accessToken)
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch certificates')
      }
      const data = await response.json()
      set({ certificates: data.items || data, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch certificates',
        isLoading: false,
      })
      throw error
    }
  },

  fetchCertificate: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(`/admin/certificates/${id}`, { method: 'GET' }, accessToken)
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch certificate')
      }
      const data = await response.json()
      set({ currentCertificate: data, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch certificate',
        isLoading: false,
      })
      throw error
    }
  },

  uploadCertificate: async (data, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        '/admin/certificates',
        {
          method: 'POST',
          body: JSON.stringify(data),
          headers: { 'Content-Type': 'application/json' },
        },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to upload certificate')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to upload certificate',
        isLoading: false,
      })
      throw error
    }
  },

  deleteCertificate: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(`/admin/certificates/${id}`, { method: 'DELETE' }, accessToken)
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete certificate')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete certificate',
        isLoading: false,
      })
      throw error
    }
  },

  getExpiringCertificates: (days = 30) => {
    const now = new Date()
    const threshold = new Date(now.getTime() + days * 24 * 60 * 60 * 1000)
    return get().certificates.filter((cert) => {
      const expiryDate = new Date(cert.not_after)
      return expiryDate <= threshold && expiryDate > now
    })
  },

  clearError: () => set({ error: null }),
}))
```

### 2.4 Component: `/settings/certificates.tsx`

**Layout:**

```
+-------------------------------------------------------------------------+
| Settings > Certificate Management                                       |
+-------------------------------------------------------------------------+
| ⚠️  3 certificates expiring in the next 30 days                         |
+-------------------------------------------------------------------------+
| [Upload Certificate]                                                    |
+-------------------------------------------------------------------------+
| Domain          | Issuer         | Valid Until  | Days Left | Actions  |
+-------------------------------------------------------------------------+
| example.com     | Let's Encrypt  | 2026-04-15   | 51       | [View] [Del] |
| ⚠️ test.com     | Let's Encrypt  | 2026-03-10   | 15       | [View] [Del] |
| mail.example.com| Cloudflare     | 2026-06-01   | 98       | [View] [Del] |
+-------------------------------------------------------------------------+
```

**Features:**
- Alert banner showing count of expiring certificates (< 30 days)
- Table with certificate details:
  - Domain name
  - Issuer
  - Valid until date
  - Days remaining (calculated client-side)
  - Warning icon for certificates < 30 days
- Upload form:
  - Domain selection dropdown
  - Certificate PEM file upload or textarea
  - Private key PEM file upload or textarea
  - Auto-renew toggle (default: true)
  - Submit button
- View certificate modal showing:
  - Full certificate details
  - PEM text (for cert only, not private key)
- Delete with confirmation modal

**Expiration Warning System:**

Add a helper function to calculate days until expiration:

```typescript
function getDaysUntilExpiry(notAfter: string): number {
  const now = new Date()
  const expiry = new Date(notAfter)
  const diff = expiry.getTime() - now.getTime()
  return Math.floor(diff / (1000 * 60 * 60 * 24))
}

function getExpiryStatus(days: number): 'expired' | 'critical' | 'warning' | 'ok' {
  if (days < 0) return 'expired'
  if (days <= 7) return 'critical'
  if (days <= 30) return 'warning'
  return 'ok'
}
```

Display color-coded indicators:
- Red: Expired or < 7 days
- Orange: 7-30 days
- Green: > 30 days

---

## 3. IP Ban Management

### 3.1 Route: `/settings/bans`

**Purpose:** Manage IP address bans for SMTP, IMAP, and POP3 protocols

**Features:**
- List all IP bans with filters
- Create new ban (with optional expiration)
- Delete ban by ID or IP address
- Show expired bans (grayed out)
- Filter by protocol and active status

### 3.2 API Endpoints

**Backend Handler:** `internal/api/handlers/ban.go` (BanHandler)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/admin/bans?protocol=&active=&limit=&offset=` | List bans with filters |
| POST | `/api/v1/admin/bans` | Create or update ban (upserts by IP) |
| DELETE | `/api/v1/admin/bans/{id}` | Delete ban by ID |
| DELETE | `/api/v1/admin/bans/ip/{ip}` | Delete ban by IP address |

**Request/Response Types:**

```typescript
// GET /api/v1/admin/bans response
interface Ban {
  id: number
  ip: string
  reason: string
  protocol: 'smtp' | 'imap' | 'pop3' | 'all'
  created_by: string
  expires_at: string | null    // ISO 8601 date, null = permanent
  created_at: string
  updated_at: string
}

// POST /api/v1/admin/bans request
interface CreateBanRequest {
  ip: string
  reason: string
  protocol: 'smtp' | 'imap' | 'pop3' | 'all'
  duration?: string            // e.g., "24h", "168h"; omit for permanent
  created_by: string
}
```

**Query Parameters:**
- `protocol`: Filter by protocol (smtp, imap, pop3, all)
- `active`: Filter by active status (default: true, set to "false" to include expired)
- `ip`: Filter by specific IP address
- `limit`: Results per page (default: 50, max: 200)
- `offset`: Pagination offset

### 3.3 Zustand Store: `banStore.ts`

```typescript
import { create } from 'zustand'
import { apiV1 } from '../api'

interface Ban {
  id: number
  ip: string
  reason: string
  protocol: 'smtp' | 'imap' | 'pop3' | 'all'
  created_by: string
  expires_at: string | null
  created_at: string
  updated_at: string
}

interface BanState {
  bans: Ban[]
  isLoading: boolean
  error: string | null
  pagination: {
    total: number
    hasMore: boolean
  }

  // Actions
  fetchBans: (filters: { protocol?: string; active?: boolean; limit?: number; offset?: number }, accessToken: string) => Promise<void>
  createBan: (data: { ip: string; reason: string; protocol: string; duration?: string; created_by: string }, accessToken: string) => Promise<void>
  deleteBan: (id: number, accessToken: string) => Promise<void>
  deleteBanByIP: (ip: string, accessToken: string) => Promise<void>
  isExpired: (ban: Ban) => boolean
  clearError: () => void
}

export const useBanStore = create<BanState>((set, get) => ({
  bans: [],
  isLoading: false,
  error: null,
  pagination: { total: 0, hasMore: false },

  fetchBans: async (filters, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const params = new URLSearchParams()
      if (filters.protocol) params.set('protocol', filters.protocol)
      if (filters.active !== undefined) params.set('active', String(filters.active))
      if (filters.limit) params.set('limit', String(filters.limit))
      if (filters.offset) params.set('offset', String(filters.offset))

      const response = await apiV1.request(
        `/admin/bans?${params.toString()}`,
        { method: 'GET' },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch bans')
      }
      const data = await response.json()
      set({
        bans: data.items || data,
        pagination: {
          total: data.pagination?.total || 0,
          hasMore: data.pagination?.has_more || false,
        },
        isLoading: false,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch bans',
        isLoading: false,
      })
      throw error
    }
  },

  createBan: async (data, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        '/admin/bans',
        {
          method: 'POST',
          body: JSON.stringify(data),
          headers: { 'Content-Type': 'application/json' },
        },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to create ban')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to create ban',
        isLoading: false,
      })
      throw error
    }
  },

  deleteBan: async (id: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(`/admin/bans/${id}`, { method: 'DELETE' }, accessToken)
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete ban')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete ban',
        isLoading: false,
      })
      throw error
    }
  },

  deleteBanByIP: async (ip: string, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(`/admin/bans/ip/${ip}`, { method: 'DELETE' }, accessToken)
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to unban IP')
      }
      set({ isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to unban IP',
        isLoading: false,
      })
      throw error
    }
  },

  isExpired: (ban: Ban) => {
    if (!ban.expires_at) return false
    return new Date(ban.expires_at) < new Date()
  },

  clearError: () => set({ error: null }),
}))
```

### 3.4 Component: `/settings/bans.tsx`

**Layout:**

```
+------------------------------------------------------------------------------+
| Settings > IP Ban Management                                                 |
+------------------------------------------------------------------------------+
| [Add Ban]  Protocol: [All ▼]  Status: [Active Only ▼]                       |
+------------------------------------------------------------------------------+
| IP Address      | Reason          | Protocol | Expires      | Actions        |
+------------------------------------------------------------------------------+
| 192.168.1.100   | Spam attempts   | smtp     | 2026-03-01   | [Remove]       |
| 10.0.0.50       | Brute force     | all      | Permanent    | [Remove]       |
| 172.16.0.200    | Malicious scan  | smtp     | Expired      | [Remove]       |
+------------------------------------------------------------------------------+
```

**Features:**
- Filter dropdowns:
  - Protocol: All, SMTP, IMAP, POP3
  - Status: Active only, Include expired
- Table columns:
  - IP address
  - Reason
  - Protocol
  - Expires at (or "Permanent")
  - Remove button
- Expired bans shown in gray text
- Add ban form:
  - IP address input (with validation)
  - Reason textarea
  - Protocol dropdown
  - Duration input (e.g., "24h", "7d") or "Permanent" checkbox
  - Submit button
- Delete confirmation modal

**Duration Helper:**

```typescript
function parseDuration(input: string): string | undefined {
  // Convert user-friendly input to Go duration format
  // "24 hours" -> "24h"
  // "7 days" -> "168h"
  // Empty or "permanent" -> undefined
  const match = input.match(/^(\d+)\s*(h|hour|hours|d|day|days)$/i)
  if (!match) return undefined

  const value = parseInt(match[1])
  const unit = match[2].toLowerCase()

  if (unit.startsWith('h')) {
    return `${value}h`
  } else if (unit.startsWith('d')) {
    return `${value * 24}h`
  }
  return undefined
}
```

---

## 4. TLS-RPT Report Viewer

### 4.1 Route: `/settings/tls-reports`

**Purpose:** View TLS-RPT (TLS Reporting) reports received from external MTAs

**Features:**
- List TLS-RPT reports with filters
- View report details including failure breakdown
- Filter by domain and reporting organization
- Sort by received date

### 4.2 API Endpoints

**Backend Handler:** `internal/api/handlers/tlsrpt.go` (TLSReportHandler)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/admin/tls-reports?domain_id=&policy_type=&reporting_org=&limit=&offset=` | List reports |

**Note:** TLS-RPT reports are received via `POST /.well-known/smtp-tlsrpt` (public endpoint), not directly created by admins.

**Request/Response Types:**

```typescript
// GET /api/v1/admin/tls-reports response
interface TLSReport {
  id: number
  domain_id: number
  reporting_org: string
  start_date: string          // ISO 8601
  end_date: string            // ISO 8601
  policy_type: 'sts' | 'tlsa' | 'no-policy'
  policy_domain: string
  total_successful: number
  total_failure: number
  failure_details: any        // JSONB field with failure reasons
  raw_report: string          // Full JSON report as string
  received_at: string         // ISO 8601
}
```

**Query Parameters:**
- `domain_id`: Filter by domain
- `policy_type`: Filter by policy type (sts, tlsa, no-policy)
- `reporting_org`: Filter by reporting organization (ILIKE search)
- `limit`: Results per page (default: 50, max: 200)
- `offset`: Pagination offset

### 4.3 Zustand Store: `tlsReportStore.ts`

```typescript
import { create } from 'zustand'
import { apiV1 } from '../api'

interface TLSReport {
  id: number
  domain_id: number
  reporting_org: string
  start_date: string
  end_date: string
  policy_type: 'sts' | 'tlsa' | 'no-policy'
  policy_domain: string
  total_successful: number
  total_failure: number
  failure_details: any
  raw_report: string
  received_at: string
}

interface TLSReportState {
  reports: TLSReport[]
  currentReport: TLSReport | null
  isLoading: boolean
  error: string | null
  pagination: {
    total: number
    hasMore: boolean
  }

  // Actions
  fetchReports: (filters: { domain_id?: number; policy_type?: string; reporting_org?: string; limit?: number; offset?: number }, accessToken: string) => Promise<void>
  selectReport: (report: TLSReport) => void
  clearCurrentReport: () => void
  clearError: () => void
}

export const useTLSReportStore = create<TLSReportState>((set) => ({
  reports: [],
  currentReport: null,
  isLoading: false,
  error: null,
  pagination: { total: 0, hasMore: false },

  fetchReports: async (filters, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const params = new URLSearchParams()
      if (filters.domain_id) params.set('domain_id', String(filters.domain_id))
      if (filters.policy_type) params.set('policy_type', filters.policy_type)
      if (filters.reporting_org) params.set('reporting_org', filters.reporting_org)
      if (filters.limit) params.set('limit', String(filters.limit))
      if (filters.offset) params.set('offset', String(filters.offset))

      const response = await apiV1.request(
        `/admin/tls-reports?${params.toString()}`,
        { method: 'GET' },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch TLS reports')
      }
      const data = await response.json()
      set({
        reports: data.items || data,
        pagination: {
          total: data.pagination?.total || 0,
          hasMore: data.pagination?.has_more || false,
        },
        isLoading: false,
      })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch TLS reports',
        isLoading: false,
      })
      throw error
    }
  },

  selectReport: (report: TLSReport) => set({ currentReport: report }),
  clearCurrentReport: () => set({ currentReport: null }),
  clearError: () => set({ error: null }),
}))
```

### 4.4 Component: `/settings/tls-reports.tsx`

**Layout:**

```
+------------------------------------------------------------------------------+
| Settings > TLS-RPT Reports                                                   |
+------------------------------------------------------------------------------+
| Domain: [All ▼]  Reporting Org: [______]  [Filter]                          |
+------------------------------------------------------------------------------+
| Domain        | Reporting Org | Period          | Success | Fail | Actions  |
+------------------------------------------------------------------------------+
| example.com   | Gmail         | 2026-02-15 to   | 15,234  | 3    | [View]   |
|               |               | 2026-02-22      |         |      |          |
| test.com      | Outlook       | 2026-02-15 to   | 8,521   | 0    | [View]   |
|               |               | 2026-02-22      |         |      |          |
+------------------------------------------------------------------------------+
```

**Features:**
- Filter controls:
  - Domain dropdown (from domain store)
  - Reporting organization text search
- Table columns:
  - Domain
  - Reporting organization
  - Report period (start to end dates)
  - Total successful sessions
  - Total failure sessions
  - View button
- Report detail modal:
  - Full report metadata
  - Success/failure breakdown chart
  - Failure details table (if any)
  - Raw JSON view (collapsible)

**Failure Details Display:**

Parse the `failure_details` JSONB field and display in a table:

```typescript
interface FailureDetail {
  result_type: string
  sending_mta_ip: string
  receiving_mx_hostname: string
  receiving_mx_helo: string
  receiving_ip: string
  failed_session_count: number
  additional_information?: string
}
```

Display as:
```
+-------------------------------------------------------------------+
| Failure Type      | MX Host         | IP            | Count     |
+-------------------------------------------------------------------+
| certificate-not-  | mx1.example.com | 192.168.1.10  | 2         |
| trusted           |                 |               |           |
| starttls-not-     | mx2.example.com | 192.168.1.11  | 1         |
| supported         |                 |               |           |
+-------------------------------------------------------------------+
```

---

## 5. MTA-STS Policy Editor

### 5.1 Route: `/settings/mta-sts`

**Purpose:** Manage MTA-STS (Mail Transfer Agent Strict Transport Security) policies per domain

**Features:**
- List MTA-STS policies for all domains
- Create/update policy for a domain
- Set policy mode (none, testing, enforce)
- Configure MX hosts
- Set max_age
- Delete policy

### 5.2 API Endpoints

**Backend Handler:** `internal/api/handlers/mtasts.go` (MTASTSHandler)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/admin/domains/{id}/mta-sts` | Get MTA-STS policy for domain |
| PUT | `/api/v1/admin/domains/{id}/mta-sts` | Create/update MTA-STS policy |
| DELETE | `/api/v1/admin/domains/{id}/mta-sts` | Delete MTA-STS policy |

**Request/Response Types:**

```typescript
// GET /api/v1/admin/domains/{id}/mta-sts response
interface MTASTSPolicy {
  id: number
  domain_id: number
  mode: 'none' | 'testing' | 'enforce'
  mx_hosts: string              // Comma-separated list
  max_age: number               // Seconds
  active: boolean
  created_at: string
  updated_at: string
}

// PUT /api/v1/admin/domains/{id}/mta-sts request
interface SetMTASTSPolicyRequest {
  mode: 'none' | 'testing' | 'enforce'
  mx_hosts: string              // Comma-separated: "mx1.example.com,*.example.com"
  max_age?: number              // Default: 604800 (7 days)
  active?: boolean              // Default: true
}
```

### 5.3 Zustand Store: `mtastsStore.ts`

```typescript
import { create } from 'zustand'
import { apiV1 } from '../api'

interface MTASTSPolicy {
  id: number
  domain_id: number
  mode: 'none' | 'testing' | 'enforce'
  mx_hosts: string
  max_age: number
  active: boolean
  created_at: string
  updated_at: string
}

interface MTASTSState {
  policies: Map<number, MTASTSPolicy>  // keyed by domain_id
  currentPolicy: MTASTSPolicy | null
  isLoading: boolean
  error: string | null

  // Actions
  fetchPolicy: (domainId: number, accessToken: string) => Promise<void>
  setPolicy: (domainId: number, data: { mode: string; mx_hosts: string; max_age?: number; active?: boolean }, accessToken: string) => Promise<void>
  deletePolicy: (domainId: number, accessToken: string) => Promise<void>
  clearCurrentPolicy: () => void
  clearError: () => void
}

export const useMTASTSStore = create<MTASTSState>((set, get) => ({
  policies: new Map(),
  currentPolicy: null,
  isLoading: false,
  error: null,

  fetchPolicy: async (domainId: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/mta-sts`,
        { method: 'GET' },
        accessToken
      )
      if (!response.ok) {
        if (response.status === 404) {
          // No policy exists for this domain
          set({ currentPolicy: null, isLoading: false })
          return
        }
        const error = await response.json()
        throw new Error(error.error || 'Failed to fetch MTA-STS policy')
      }
      const data = await response.json()
      const policies = new Map(get().policies)
      policies.set(domainId, data)
      set({ policies, currentPolicy: data, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to fetch MTA-STS policy',
        isLoading: false,
      })
      throw error
    }
  },

  setPolicy: async (domainId: number, data, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/mta-sts`,
        {
          method: 'PUT',
          body: JSON.stringify(data),
          headers: { 'Content-Type': 'application/json' },
        },
        accessToken
      )
      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to set MTA-STS policy')
      }
      const result = await response.json()
      const policies = new Map(get().policies)
      policies.set(domainId, result)
      set({ policies, currentPolicy: result, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to set MTA-STS policy',
        isLoading: false,
      })
      throw error
    }
  },

  deletePolicy: async (domainId: number, accessToken: string) => {
    set({ isLoading: true, error: null })
    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/mta-sts`,
        { method: 'DELETE' },
        accessToken
      )
      if (!response.ok && response.status !== 204) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to delete MTA-STS policy')
      }
      const policies = new Map(get().policies)
      policies.delete(domainId)
      set({ policies, currentPolicy: null, isLoading: false })
    } catch (error) {
      set({
        error: error instanceof Error ? error.message : 'Failed to delete MTA-STS policy',
        isLoading: false,
      })
      throw error
    }
  },

  clearCurrentPolicy: () => set({ currentPolicy: null }),
  clearError: () => set({ error: null }),
}))
```

### 5.4 Component: `/settings/mta-sts.tsx`

**Layout:**

```
+------------------------------------------------------------------------------+
| Settings > MTA-STS Policy Management                                         |
+------------------------------------------------------------------------------+
| Domain: [example.com ▼]  [Load Policy]                                      |
+------------------------------------------------------------------------------+
| Policy Configuration for example.com                                         |
|                                                                              |
| Mode:          ( ) None  ( ) Testing  (•) Enforce                            |
| MX Hosts:      [mx1.example.com, *.example.com                           ]  |
| Max Age:       [604800] seconds (7 days)                                     |
| Active:        [✓] Active                                                    |
|                                                                              |
| [Save Policy]  [Delete Policy]                                               |
+------------------------------------------------------------------------------+
| Generated Policy File Preview:                                               |
| +--------------------------------------------------------------------------+ |
| | version: STSv1                                                           | |
| | mode: enforce                                                            | |
| | mx: mx1.example.com                                                      | |
| | mx: *.example.com                                                        | |
| | max_age: 604800                                                          | |
| +--------------------------------------------------------------------------+ |
+------------------------------------------------------------------------------+
```

**Features:**
- Domain selector (dropdown from domain store)
- Load policy button (fetches existing policy or shows blank form)
- Form fields:
  - Mode radio buttons (none, testing, enforce)
  - MX hosts textarea (comma-separated or one per line)
  - Max age input (seconds) with helper text showing days
  - Active toggle
- Save button (creates or updates policy)
- Delete button (only shown if policy exists)
- Policy preview showing the generated `.well-known/mta-sts.txt` content

**Max Age Helper:**

```typescript
function secondsToDays(seconds: number): number {
  return Math.floor(seconds / 86400)
}

function daysToSeconds(days: number): number {
  return days * 86400
}
```

Provide preset buttons:
- 1 day (86400)
- 7 days (604800) - recommended
- 30 days (2592000)

**MX Hosts Parsing:**

Accept both formats:
- Comma-separated: `mx1.example.com, mx2.example.com, *.example.com`
- Line-separated:
  ```
  mx1.example.com
  mx2.example.com
  *.example.com
  ```

Convert to comma-separated string for API submission.

**Policy Preview:**

Generate and display the actual policy text that will be served at `https://mta-sts.{domain}/.well-known/mta-sts.txt`:

```typescript
function generatePolicyPreview(policy: MTASTSPolicy): string {
  const mxHosts = policy.mx_hosts.split(',').map(h => h.trim()).filter(Boolean)
  const lines = [
    'version: STSv1',
    `mode: ${policy.mode}`,
    ...mxHosts.map(mx => `mx: ${mx}`),
    `max_age: ${policy.max_age}`,
  ]
  return lines.join('\n')
}
```

---

## 6. Settings Index Page

### 6.1 Route: `/settings/index.tsx`

**Purpose:** Overview page with navigation cards to all settings sections

**Layout:**

```
+------------------------------------------------------------------------------+
| Settings & Configuration                                                     |
+------------------------------------------------------------------------------+
| +---------------------------+  +---------------------------+                 |
| | DKIM Key Management       |  | Certificate Management    |                 |
| |                           |  |                           |                 |
| | Manage DKIM signing keys  |  | Manage TLS certificates   |                 |
| | for domains               |  | ⚠️ 3 expiring soon        |                 |
| |                           |  |                           |                 |
| | [Manage DKIM Keys]        |  | [Manage Certificates]     |                 |
| +---------------------------+  +---------------------------+                 |
|                                                                              |
| +---------------------------+  +---------------------------+                 |
| | IP Ban Management         |  | TLS-RPT Reports           |                 |
| |                           |  |                           |                 |
| | Block malicious IPs from  |  | View TLS reporting from   |                 |
| | connecting                |  | external MTAs             |                 |
| |                           |  |                           |                 |
| | [Manage Bans]             |  | [View Reports]            |                 |
| +---------------------------+  +---------------------------+                 |
|                                                                              |
| +---------------------------+                                                |
| | MTA-STS Policies          |                                                |
| |                           |                                                |
| | Configure mail transport  |                                                |
| | security policies         |                                                |
| |                           |                                                |
| | [Manage Policies]         |                                                |
| +---------------------------+                                                |
+------------------------------------------------------------------------------+
```

**Features:**
- Navigation cards for each settings section
- Warning badges on certificate card if any are expiring soon
- Brief description of each section
- Button to navigate to detail page

**Implementation:**

```typescript
export default function SettingsIndex() {
  const { certificates, getExpiringCertificates } = useCertificateStore()
  const expiringCount = getExpiringCertificates(30).length

  const sections = [
    {
      title: 'DKIM Key Management',
      description: 'Manage DKIM signing keys for domains',
      path: '/settings/dkim',
      icon: KeyIcon,
    },
    {
      title: 'Certificate Management',
      description: 'Manage TLS certificates',
      path: '/settings/certificates',
      icon: CertificateIcon,
      badge: expiringCount > 0 ? `${expiringCount} expiring soon` : undefined,
    },
    {
      title: 'IP Ban Management',
      description: 'Block malicious IPs from connecting',
      path: '/settings/bans',
      icon: ShieldIcon,
    },
    {
      title: 'TLS-RPT Reports',
      description: 'View TLS reporting from external MTAs',
      path: '/settings/tls-reports',
      icon: ReportIcon,
    },
    {
      title: 'MTA-STS Policies',
      description: 'Configure mail transport security policies',
      path: '/settings/mta-sts',
      icon: SecurityIcon,
    },
  ]

  return (
    <div className="container mx-auto p-6">
      <h1 className="text-3xl font-bold mb-6">Settings & Configuration</h1>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {sections.map((section) => (
          <Card key={section.path}>
            <CardHeader>
              <CardTitle>
                {section.title}
                {section.badge && (
                  <Badge variant="warning" className="ml-2">
                    {section.badge}
                  </Badge>
                )}
              </CardTitle>
            </CardHeader>
            <CardContent>
              <p className="text-muted-foreground mb-4">{section.description}</p>
              <Link to={section.path}>
                <Button>Manage</Button>
              </Link>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}
```

---

## 7. Implementation Checklist

### Phase 1: Setup (Day 1)
- [ ] Create all 5 Zustand stores (dkim, certificate, ban, tlsReport, mtasts)
- [ ] Create route files with basic layout
- [ ] Add TypeScript interfaces for all API types
- [ ] Update settings index with navigation cards

### Phase 2: DKIM Management (Day 1-2)
- [ ] Implement DKIM list table
- [ ] Create DKIM key form (with domain selector)
- [ ] Add client-side RSA key generation (or manual paste)
- [ ] Implement public key display dialog
- [ ] Add delete with confirmation
- [ ] Test CRUD operations

### Phase 3: Certificate Management (Day 2)
- [ ] Implement certificate list table
- [ ] Add expiration warning badge/banner
- [ ] Create certificate upload form (file upload + textarea fallback)
- [ ] Implement certificate detail modal
- [ ] Add delete with confirmation
- [ ] Test expiration calculations

### Phase 4: IP Ban Management (Day 3)
- [ ] Implement ban list table with filters
- [ ] Create ban form with duration parsing
- [ ] Add expired ban display (grayed out)
- [ ] Implement delete/unban functionality
- [ ] Test filter combinations
- [ ] Test duration parsing

### Phase 5: TLS-RPT Reports (Day 3-4)
- [ ] Implement report list table with filters
- [ ] Create report detail modal
- [ ] Add failure details table parsing
- [ ] Implement raw JSON view (collapsible)
- [ ] Add success/failure chart (optional)
- [ ] Test pagination

### Phase 6: MTA-STS Policies (Day 4)
- [ ] Implement domain selector
- [ ] Create policy form (mode, MX hosts, max age)
- [ ] Add policy preview generation
- [ ] Implement save/update functionality
- [ ] Add delete with confirmation
- [ ] Test policy generation

### Phase 7: Polish & Testing (Day 5)
- [ ] Add loading states to all tables
- [ ] Add error handling and toast notifications
- [ ] Implement form validation (Zod schemas)
- [ ] Add empty states ("No certificates uploaded")
- [ ] Test all CRUD operations end-to-end
- [ ] Verify expiration warning system
- [ ] Ensure responsive design
- [ ] Update settings index with real data

---

## 8. RBAC / Capability Requirements

Apply capability checks to settings pages:

| Feature | Required Capability | Notes |
|---------|---------------------|-------|
| DKIM Management | `domains:write` | Read-only users can view but not modify |
| Certificate Management | `domains:write` | Same as DKIM |
| IP Ban Management | `bans:write`, `bans:delete` | Separate read capability (`bans:read`) |
| TLS-RPT Reports | `domains:read` | Read-only access |
| MTA-STS Policies | `domains:write` | Same as domain configuration |

**Implementation:**

```typescript
import { useCapabilities } from '@/lib/hooks/useCapabilities'

function SettingsPage() {
  const { hasCapability } = useCapabilities()
  const canWrite = hasCapability('domains:write')

  return (
    <>
      {canWrite && <Button onClick={handleCreate}>Create</Button>}
      {/* Disable forms if no write access */}
    </>
  )
}
```

---

## 9. Testing Strategy

### Unit Tests
- Test Zustand store actions (mock API calls)
- Test helper functions (duration parsing, expiration calculation, policy generation)
- Test form validation schemas

### Integration Tests
- Test DKIM key creation flow (domain select → key gen → submit)
- Test certificate upload with file input
- Test ban creation with various duration formats
- Test TLS-RPT report filtering
- Test MTA-STS policy save/update

### E2E Tests (Playwright)
- Create DKIM key for domain → verify in list
- Upload certificate → verify expiration warning appears if < 30 days
- Create IP ban → verify in list → delete ban
- View TLS-RPT report details
- Create MTA-STS policy → verify preview → save

---

## 10. Success Criteria

1. All 5 settings subsections functional and accessible
2. DKIM key generation works client-side
3. Certificate expiration warnings display correctly
4. IP bans can be created with custom durations
5. TLS-RPT reports display failure details properly
6. MTA-STS policy preview matches RFC 8461 format
7. All forms validate input and display errors
8. Loading states and error handling implemented
9. Capability-based UI hiding works correctly
10. Mobile-responsive design

---

## 11. Known Issues & Limitations

### Backend Limitations:
- DKIM: No endpoint to generate keys server-side (must be done client-side)
- Certificates: Private keys are never returned after upload (correct for security)
- TLS-RPT: No endpoint to manually delete reports (retention is DB-level)
- MTA-STS: Policy serving requires DNS setup (`mta-sts.{domain}` subdomain)

### Frontend Considerations:
- DKIM key generation requires Web Crypto API or library (large bundle size)
- Certificate file upload needs secure handling (never log/expose private keys)
- TLS-RPT failure details structure may vary (flexible parsing needed)

---

## 12. Future Enhancements

- **DKIM:**
  - Support for multiple selectors per domain
  - Key rotation scheduler
  - Public key DNS auto-verification

- **Certificates:**
  - Let's Encrypt integration for auto-renewal
  - Certificate expiration email notifications
  - CSR generation helper

- **Bans:**
  - Auto-ban based on failed login attempts
  - Whitelist management (IPs that cannot be banned)
  - Ban statistics and charts

- **TLS-RPT:**
  - Report retention policy settings
  - Aggregate failure charts over time
  - Email alerts for high failure rates

- **MTA-STS:**
  - Policy validation against actual DNS/MX records
  - Policy version tracking
  - Multi-domain policy templates

---

**Document Created:** 2026-02-23
**Status:** Ready for implementation
**Next Steps:** Begin Phase 1 setup (stores and routes)
