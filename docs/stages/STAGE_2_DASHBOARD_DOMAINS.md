# Stage 2: Dashboard & Domain Management - Detailed Implementation Plan

**Status:** 🟡 Partially Complete - Dashboard metrics functional, charts and DNS status needed
**Priority:** HIGH
**Estimated Effort:** 2-3 days (1 day dashboard enhancements, 1-2 days domain DNS components)

---

## Overview

Complete the dashboard with interactive visualizations and activity tracking, plus add DNS status display to domain management. Stage 1 (Foundation) has been completed with authentication, routing, and basic UI components in place.

**Current State:**
- ✅ Dashboard route and layout complete
- ✅ Basic metric cards implemented (domains, mailboxes, queue stats)
- ✅ Domain list, create, edit, delete functionality complete
- ✅ Domain store with full CRUD operations
- ✅ Recharts library already installed (v3.7.0)
- ❌ Message volume chart needs real data integration
- ❌ Recent activity feed needs backend API integration
- ❌ DNS status check component not yet implemented
- ❌ Real-time data refresh strategy not implemented

**Tech Stack:**
- React 19 with TanStack Router
- Zustand for state management
- Recharts for charts (already installed, React 19 compatible)
- Tailwind CSS v4 with Swiss Clean Design System
- Lucide React icons

---

## Current Implementation Analysis

### Dashboard Current State

**File:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/dashboard/index.tsx`

**Implemented:**
- ✅ Four metric cards: Total Domains, Total Mailboxes, Queue Pending, Queue Failed
- ✅ Message volume bar chart (Recharts BarChart component)
- ✅ Recent activity panel with activity type icons and timestamp formatting
- ✅ Loading and error states
- ✅ Responsive grid layout (1 col mobile, 2 col tablet, 4 col desktop)

**Dashboard Store:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/lib/stores/dashboardStore.ts`

**Implemented:**
- ✅ API call to `/api/v1/admin/stats` endpoint
- ✅ TypeScript interfaces for all data structures
- ✅ Error handling and loading states

**Data Structure Expected:**
```typescript
interface DashboardStats {
  domainCount: number
  mailboxCount: number
  queueStats: {
    pending: number
    processing: number
    failed: number
  }
  messageVolume: Array<{
    date: string
    count: number
  }>
  recentActivity: Array<{
    id: string
    type: 'domain_created' | 'mailbox_created' | 'message_sent' | 'message_received'
    description: string
    timestamp: string
  }>
}
```

### Domain Management Current State

**Files:**
- `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/domains/index.tsx` - List view
- `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/domains/$id.tsx` - Edit view
- `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/domains/new.tsx` - Create view
- `/Volumes/sdcard256gb/projects/rest-mail/admin/src/lib/stores/domainStore.ts` - State management

**Implemented:**
- ✅ Domain list table with search functionality
- ✅ Domain type badge (traditional/restmail)
- ✅ Active status toggle visualization
- ✅ Create domain form with validation
- ✅ Edit domain form with existing data loading
- ✅ Delete domain with confirmation
- ✅ Full CRUD operations in domain store

**Domain Store Interface:**
```typescript
interface Domain {
  id: number
  domain_name: string
  domain_type: string
  is_active: boolean
  created_at: string
  dns_records?: DnsRecord[]
}

interface DnsRecord {
  type: string
  name: string
  value: string
  verified: boolean
}
```

**Missing:**
- ❌ DNS status check API integration
- ❌ DNS records display component
- ❌ DNS verification status indicators
- ❌ DKIM public key display
- ❌ MTA-STS policy status

---

## Backend API Requirements

### 1. Dashboard Stats Endpoint Enhancement

**Endpoint:** `GET /api/v1/admin/stats`

**Current Response (Assumed):**
```json
{
  "domainCount": 5,
  "mailboxCount": 23,
  "queueStats": {
    "pending": 12,
    "processing": 3,
    "failed": 1
  },
  "messageVolume": [],
  "recentActivity": []
}
```

**Required Enhancements:**

#### Message Volume Data
The backend should populate `messageVolume` with last 7 days of message counts:

```json
"messageVolume": [
  { "date": "Feb 17", "count": 45 },
  { "date": "Feb 18", "count": 67 },
  { "date": "Feb 19", "count": 52 },
  { "date": "Feb 20", "count": 89 },
  { "date": "Feb 21", "count": 71 },
  { "date": "Feb 22", "count": 94 },
  { "date": "Feb 23", "count": 38 }
]
```

**SQL Query Example (for Go backend):**
```sql
SELECT
  DATE_FORMAT(created_at, '%b %d') as date,
  COUNT(*) as count
FROM outbound_queue
WHERE created_at >= DATE_SUB(NOW(), INTERVAL 7 DAY)
GROUP BY DATE(created_at)
ORDER BY DATE(created_at) ASC
```

#### Recent Activity Data
The backend should populate `recentActivity` from the activity_logs table:

```json
"recentActivity": [
  {
    "id": "act_123",
    "type": "domain_created",
    "description": "Domain example.com was created",
    "timestamp": "2026-02-23T10:30:00Z"
  },
  {
    "id": "act_124",
    "type": "mailbox_created",
    "description": "Mailbox john@example.com was created",
    "timestamp": "2026-02-23T09:15:00Z"
  },
  {
    "id": "act_125",
    "type": "message_sent",
    "description": "Message sent to customer@domain.com",
    "timestamp": "2026-02-23T08:45:00Z"
  }
]
```

**SQL Query Example:**
```sql
SELECT
  id,
  action as type,
  CONCAT(action, ' ', resource_type, ' ', resource_id) as description,
  created_at as timestamp
FROM activity_logs
ORDER BY created_at DESC
LIMIT 10
```

**Go Handler Enhancement:**
```go
// internal/api/handlers/stats.go (enhancement)
func (h *StatsHandler) GetDashboardStats(w http.ResponseWriter, r *http.Request) {
    // ... existing domain/mailbox/queue counts ...

    // Add message volume (last 7 days)
    messageVolume, err := h.getMessageVolume()
    if err != nil {
        log.Printf("Failed to fetch message volume: %v", err)
        messageVolume = []MessageVolumeData{}
    }

    // Add recent activity (last 10 items)
    recentActivity, err := h.getRecentActivity()
    if err != nil {
        log.Printf("Failed to fetch recent activity: %v", err)
        recentActivity = []ActivityItem{}
    }

    stats := DashboardStats{
        DomainCount: domainCount,
        MailboxCount: mailboxCount,
        QueueStats: queueStats,
        MessageVolume: messageVolume,
        RecentActivity: recentActivity,
    }

    respond.Data(w, http.StatusOK, stats)
}
```

### 2. Domain DNS Check Endpoint

**Endpoint:** `GET /api/v1/admin/domains/{id}/dns`

**Purpose:** Perform real-time DNS lookups and verify domain configuration.

**Response Format:**
```json
{
  "domain_id": 5,
  "domain_name": "example.com",
  "last_checked": "2026-02-23T12:30:00Z",
  "records": [
    {
      "type": "MX",
      "name": "example.com",
      "value": "mail.example.com",
      "priority": 10,
      "verified": true,
      "status": "valid",
      "message": "MX record correctly points to mail server"
    },
    {
      "type": "SPF",
      "name": "example.com",
      "value": "v=spf1 mx ~all",
      "verified": true,
      "status": "valid",
      "message": "SPF record found and valid"
    },
    {
      "type": "DKIM",
      "name": "default._domainkey.example.com",
      "value": "v=DKIM1; k=rsa; p=MIGfMA0GCSq...",
      "verified": true,
      "status": "valid",
      "message": "DKIM public key published",
      "selector": "default"
    },
    {
      "type": "DMARC",
      "name": "_dmarc.example.com",
      "value": "v=DMARC1; p=quarantine; rua=mailto:dmarc@example.com",
      "verified": true,
      "status": "valid",
      "message": "DMARC policy found"
    },
    {
      "type": "MTA-STS",
      "name": "_mta-sts.example.com",
      "value": "v=STSv1; id=20260223123000",
      "verified": false,
      "status": "missing",
      "message": "MTA-STS record not found"
    }
  ]
}
```

**Go Handler Implementation:**
```go
// File: internal/api/handlers/domain_dns.go (new file)
package handlers

import (
    "net/http"
    "net"
    "strconv"
    "time"

    "github.com/go-chi/chi/v5"
    "github.com/restmail/restmail/internal/api/respond"
    "github.com/restmail/restmail/internal/db/repositories"
    "gorm.io/gorm"
)

type DomainDNSHandler struct {
    db *gorm.DB
}

func NewDomainDNSHandler(db *gorm.DB) *DomainDNSHandler {
    return &DomainDNSHandler{db: db}
}

type DNSCheckResponse struct {
    DomainID    int         `json:"domain_id"`
    DomainName  string      `json:"domain_name"`
    LastChecked string      `json:"last_checked"`
    Records     []DNSRecord `json:"records"`
}

type DNSRecord struct {
    Type     string `json:"type"`
    Name     string `json:"name"`
    Value    string `json:"value"`
    Priority *int   `json:"priority,omitempty"`
    Verified bool   `json:"verified"`
    Status   string `json:"status"` // valid, invalid, missing
    Message  string `json:"message"`
    Selector string `json:"selector,omitempty"`
}

func (h *DomainDNSHandler) CheckDNS(w http.ResponseWriter, r *http.Request) {
    idStr := chi.URLParam(r, "id")
    id, err := strconv.ParseInt(idStr, 10, 64)
    if err != nil {
        respond.Error(w, http.StatusBadRequest, "invalid_id", "Invalid domain ID")
        return
    }

    // Get domain from database
    repo := repositories.NewDomainRepository(h.db)
    domain, err := repo.GetByID(uint(id))
    if err != nil {
        respond.Error(w, http.StatusNotFound, "not_found", "Domain not found")
        return
    }

    // Perform DNS checks
    records := []DNSRecord{}

    // Check MX record
    mxRecords, _ := net.LookupMX(domain.DomainName)
    if len(mxRecords) > 0 {
        for _, mx := range mxRecords {
            records = append(records, DNSRecord{
                Type:     "MX",
                Name:     domain.DomainName,
                Value:    mx.Host,
                Priority: intPtr(int(mx.Pref)),
                Verified: true,
                Status:   "valid",
                Message:  "MX record found",
            })
        }
    } else {
        records = append(records, DNSRecord{
            Type:     "MX",
            Name:     domain.DomainName,
            Verified: false,
            Status:   "missing",
            Message:  "No MX records found",
        })
    }

    // Check SPF record
    txtRecords, _ := net.LookupTXT(domain.DomainName)
    spfFound := false
    for _, txt := range txtRecords {
        if strings.HasPrefix(txt, "v=spf1") {
            records = append(records, DNSRecord{
                Type:     "SPF",
                Name:     domain.DomainName,
                Value:    txt,
                Verified: true,
                Status:   "valid",
                Message:  "SPF record found",
            })
            spfFound = true
            break
        }
    }
    if !spfFound {
        records = append(records, DNSRecord{
            Type:     "SPF",
            Name:     domain.DomainName,
            Verified: false,
            Status:   "missing",
            Message:  "SPF record not found",
        })
    }

    // Check DKIM (get selector from database)
    dkimKeys, _ := repo.GetDKIMKeys(uint(id))
    if len(dkimKeys) > 0 {
        for _, key := range dkimKeys {
            dkimName := fmt.Sprintf("%s._domainkey.%s", key.Selector, domain.DomainName)
            dkimRecords, _ := net.LookupTXT(dkimName)
            if len(dkimRecords) > 0 {
                records = append(records, DNSRecord{
                    Type:     "DKIM",
                    Name:     dkimName,
                    Value:    dkimRecords[0],
                    Verified: true,
                    Status:   "valid",
                    Message:  "DKIM public key published",
                    Selector: key.Selector,
                })
            } else {
                records = append(records, DNSRecord{
                    Type:     "DKIM",
                    Name:     dkimName,
                    Verified: false,
                    Status:   "missing",
                    Message:  "DKIM public key not found in DNS",
                    Selector: key.Selector,
                })
            }
        }
    }

    // Check DMARC
    dmarcName := fmt.Sprintf("_dmarc.%s", domain.DomainName)
    dmarcRecords, _ := net.LookupTXT(dmarcName)
    if len(dmarcRecords) > 0 {
        records = append(records, DNSRecord{
            Type:     "DMARC",
            Name:     dmarcName,
            Value:    dmarcRecords[0],
            Verified: true,
            Status:   "valid",
            Message:  "DMARC policy found",
        })
    } else {
        records = append(records, DNSRecord{
            Type:     "DMARC",
            Name:     dmarcName,
            Verified: false,
            Status:   "missing",
            Message:  "DMARC policy not found",
        })
    }

    response := DNSCheckResponse{
        DomainID:    int(id),
        DomainName:  domain.DomainName,
        LastChecked: time.Now().Format(time.RFC3339),
        Records:     records,
    }

    respond.Data(w, http.StatusOK, response)
}

func intPtr(i int) *int {
    return &i
}
```

**Route Registration:**
```go
// File: internal/api/routes.go (add to admin routes section)
domainDNSH := handlers.NewDomainDNSHandler(db)
r.Get("/api/v1/admin/domains/{id}/dns", domainDNSH.CheckDNS)
```

---

## Frontend Implementation

### 1. Dashboard Enhancements

#### 1.1 Message Volume Chart Component

**Status:** Chart component exists, needs proper data integration testing

**Current Implementation:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/dashboard/index.tsx`

The chart is already implemented using Recharts. When backend provides proper `messageVolume` data, it will automatically display.

**Testing Checklist:**
- [ ] Verify chart renders with mock data
- [ ] Test responsive container behavior
- [ ] Verify tooltip displays correctly
- [ ] Check bar colors match design system (var(--red-primary))
- [ ] Test empty state handling (no data)

**Enhancement: Add Empty State**

**Location:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/dashboard/index.tsx` (line 108)

```tsx
<div className="h-80">
  {stats?.messageVolume && stats.messageVolume.length > 0 ? (
    <ResponsiveContainer width="100%" height="100%">
      <BarChart data={stats.messageVolume}>
        {/* ... existing chart code ... */}
      </BarChart>
    </ResponsiveContainer>
  ) : (
    <div className="flex items-center justify-center h-full">
      <div className="text-center">
        <BarChart className="w-12 h-12 mx-auto mb-4" style={{ color: 'var(--gray-secondary)' }} />
        <p style={{ color: 'var(--gray-secondary)' }} className="text-sm">
          No message volume data available
        </p>
      </div>
    </div>
  )}
</div>
```

#### 1.2 Recent Activity Component

**Status:** Component exists and functional, waiting for backend data

**Current Implementation:** `ActivityItem` component in dashboard (line 216-260)

**Activity Type Icons:**
- ✅ `domain_created` → Server icon
- ✅ `mailbox_created` → Mail icon
- ✅ `message_sent` / `message_received` → Activity icon

**Timestamp Formatting:**
- ✅ Just now (< 60 seconds)
- ✅ Xm ago (< 60 minutes)
- ✅ Xh ago (< 24 hours)
- ✅ Xd ago (>= 24 hours)

**Testing Checklist:**
- [ ] Verify activity icons display correctly
- [ ] Test timestamp formatting for all ranges
- [ ] Test scrolling behavior with many activities (max-h-80)
- [ ] Verify empty state displays correctly
- [ ] Test activity description truncation

No code changes needed - component is complete.

### 2. DNS Status Display Component

#### 2.1 Create DomainDNS Component

**File:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/components/domains/DomainDNS.tsx` (new file)

```tsx
import { useEffect, useState } from 'react'
import { CheckCircle, XCircle, AlertCircle, RefreshCw } from 'lucide-react'
import { apiV1 } from '../../lib/api'

interface DNSRecord {
  type: string
  name: string
  value: string
  priority?: number
  verified: boolean
  status: 'valid' | 'invalid' | 'missing'
  message: string
  selector?: string
}

interface DNSCheckResponse {
  domain_id: number
  domain_name: string
  last_checked: string
  records: DNSRecord[]
}

interface DomainDNSProps {
  domainId: number
  accessToken: string
}

export function DomainDNS({ domainId, accessToken }: DomainDNSProps) {
  const [dnsData, setDnsData] = useState<DNSCheckResponse | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const checkDNS = async () => {
    setIsLoading(true)
    setError(null)

    try {
      const response = await apiV1.request(
        `/admin/domains/${domainId}/dns`,
        { method: 'GET' },
        accessToken
      )

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.error || 'Failed to check DNS')
      }

      const data = await response.json()
      setDnsData(data)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to check DNS')
    } finally {
      setIsLoading(false)
    }
  }

  useEffect(() => {
    checkDNS()
  }, [domainId, accessToken])

  const getStatusIcon = (status: string, verified: boolean) => {
    if (status === 'valid' && verified) {
      return <CheckCircle className="w-5 h-5" style={{ color: 'var(--green-success)' }} />
    } else if (status === 'invalid') {
      return <XCircle className="w-5 h-5" style={{ color: 'var(--red-primary)' }} />
    } else {
      return <AlertCircle className="w-5 h-5" style={{ color: 'var(--gray-secondary)' }} />
    }
  }

  const getStatusBadge = (status: string, verified: boolean) => {
    if (status === 'valid' && verified) {
      return (
        <span
          className="px-2 py-1 text-xs rounded"
          style={{
            backgroundColor: 'rgba(34, 197, 94, 0.1)',
            color: 'var(--green-success)',
          }}
        >
          Verified
        </span>
      )
    } else if (status === 'invalid') {
      return (
        <span
          className="px-2 py-1 text-xs rounded"
          style={{
            backgroundColor: 'rgba(228, 35, 19, 0.1)',
            color: 'var(--red-primary)',
          }}
        >
          Invalid
        </span>
      )
    } else {
      return (
        <span
          className="px-2 py-1 text-xs rounded"
          style={{
            backgroundColor: 'var(--bg-surface)',
            color: 'var(--gray-secondary)',
          }}
        >
          Missing
        </span>
      )
    }
  }

  const formatTimestamp = (timestamp: string) => {
    const date = new Date(timestamp)
    return date.toLocaleString('en-US', {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    })
  }

  if (isLoading && !dnsData) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="text-center">
          <div className="w-8 h-8 border-4 border-gray-200 border-t-[var(--red-primary)] rounded-full animate-spin mx-auto mb-4"></div>
          <p style={{ color: 'var(--gray-secondary)' }}>Checking DNS records...</p>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-6 rounded-lg" style={{ border: '1px solid var(--red-primary)', backgroundColor: 'rgba(228, 35, 19, 0.05)' }}>
        <div className="flex items-start gap-3">
          <XCircle className="w-5 h-5 flex-shrink-0" style={{ color: 'var(--red-primary)' }} />
          <div>
            <p style={{ color: 'var(--black-soft)' }} className="font-semibold mb-1">
              DNS Check Failed
            </p>
            <p style={{ color: 'var(--gray-secondary)' }} className="text-sm">
              {error}
            </p>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {/* Header with Refresh Button */}
      <div className="flex items-center justify-between">
        <div>
          <h3
            style={{
              fontFamily: 'Space Grotesk, sans-serif',
              color: 'var(--black-soft)',
            }}
            className="text-lg font-semibold"
          >
            DNS Configuration
          </h3>
          {dnsData && (
            <p style={{ color: 'var(--gray-secondary)' }} className="text-sm mt-1">
              Last checked: {formatTimestamp(dnsData.last_checked)}
            </p>
          )}
        </div>
        <button
          onClick={checkDNS}
          disabled={isLoading}
          className="flex items-center gap-2 px-4 py-2 rounded-lg"
          style={{
            border: '1px solid var(--gray-border)',
            color: 'var(--black-soft)',
          }}
        >
          <RefreshCw className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} />
          Refresh
        </button>
      </div>

      {/* DNS Records */}
      <div className="space-y-3">
        {dnsData?.records.map((record, index) => (
          <div
            key={index}
            className="p-4 rounded-lg"
            style={{ border: '1px solid var(--gray-border)' }}
          >
            <div className="flex items-start justify-between mb-3">
              <div className="flex items-center gap-3">
                {getStatusIcon(record.status, record.verified)}
                <div>
                  <div className="flex items-center gap-2">
                    <span
                      style={{
                        fontFamily: 'Space Grotesk, sans-serif',
                        color: 'var(--black-soft)',
                      }}
                      className="font-semibold"
                    >
                      {record.type}
                    </span>
                    {record.selector && (
                      <span
                        className="text-xs px-2 py-0.5 rounded"
                        style={{
                          backgroundColor: 'var(--bg-surface)',
                          color: 'var(--gray-secondary)',
                        }}
                      >
                        selector: {record.selector}
                      </span>
                    )}
                  </div>
                  <p style={{ color: 'var(--gray-secondary)' }} className="text-sm mt-1">
                    {record.name}
                  </p>
                </div>
              </div>
              {getStatusBadge(record.status, record.verified)}
            </div>

            {record.value && (
              <div
                className="p-3 rounded text-sm font-mono break-all"
                style={{
                  backgroundColor: 'var(--bg-surface)',
                  color: 'var(--gray-secondary)',
                }}
              >
                {record.priority !== undefined && `Priority: ${record.priority} | `}
                {record.value}
              </div>
            )}

            <p style={{ color: 'var(--gray-secondary)' }} className="text-sm mt-2">
              {record.message}
            </p>
          </div>
        ))}
      </div>
    </div>
  )
}
```

#### 2.2 Integrate DNS Component into Domain Detail View

**File:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/domains/$id.tsx`

**Add Import:**
```tsx
import { DomainDNS } from '../../components/domains/DomainDNS'
```

**Add DNS Section (after the form, around line 150):**
```tsx
{/* DNS Configuration Section */}
<div
  className="p-6 rounded-lg mt-6"
  style={{ border: '1px solid var(--gray-border)' }}
>
  <DomainDNS domainId={parseInt(id)} accessToken={accessToken} />
</div>
```

#### 2.3 Update Domain Store to Include DNS Data

**File:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/lib/stores/domainStore.ts`

**Add Method:**
```typescript
checkDomainDNS: async (id: string, accessToken: string) => {
  try {
    const response = await apiV1.request(`/admin/domains/${id}/dns`, { method: 'GET' }, accessToken)

    if (!response.ok) {
      const error = await response.json()
      throw new Error(error.error || 'Failed to check DNS')
    }

    const data = await response.json()

    // Update current domain with DNS records
    const currentDomain = get().currentDomain
    if (currentDomain && currentDomain.id === parseInt(id)) {
      set({
        currentDomain: {
          ...currentDomain,
          dns_records: data.records,
        },
      })
    }

    return data
  } catch (error) {
    throw error
  }
},
```

### 3. Real-Time Data Refresh Strategy

#### 3.1 Auto-Refresh Dashboard Stats

**File:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/dashboard/index.tsx`

**Implementation Strategy:**
- Auto-refresh every 30 seconds
- Pause refresh when user navigates away (use visibility API)
- Show "Refreshing..." indicator

**Add to Dashboard Component:**
```tsx
useEffect(() => {
  if (!accessToken) return

  // Initial fetch
  fetchDashboardStats(accessToken)

  // Set up interval for auto-refresh (30 seconds)
  const interval = setInterval(() => {
    if (document.visibilityState === 'visible') {
      fetchDashboardStats(accessToken)
    }
  }, 30000)

  // Cleanup interval on unmount
  return () => clearInterval(interval)
}, [accessToken, fetchDashboardStats])

// Listen for visibility changes
useEffect(() => {
  const handleVisibilityChange = () => {
    if (document.visibilityState === 'visible' && accessToken) {
      fetchDashboardStats(accessToken)
    }
  }

  document.addEventListener('visibilitychange', handleVisibilityChange)
  return () => document.removeEventListener('visibilitychange', handleVisibilityChange)
}, [accessToken, fetchDashboardStats])
```

#### 3.2 Manual Refresh Button

**Add Refresh Button to Dashboard Header:**
```tsx
<div className="flex items-center justify-between mb-6">
  <div>
    <h1
      style={{
        fontFamily: 'Space Grotesk, sans-serif',
        color: 'var(--black-soft)',
      }}
      className="text-2xl font-bold"
    >
      Dashboard
    </h1>
  </div>
  <button
    onClick={() => fetchDashboardStats(accessToken)}
    disabled={isLoading}
    className="flex items-center gap-2 px-4 py-2 rounded-lg"
    style={{
      border: '1px solid var(--gray-border)',
      color: 'var(--black-soft)',
    }}
  >
    <RefreshCw className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} />
    Refresh
  </button>
</div>
```

#### 3.3 Optimistic Updates

For domain and mailbox operations, implement optimistic updates to improve perceived performance:

**Example in Domain Store:**
```typescript
deleteDomain: async (id: string, accessToken: string) => {
  // Optimistic update: remove from UI immediately
  const previousDomains = get().domains
  set({
    domains: previousDomains.filter((d) => d.id !== parseInt(id)),
  })

  try {
    const response = await apiV1.request(`/admin/domains/${id}`, { method: 'DELETE' }, accessToken)

    if (!response.ok) {
      // Rollback on error
      set({ domains: previousDomains })
      const error = await response.json()
      throw new Error(error.error || 'Failed to delete domain')
    }

    set({
      isLoading: false,
      error: null,
    })
  } catch (error) {
    // Already rolled back
    set({
      error: error instanceof Error ? error.message : 'Failed to delete domain',
      isLoading: false,
    })
    throw error
  }
},
```

---

## Component Specifications

### 1. MetricCard Component

**Status:** ✅ Already implemented

**Location:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/dashboard/index.tsx` (line 176-206)

**Props:**
- `icon: React.ReactNode` - Lucide icon component
- `label: string` - Display label (e.g., "Total Domains")
- `value: number` - Numeric value to display
- `color: string` - CSS variable for icon/text color
- `isHighlight?: boolean` - Red background for critical metrics

**Styling:**
- Border: `1px solid var(--gray-border)`
- Padding: `24px`
- Border radius: `8px`
- Value font: Space Grotesk, 3xl, bold
- Label font: 14px, gray-secondary

### 2. ActivityItem Component

**Status:** ✅ Already implemented

**Location:** `/Volumes/sdcard256gb/projects/rest-mail/admin/src/routes/dashboard/index.tsx` (line 216-260)

**Props:**
- `activity.type` - Activity type enum
- `activity.description` - Human-readable description
- `activity.timestamp` - ISO 8601 timestamp

**Features:**
- Dynamic icon based on activity type
- Relative timestamp formatting
- Icon background with surface color
- Truncation for long descriptions

### 3. DomainDNS Component

**Status:** ❌ Needs to be created

**Specification provided in section 2.1 above**

**Features:**
- Real-time DNS lookup
- Status indicators (verified, invalid, missing)
- Refresh button
- Last checked timestamp
- Record type badges (MX, SPF, DKIM, DMARC, MTA-STS)
- Copy-to-clipboard for DNS values
- Expandable/collapsible sections

### 4. Chart Wrapper Components

**Status:** ✅ Recharts already integrated

**No additional wrapper needed** - Recharts components are used directly:
- `<BarChart>` for message volume
- `<ResponsiveContainer>` for responsive sizing
- `<CartesianGrid>` for gridlines
- `<XAxis>` and `<YAxis>` for axes
- `<Tooltip>` for hover info
- `<Bar>` for data visualization

**Custom Styling:**
- Bar color: `var(--red-primary)`
- Grid color: `var(--gray-border)`
- Axis text: `var(--gray-secondary)`, 12px
- Tooltip: White background, gray border, 8px radius

---

## Testing Checklist

### Backend Tests:

**Dashboard Stats Endpoint:**
- [ ] `/api/v1/admin/stats` returns correct domain count
- [ ] `/api/v1/admin/stats` returns correct mailbox count
- [ ] `/api/v1/admin/stats` returns accurate queue statistics
- [ ] Message volume includes last 7 days with correct date formatting
- [ ] Recent activity fetches from activity_logs table
- [ ] Recent activity limits to 10 items
- [ ] Endpoint handles database errors gracefully

**Domain DNS Endpoint:**
- [ ] `/api/v1/admin/domains/{id}/dns` performs MX lookup
- [ ] Endpoint performs SPF TXT record lookup
- [ ] Endpoint performs DKIM TXT record lookup (with selector)
- [ ] Endpoint performs DMARC TXT record lookup
- [ ] Endpoint checks MTA-STS DNS record
- [ ] Missing DNS records marked as status: "missing"
- [ ] Invalid records marked as status: "invalid"
- [ ] Valid records marked as status: "valid" and verified: true
- [ ] Endpoint returns 404 for non-existent domain
- [ ] DNS timeout handled gracefully (5 second timeout)

### Frontend Tests:

**Dashboard:**
- [ ] Dashboard loads without errors
- [ ] Metric cards display correct values
- [ ] Message volume chart renders with data
- [ ] Empty state displays when no message volume data
- [ ] Recent activity list displays correctly
- [ ] Activity icons match activity types
- [ ] Timestamp formatting works for all ranges
- [ ] Auto-refresh triggers every 30 seconds
- [ ] Manual refresh button works
- [ ] Refresh pauses when tab not visible
- [ ] Loading spinner displays during fetch
- [ ] Error message displays on API failure

**Domain DNS Component:**
- [ ] DNS component loads when domain detail page opens
- [ ] DNS records display with correct status badges
- [ ] Verified records show green checkmark
- [ ] Missing records show gray alert icon
- [ ] Invalid records show red X icon
- [ ] Refresh button triggers new DNS check
- [ ] Last checked timestamp updates after refresh
- [ ] Loading state displays during DNS check
- [ ] Error state displays on DNS check failure
- [ ] Record values display in monospace font
- [ ] Long DNS values wrap correctly
- [ ] Component handles missing DKIM selector gracefully

**Integration Tests:**
- [ ] Dashboard to domain list navigation works
- [ ] Domain list to domain detail navigation works
- [ ] DNS component updates when switching between domains
- [ ] State persists correctly across navigation
- [ ] Multiple simultaneous DNS checks don't conflict

---

## Success Criteria

### Functional Requirements:
1. ✅ Dashboard displays 4 metric cards with real-time data
2. ✅ Message volume chart visualizes last 7 days of message counts
3. ✅ Recent activity feed shows last 10 admin actions
4. ✅ DNS status component performs real-time DNS lookups
5. ✅ DNS records display with verification status (valid/invalid/missing)
6. ✅ Manual refresh functionality on dashboard and DNS component
7. ✅ Auto-refresh dashboard data every 30 seconds
8. ✅ Pause refresh when user navigates away from tab

### Performance Requirements:
1. ✅ Dashboard loads in < 2 seconds
2. ✅ DNS check completes in < 5 seconds
3. ✅ Chart renders without lag
4. ✅ No layout shifts during data loading
5. ✅ Smooth transitions for loading states

### UX Requirements:
1. ✅ Loading states for all async operations
2. ✅ Error messages are user-friendly
3. ✅ Success feedback for DNS refresh
4. ✅ Responsive design on mobile/tablet/desktop
5. ✅ Accessible keyboard navigation
6. ✅ Color-coded status indicators (green/yellow/red)

---

## Implementation Steps

### Day 1: Backend API Implementation

#### Morning (3-4 hours):
1. **Enhance `/api/v1/admin/stats` endpoint:**
   - Add message volume query (last 7 days)
   - Add recent activity query (last 10 items)
   - Test with Postman/curl
   - Verify date formatting for chart

2. **Test dashboard stats:**
   ```bash
   curl -H "Authorization: Bearer $TOKEN" \
        http://localhost:3000/api/v1/admin/stats | jq
   ```

#### Afternoon (4-5 hours):
3. **Create DNS check handler:**
   - Implement `internal/api/handlers/domain_dns.go`
   - Add MX, SPF, DKIM, DMARC, MTA-STS checks
   - Add 5-second timeout for DNS queries
   - Handle missing/invalid DNS gracefully

4. **Register DNS route:**
   - Add route to `internal/api/routes.go`
   - Test DNS endpoint for existing domain

5. **Test DNS endpoint:**
   ```bash
   curl -H "Authorization: Bearer $TOKEN" \
        http://localhost:3000/api/v1/admin/domains/1/dns | jq
   ```

### Day 2: Frontend Integration

#### Morning (3-4 hours):
1. **Test dashboard with real API data:**
   - Start backend server
   - Login to admin website
   - Navigate to dashboard
   - Verify metrics cards load correctly
   - Check chart displays data
   - Verify recent activity displays

2. **Add dashboard enhancements:**
   - Add manual refresh button to header
   - Implement auto-refresh with 30-second interval
   - Add visibility API integration
   - Test refresh functionality

3. **Add chart empty state:**
   - Update chart section with conditional rendering
   - Test empty state display

#### Afternoon (4-5 hours):
4. **Create DomainDNS component:**
   - Create file: `admin/src/components/domains/DomainDNS.tsx`
   - Copy implementation from section 2.1
   - Add to components/domains directory

5. **Integrate DNS component:**
   - Open `admin/src/routes/domains/$id.tsx`
   - Import DomainDNS component
   - Add DNS section below form
   - Test DNS display

6. **Test DNS component:**
   - Navigate to domain detail page
   - Click "Refresh" button
   - Verify DNS records display
   - Test status badges (verified/missing/invalid)
   - Test with domain that has missing DNS records

### Day 3: Testing & Polish

#### Morning (2-3 hours):
1. **Manual testing:**
   - Test all dashboard features
   - Test DNS component on multiple domains
   - Test refresh functionality
   - Test error states (disconnect backend)
   - Test loading states
   - Test responsive design on mobile/tablet

2. **Bug fixes and polish:**
   - Fix any issues discovered during testing
   - Improve error messages
   - Add loading animations where needed

#### Afternoon (2-3 hours):
3. **Documentation:**
   - Document new API endpoints
   - Update component documentation
   - Add inline code comments

4. **Code review preparation:**
   - Clean up console.logs
   - Remove commented code
   - Format code consistently
   - Run linter and fix warnings

---

## Deployment Checklist

### Pre-deployment:
- [ ] All tests passing
- [ ] No console errors in browser
- [ ] Backend DNS timeout configured (5 seconds)
- [ ] Environment variables set correctly
- [ ] Build succeeds without warnings

### Backend Deployment:
- [ ] New handlers compiled successfully
- [ ] DNS route registered in routes.go
- [ ] Database migrations run (if needed)
- [ ] Backend restarted with new code

### Frontend Deployment:
- [ ] `npm run build` completes successfully
- [ ] Built files copied to deployment directory
- [ ] Admin website accessible at `/admin`
- [ ] Dashboard loads correctly in production
- [ ] DNS component works in production

### Post-deployment Verification:
- [ ] Dashboard metrics display real data
- [ ] Message volume chart renders correctly
- [ ] Recent activity feed populates
- [ ] DNS checks work for all domains
- [ ] Auto-refresh working (check after 30 seconds)
- [ ] Manual refresh button works
- [ ] No JavaScript errors in console
- [ ] Responsive design works on mobile

---

## Known Limitations & Future Enhancements

### Current Limitations:
1. DNS checks are synchronous (blocks response until complete)
2. No caching of DNS results (every check does full lookup)
3. Recent activity limited to 10 items (no pagination)
4. Message volume limited to 7 days (no date range selection)
5. Auto-refresh fixed at 30 seconds (not configurable)

### Future Enhancements:
1. **Async DNS Checks:**
   - Queue DNS checks in background job
   - Cache results for 5 minutes
   - Show cached results instantly, refresh in background

2. **Advanced Dashboard:**
   - Date range selector for message volume (7d/30d/90d)
   - Additional chart types (line chart, pie chart)
   - Export dashboard data to CSV
   - Customizable metric cards

3. **DNS Management:**
   - One-click DNS configuration (generate records)
   - DNS record suggestions based on missing records
   - Integration with DNS providers (Cloudflare, Route53)
   - Automated DNS propagation checking

4. **Activity Feed:**
   - Pagination for activity logs
   - Filter by activity type
   - Search activity descriptions
   - Export activity log to CSV

5. **Real-time Updates:**
   - WebSocket integration for live dashboard updates
   - Push notifications for critical events
   - Live queue status updates

---

## Dependencies

### Frontend:
- ✅ recharts (v3.7.0) - Already installed
- ✅ lucide-react - Already installed
- ✅ zustand - Already installed
- ✅ @tanstack/react-router - Already installed

### Backend:
- Standard library `net` package for DNS lookups
- No additional dependencies required

### CSS Variables (Swiss Clean Design System):
- `--red-primary` - Primary accent color
- `--black-soft` - Primary text color
- `--gray-secondary` - Secondary text color
- `--gray-border` - Border color
- `--bg-surface` - Surface background color
- `--green-success` - Success state color

---

## Rollback Plan

If issues arise after deployment:

1. **Backend Rollback:**
   - Revert to previous backend binary
   - New routes are additive (safe to remove)
   - No database migrations required

2. **Frontend Rollback:**
   - Revert to previous admin build
   - Or hide DNS component with feature flag:
     ```tsx
     const FEATURE_DNS_CHECK = false // Set to false to disable

     {FEATURE_DNS_CHECK && <DomainDNS ... />}
     ```

3. **Quick Fixes:**
   - Disable auto-refresh: Comment out interval in dashboard
   - Disable DNS check: Return mock "Not available" response

---

## Next Steps After Completion

Once Stage 2 is complete:

1. **Stage 3: Mailbox & Alias Management**
   - Add quota visualization charts
   - Implement alias management UI
   - Add bulk mailbox operations

2. **Stage 4: Queue Management**
   - Test queue action buttons (retry, bounce, delete)
   - Add queue filtering by date range
   - Implement bulk queue operations

3. **Stage 5: Pipelines & Filters**
   - Pipeline list and editor
   - Custom filter creation
   - Filter testing interface

4. **Stage 6: Admin Users & RBAC**
   - Complete backend API implementation (currently blocked)
   - Role management interface
   - Activity log viewer

---

**Document Created:** 2026-02-23
**Stage Status:** Ready for implementation
**Estimated Completion:** 2-3 days
**Dependencies:** Backend DNS check endpoint, Enhanced stats endpoint
**Risk Level:** LOW - Additive changes, no breaking modifications
