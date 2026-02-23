# Stage 8: Polish, Testing & Deployment - Detailed Implementation Plan

**Status:** NOT STARTED
**Priority:** CRITICAL - Final production readiness
**Estimated Effort:** 10-14 days
**Dependencies:** Stages 1-7 must be complete

---

## Overview

This is the final polish and hardening phase before production deployment. The goal is to ensure the admin website is production-ready with comprehensive error handling, testing coverage, accessibility compliance, security hardening, and complete documentation.

**Current State:**
- Admin website structure complete (Stages 1-4)
- Core features implemented (domains, mailboxes, queue)
- Some features pending (aliases, pipelines, settings)
- No comprehensive testing strategy in place
- Missing error handling and loading states
- No accessibility audit completed
- Production deployment plan needed

---

## 1. Error Handling Improvements

### 1.1 Global Error Boundary

**File:** `admin/src/components/ErrorBoundary.tsx` (new)

```typescript
import { Component, ReactNode } from 'react'
import { Button } from '#/components/ui/button'

interface Props {
  children: ReactNode
  fallback?: ReactNode
}

interface State {
  hasError: boolean
  error: Error | null
}

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props)
    this.state = { hasError: false, error: null }
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error }
  }

  componentDidCatch(error: Error, errorInfo: React.ErrorInfo) {
    console.error('ErrorBoundary caught:', error, errorInfo)
    // TODO: Send to error tracking service (Sentry)
  }

  render() {
    if (this.state.hasError) {
      if (this.props.fallback) {
        return this.props.fallback
      }

      return (
        <div className="flex min-h-screen items-center justify-center bg-gray-50">
          <div className="max-w-md rounded-lg bg-white p-8 shadow-lg">
            <h1 className="mb-4 text-2xl font-bold text-red-600">
              Something went wrong
            </h1>
            <p className="mb-4 text-gray-600">
              {this.state.error?.message || 'An unexpected error occurred'}
            </p>
            <Button
              onClick={() => window.location.reload()}
              variant="default"
            >
              Reload page
            </Button>
          </div>
        </div>
      )
    }

    return this.props.children
  }
}
```

**Implementation:**
- Wrap the entire app in ErrorBoundary in `app.tsx`
- Add error boundaries around major features (domain list, queue, etc.)
- Log errors to console in development
- Send to Sentry/similar in production

### 1.2 API Error Handling

**File:** `admin/src/lib/api/client.ts` (enhance existing)

```typescript
// Add error response type
export interface ApiError {
  error: {
    code: string
    message: string
    details?: Record<string, string[]>
  }
  status: number
}

// Add error parser
export function parseApiError(error: unknown): ApiError {
  if (axios.isAxiosError(error)) {
    const status = error.response?.status || 500
    const data = error.response?.data

    if (data?.error) {
      return {
        error: {
          code: data.error.code || 'unknown_error',
          message: data.error.message || 'An error occurred',
          details: data.error.details,
        },
        status,
      }
    }

    return {
      error: {
        code: 'network_error',
        message: error.message,
      },
      status,
    }
  }

  return {
    error: {
      code: 'unknown_error',
      message: 'An unexpected error occurred',
    },
    status: 500,
  }
}

// Add to Axios interceptor
apiClient.interceptors.response.use(
  (response) => response,
  (error) => {
    const apiError = parseApiError(error)

    // Show toast for certain errors
    if (apiError.status === 403) {
      toast.error('You do not have permission to perform this action')
    } else if (apiError.status >= 500) {
      toast.error('Server error. Please try again later.')
    }

    return Promise.reject(apiError)
  }
)
```

**Implementation Tasks:**
- Add error type definitions
- Implement parseApiError utility
- Add error toast notifications
- Handle 401 (redirect to login), 403 (permission denied), 404 (not found), 422 (validation), 500 (server error)
- Add retry logic for transient errors (network timeouts)

### 1.3 Form Validation Errors

**File:** `admin/src/lib/utils/validation.ts` (enhance)

```typescript
import { toast } from '#/lib/hooks/useToast'

export function handleValidationError(
  error: ApiError,
  form: any // React Hook Form instance
) {
  if (error.error.details) {
    // Map API validation errors to form fields
    Object.entries(error.error.details).forEach(([field, messages]) => {
      form.setError(field, {
        type: 'manual',
        message: messages[0], // Show first error
      })
    })
  } else {
    toast.error(error.error.message)
  }
}
```

**Implementation:**
- Display field-level errors from API
- Show summary errors at top of form
- Clear errors on field change
- Prevent submission with client-side validation errors

### 1.4 Error States in Lists

**Pattern for all list pages:**
```typescript
function DomainList() {
  const { domains, isLoading, error } = useDomainStore()

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <h3 className="font-semibold text-red-800">Failed to load domains</h3>
        <p className="text-sm text-red-600">{error}</p>
        <Button onClick={() => fetchDomains()} className="mt-2">
          Retry
        </Button>
      </div>
    )
  }

  // ... rest of component
}
```

**Apply to:**
- Domain list
- Mailbox list
- Queue list
- Admin users list
- All settings pages

---

## 2. Loading States & Skeleton Screens

### 2.1 Skeleton Components

**File:** `admin/src/components/ui/skeleton.tsx` (new)

```typescript
import { cn } from '#/lib/utils'

export function Skeleton({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn('animate-pulse rounded-md bg-gray-200', className)}
      {...props}
    />
  )
}

export function TableSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <div className="space-y-3">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="flex gap-4">
          <Skeleton className="h-12 w-full" />
        </div>
      ))}
    </div>
  )
}

export function CardSkeleton() {
  return (
    <div className="rounded-lg border p-6">
      <Skeleton className="mb-4 h-6 w-1/3" />
      <Skeleton className="h-4 w-full" />
      <Skeleton className="mt-2 h-4 w-2/3" />
    </div>
  )
}

export function FormSkeleton() {
  return (
    <div className="space-y-6">
      <div>
        <Skeleton className="mb-2 h-4 w-24" />
        <Skeleton className="h-10 w-full" />
      </div>
      <div>
        <Skeleton className="mb-2 h-4 w-32" />
        <Skeleton className="h-10 w-full" />
      </div>
      <div>
        <Skeleton className="mb-2 h-4 w-28" />
        <Skeleton className="h-10 w-full" />
      </div>
    </div>
  )
}
```

### 2.2 Loading States by Feature

**Dashboard:**
```typescript
function Dashboard() {
  const { metrics, isLoading } = useDashboardStore()

  if (isLoading) {
    return (
      <div className="grid grid-cols-1 gap-6 md:grid-cols-2 lg:grid-cols-4">
        {Array.from({ length: 8 }).map((_, i) => (
          <CardSkeleton key={i} />
        ))}
      </div>
    )
  }

  // ... rest
}
```

**Tables:**
```typescript
function DomainList() {
  const { domains, isLoading } = useDomainStore()

  if (isLoading) {
    return <TableSkeleton rows={10} />
  }

  // ... rest
}
```

**Forms:**
```typescript
function DomainForm() {
  const { domain, isLoading } = useDomainStore()

  if (isLoading) {
    return <FormSkeleton />
  }

  // ... rest
}
```

### 2.3 Button Loading States

**Pattern:**
```typescript
<Button disabled={isSubmitting}>
  {isSubmitting ? (
    <>
      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
      Saving...
    </>
  ) : (
    'Save'
  )}
</Button>
```

**Apply to:**
- All form submit buttons
- Delete confirmation buttons
- Bulk action buttons
- Retry/bounce queue buttons

### 2.4 Optimistic Updates

**Example for delete:**
```typescript
async function handleDelete(id: number) {
  // Optimistic update
  removeDomain(id) // Update store immediately

  try {
    await deleteDomain(id)
    toast.success('Domain deleted')
  } catch (error) {
    // Rollback on error
    fetchDomains() // Refetch to restore
    toast.error('Failed to delete domain')
  }
}
```

---

## 3. Form Validation with Zod Schemas

### 3.1 Validation Schemas

**File:** `admin/src/lib/schemas/domain.ts` (new)

```typescript
import { z } from 'zod'

export const domainSchema = z.object({
  name: z
    .string()
    .min(3, 'Domain must be at least 3 characters')
    .regex(
      /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/,
      'Invalid domain format'
    ),
  server_type: z.enum(['traditional', 'restmail'], {
    required_error: 'Server type is required',
  }),
  active: z.boolean().default(true),
  default_quota: z
    .number()
    .int()
    .positive('Quota must be positive')
    .optional(),
})

export type DomainFormData = z.infer<typeof domainSchema>
```

**File:** `admin/src/lib/schemas/mailbox.ts` (new)

```typescript
export const mailboxSchema = z.object({
  domain_id: z.number().int().positive('Domain is required'),
  local_part: z
    .string()
    .min(1, 'Username is required')
    .max(64, 'Username too long')
    .regex(/^[a-z0-9._-]+$/, 'Invalid username format'),
  display_name: z.string().optional(),
  password: z
    .string()
    .min(8, 'Password must be at least 8 characters')
    .regex(/[A-Z]/, 'Must contain uppercase letter')
    .regex(/[a-z]/, 'Must contain lowercase letter')
    .regex(/[0-9]/, 'Must contain number')
    .regex(/[^A-Za-z0-9]/, 'Must contain special character'),
  quota: z.number().int().positive().optional(),
  active: z.boolean().default(true),
})
```

**File:** `admin/src/lib/schemas/adminUser.ts` (new)

```typescript
export const adminUserSchema = z.object({
  username: z
    .string()
    .min(3, 'Username must be at least 3 characters')
    .max(32, 'Username too long')
    .regex(/^[a-z0-9_-]+$/, 'Invalid username format'),
  email: z.string().email('Invalid email').optional().or(z.literal('')),
  password: z
    .string()
    .min(12, 'Admin password must be at least 12 characters')
    .regex(/[A-Z]/, 'Must contain uppercase')
    .regex(/[a-z]/, 'Must contain lowercase')
    .regex(/[0-9]/, 'Must contain number')
    .regex(/[^A-Za-z0-9]/, 'Must contain special character'),
  role_ids: z.array(z.number()).min(1, 'At least one role required'),
})

export const updateAdminUserSchema = adminUserSchema.partial().extend({
  password: z
    .string()
    .min(12, 'Admin password must be at least 12 characters')
    .regex(/[A-Z]/, 'Must contain uppercase')
    .regex(/[a-z]/, 'Must contain lowercase')
    .regex(/[0-9]/, 'Must contain number')
    .regex(/[^A-Za-z0-9]/, 'Must contain special character')
    .optional()
    .or(z.literal('')),
})
```

### 3.2 React Hook Form Integration

**Pattern:**
```typescript
import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'

function DomainForm() {
  const form = useForm<DomainFormData>({
    resolver: zodResolver(domainSchema),
    defaultValues: {
      active: true,
      server_type: 'restmail',
    },
  })

  async function onSubmit(data: DomainFormData) {
    try {
      await createDomain(data)
      toast.success('Domain created')
      navigate('/admin/domains')
    } catch (error) {
      handleValidationError(error as ApiError, form)
    }
  }

  return (
    <form onSubmit={form.handleSubmit(onSubmit)}>
      {/* fields */}
    </form>
  )
}
```

**Apply to all forms:**
- Domain create/edit
- Mailbox create/edit
- Admin user create/edit
- Alias create/edit
- Queue filters
- Settings forms (DKIM, certificates, bans)

---

## 4. Toast Notification System

### 4.1 Toast Hook

**File:** `admin/src/lib/hooks/useToast.ts` (new)

```typescript
import { create } from 'zustand'

export interface Toast {
  id: string
  type: 'success' | 'error' | 'warning' | 'info'
  message: string
  description?: string
  duration?: number
}

interface ToastStore {
  toasts: Toast[]
  addToast: (toast: Omit<Toast, 'id'>) => void
  removeToast: (id: string) => void
}

export const useToastStore = create<ToastStore>((set) => ({
  toasts: [],
  addToast: (toast) => {
    const id = Math.random().toString(36).substring(7)
    set((state) => ({
      toasts: [...state.toasts, { ...toast, id }],
    }))

    // Auto-remove after duration
    setTimeout(() => {
      set((state) => ({
        toasts: state.toasts.filter((t) => t.id !== id),
      }))
    }, toast.duration || 5000)
  },
  removeToast: (id) =>
    set((state) => ({
      toasts: state.toasts.filter((t) => t.id !== id),
    })),
}))

export function toast(
  message: string,
  options?: Partial<Omit<Toast, 'id' | 'message'>>
) {
  useToastStore.getState().addToast({
    message,
    type: 'info',
    ...options,
  })
}

toast.success = (message: string, description?: string) => {
  useToastStore.getState().addToast({
    type: 'success',
    message,
    description,
  })
}

toast.error = (message: string, description?: string) => {
  useToastStore.getState().addToast({
    type: 'error',
    message,
    description,
  })
}

toast.warning = (message: string, description?: string) => {
  useToastStore.getState().addToast({
    type: 'warning',
    message,
    description,
  })
}

toast.info = (message: string, description?: string) => {
  useToastStore.getState().addToast({
    type: 'info',
    message,
    description,
  })
}
```

### 4.2 Toast Component

**File:** `admin/src/components/ui/toast.tsx` (new)

```typescript
import { X, CheckCircle, AlertCircle, Info, AlertTriangle } from 'lucide-react'
import { useToastStore } from '#/lib/hooks/useToast'
import { cn } from '#/lib/utils'

const icons = {
  success: CheckCircle,
  error: AlertCircle,
  warning: AlertTriangle,
  info: Info,
}

const styles = {
  success: 'bg-green-50 text-green-800 border-green-200',
  error: 'bg-red-50 text-red-800 border-red-200',
  warning: 'bg-yellow-50 text-yellow-800 border-yellow-200',
  info: 'bg-blue-50 text-blue-800 border-blue-200',
}

export function ToastContainer() {
  const { toasts, removeToast } = useToastStore()

  return (
    <div className="fixed bottom-4 right-4 z-50 space-y-2">
      {toasts.map((toast) => {
        const Icon = icons[toast.type]
        return (
          <div
            key={toast.id}
            className={cn(
              'flex items-start gap-3 rounded-lg border p-4 shadow-lg',
              styles[toast.type]
            )}
          >
            <Icon className="h-5 w-5" />
            <div className="flex-1">
              <p className="font-medium">{toast.message}</p>
              {toast.description && (
                <p className="mt-1 text-sm opacity-90">{toast.description}</p>
              )}
            </div>
            <button
              onClick={() => removeToast(toast.id)}
              className="rounded hover:opacity-75"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
        )
      })}
    </div>
  )
}
```

### 4.3 Toast Usage Examples

```typescript
// Success
toast.success('Domain created successfully')

// Error
toast.error('Failed to delete mailbox', 'Please try again')

// Warning
toast.warning('Certificate expires in 7 days')

// Info
toast.info('Queue processing paused')
```

---

## 5. Responsive Design Testing

### 5.1 Breakpoints

**Tailwind config (already in place):**
- `sm`: 640px (mobile landscape)
- `md`: 768px (tablet)
- `lg`: 1024px (desktop)
- `xl`: 1280px (large desktop)

### 5.2 Mobile-First Components

**Sidebar (responsive):**
```typescript
<aside className="fixed inset-y-0 left-0 z-50 w-64 bg-white shadow-lg lg:static lg:z-auto">
  {/* Sidebar content */}
</aside>

{/* Mobile overlay */}
<div className="fixed inset-0 z-40 bg-black/50 lg:hidden" />
```

**Tables (responsive):**
```typescript
<div className="overflow-x-auto">
  <table className="min-w-full">
    {/* table content */}
  </table>
</div>

{/* Alternative: Card layout on mobile */}
<div className="block lg:hidden">
  {items.map((item) => (
    <div key={item.id} className="rounded-lg border p-4">
      {/* card layout */}
    </div>
  ))}
</div>
```

**Forms (stack on mobile):**
```typescript
<div className="grid grid-cols-1 gap-6 md:grid-cols-2">
  <div>{/* field 1 */}</div>
  <div>{/* field 2 */}</div>
</div>
```

### 5.3 Testing Checklist

**Test on:**
- [ ] iPhone SE (375px)
- [ ] iPhone 14 Pro (390px)
- [ ] iPad Mini (768px)
- [ ] iPad Pro (1024px)
- [ ] Desktop (1920px)

**Test scenarios:**
- [ ] Login page
- [ ] Dashboard (metric cards stack)
- [ ] Domain list (table scrolls or cards)
- [ ] Domain form (fields stack)
- [ ] Queue list with filters
- [ ] Sidebar collapses on mobile
- [ ] Modals fit on mobile
- [ ] Toast notifications positioned correctly

---

## 6. Accessibility Audit (WCAG 2.1 AA)

### 6.1 Semantic HTML

**Requirements:**
- Use semantic tags (`<header>`, `<nav>`, `<main>`, `<aside>`, `<footer>`)
- Proper heading hierarchy (h1 → h2 → h3)
- Use `<button>` for actions, `<a>` for navigation
- Add `<label>` for all form inputs
- Use `<table>` with `<thead>`, `<tbody>`, `<th>` for data tables

**Example:**
```typescript
<main role="main" aria-labelledby="page-title">
  <header>
    <h1 id="page-title">Domain Management</h1>
  </header>

  <section aria-labelledby="domain-list">
    <h2 id="domain-list">Active Domains</h2>
    {/* content */}
  </section>
</main>
```

### 6.2 ARIA Attributes

**Required:**
- `aria-label` for icon-only buttons
- `aria-labelledby` for sections
- `aria-describedby` for help text
- `aria-expanded` for collapsible elements
- `aria-selected` for tabs
- `aria-busy` for loading states
- `aria-live` for dynamic content

**Example:**
```typescript
<button
  onClick={handleDelete}
  aria-label="Delete domain"
  aria-describedby="delete-help"
>
  <Trash2 className="h-4 w-4" />
</button>
<span id="delete-help" className="sr-only">
  This action cannot be undone
</span>
```

### 6.3 Focus Management

**Requirements:**
- Visible focus indicators (outline or ring)
- Focus trap in modals
- Focus restoration after modal close
- Skip to main content link

**Focus styles:**
```css
/* Add to global CSS */
*:focus-visible {
  @apply outline-2 outline-offset-2 outline-blue-500;
}
```

**Modal focus trap:**
```typescript
import { useEffect, useRef } from 'react'

function Modal({ isOpen, onClose, children }) {
  const modalRef = useRef<HTMLDivElement>(null)
  const previousFocus = useRef<HTMLElement | null>(null)

  useEffect(() => {
    if (isOpen) {
      previousFocus.current = document.activeElement as HTMLElement
      modalRef.current?.focus()
    } else {
      previousFocus.current?.focus()
    }
  }, [isOpen])

  return (
    <div ref={modalRef} tabIndex={-1} role="dialog" aria-modal="true">
      {children}
    </div>
  )
}
```

### 6.4 Color Contrast

**Requirements:**
- Text: 4.5:1 contrast ratio (WCAG AA)
- Large text: 3:1 contrast ratio
- UI components: 3:1 contrast ratio

**Testing:**
- Use Chrome DevTools Lighthouse
- WebAIM Contrast Checker
- axe DevTools extension

**Fix issues:**
```css
/* Bad: low contrast */
.text-gray-400 on .bg-gray-100

/* Good: sufficient contrast */
.text-gray-700 on .bg-white
.text-white on .bg-blue-600
```

### 6.5 Screen Reader Support

**Best practices:**
- Use descriptive link text (not "click here")
- Provide alt text for icons used as content
- Announce dynamic changes with `aria-live`
- Hide decorative icons with `aria-hidden="true"`

**Example:**
```typescript
{/* Good: descriptive */}
<a href="/domains/123">Edit mail1.test domain</a>

{/* Bad: non-descriptive */}
<a href="/domains/123">Click here</a>

{/* Icon with meaning */}
<AlertCircle aria-label="Warning" />

{/* Decorative icon */}
<ChevronRight aria-hidden="true" />
```

### 6.6 Accessibility Testing Checklist

**Automated testing:**
- [ ] Run axe DevTools on all pages
- [ ] Run Lighthouse accessibility audit (score > 90)
- [ ] Fix all critical issues

**Manual testing:**
- [ ] Navigate entire app with keyboard only
- [ ] Test with screen reader (NVDA on Windows, VoiceOver on Mac)
- [ ] Verify all form fields have labels
- [ ] Check all interactive elements are focusable
- [ ] Verify focus order is logical
- [ ] Test high contrast mode
- [ ] Test at 200% zoom

---

## 7. Keyboard Navigation Testing

### 7.1 Keyboard Shortcuts

**Global shortcuts:**
- `Tab` / `Shift+Tab`: Navigate forward/backward
- `Enter`: Activate button/link
- `Space`: Toggle checkbox/switch
- `Esc`: Close modal/dropdown
- `Arrow keys`: Navigate lists/menus
- `/`: Focus search (optional)

### 7.2 Focus Indicators

**Ensure visible focus on:**
- All buttons
- All links
- All form inputs
- Table rows (if clickable)
- Dropdown menu items
- Modal close button

### 7.3 Tab Order

**Verify logical tab order:**
1. Skip to main content
2. Logo/home link
3. Navigation menu items
4. Page title
5. Primary action button
6. Search input
7. Filter controls
8. Table/list content
9. Pagination controls
10. Footer links

### 7.4 Modal Keyboard Behavior

**Requirements:**
- Focus trapped inside modal
- `Esc` closes modal
- `Tab` cycles through modal elements only
- Focus returns to trigger element on close

### 7.5 Testing Checklist

- [ ] Navigate login page with keyboard
- [ ] Navigate dashboard with keyboard
- [ ] Open domain form with keyboard
- [ ] Fill and submit form with keyboard
- [ ] Open dropdown menu with keyboard
- [ ] Navigate table rows with keyboard
- [ ] Open modal with keyboard
- [ ] Close modal with Esc
- [ ] Navigate sidebar with keyboard
- [ ] Verify skip to main content works

---

## 8. End-to-End Testing with Playwright

### 8.1 Setup Playwright

**Install:**
```bash
cd admin
npm install -D @playwright/test
npx playwright install
```

**File:** `admin/playwright.config.ts` (new)

```typescript
import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: 'html',
  use: {
    baseURL: 'http://localhost:3000',
    trace: 'on-first-retry',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'firefox',
      use: { ...devices['Desktop Firefox'] },
    },
    {
      name: 'webkit',
      use: { ...devices['Desktop Safari'] },
    },
  ],
  webServer: {
    command: 'npm run dev',
    url: 'http://localhost:3000',
    reuseExistingServer: !process.env.CI,
  },
})
```

### 8.2 Test Utilities

**File:** `admin/e2e/utils/auth.ts` (new)

```typescript
import { Page } from '@playwright/test'

export async function login(page: Page) {
  await page.goto('/admin/login')
  await page.fill('input[name="username"]', 'admin')
  await page.fill('input[name="password"]', 'admin123!@')
  await page.click('button[type="submit"]')
  await page.waitForURL('/admin/dashboard')
}

export async function logout(page: Page) {
  await page.click('[aria-label="User menu"]')
  await page.click('text=Logout')
  await page.waitForURL('/admin/login')
}
```

**File:** `admin/e2e/utils/test-data.ts` (new)

```typescript
export const TEST_DOMAIN = {
  name: 'test-e2e.local',
  server_type: 'restmail',
  active: true,
}

export const TEST_MAILBOX = {
  local_part: 'testuser',
  display_name: 'Test User',
  password: 'Test123!@#',
  quota: 1073741824, // 1GB
}

export const TEST_ADMIN_USER = {
  username: 'testadmin',
  email: 'test@example.com',
  password: 'TestAdmin123!@',
}
```

### 8.3 Critical User Flow Tests

**File:** `admin/e2e/auth.spec.ts` (new)

```typescript
import { test, expect } from '@playwright/test'

test.describe('Authentication', () => {
  test('should login with valid credentials', async ({ page }) => {
    await page.goto('/admin/login')

    await page.fill('input[name="username"]', 'admin')
    await page.fill('input[name="password"]', 'admin123!@')
    await page.click('button[type="submit"]')

    await expect(page).toHaveURL('/admin/dashboard')
    await expect(page.locator('h1')).toContainText('Dashboard')
  })

  test('should show error with invalid credentials', async ({ page }) => {
    await page.goto('/admin/login')

    await page.fill('input[name="username"]', 'admin')
    await page.fill('input[name="password"]', 'wrongpassword')
    await page.click('button[type="submit"]')

    await expect(page.locator('text=Invalid credentials')).toBeVisible()
  })

  test('should logout successfully', async ({ page }) => {
    await login(page)

    await page.click('[aria-label="User menu"]')
    await page.click('text=Logout')

    await expect(page).toHaveURL('/admin/login')
  })
})
```

**File:** `admin/e2e/domains.spec.ts` (new)

```typescript
import { test, expect } from '@playwright/test'
import { login } from './utils/auth'
import { TEST_DOMAIN } from './utils/test-data'

test.describe('Domain Management', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
    await page.goto('/admin/domains')
  })

  test('should list domains', async ({ page }) => {
    await expect(page.locator('h1')).toContainText('Domains')
    await expect(page.locator('table tbody tr').first()).toBeVisible()
  })

  test('should create new domain', async ({ page }) => {
    await page.click('text=New Domain')

    await page.fill('input[name="name"]', TEST_DOMAIN.name)
    await page.selectOption('select[name="server_type"]', TEST_DOMAIN.server_type)
    await page.click('button[type="submit"]')

    await expect(page.locator('text=Domain created')).toBeVisible()
    await expect(page).toHaveURL('/admin/domains')
    await expect(page.locator(`text=${TEST_DOMAIN.name}`)).toBeVisible()
  })

  test('should edit domain', async ({ page }) => {
    // Click first domain row
    await page.click('table tbody tr:first-child')

    // Toggle active status
    await page.click('input[name="active"]')
    await page.click('button[type="submit"]')

    await expect(page.locator('text=Domain updated')).toBeVisible()
  })

  test('should delete domain', async ({ page }) => {
    // Click delete on first domain
    await page.click('table tbody tr:first-child button[aria-label="Delete"]')

    // Confirm deletion
    await page.click('text=Confirm')

    await expect(page.locator('text=Domain deleted')).toBeVisible()
  })

  test('should search domains', async ({ page }) => {
    await page.fill('input[placeholder="Search domains"]', 'mail1')

    await expect(page.locator('table tbody tr')).toHaveCount(1)
    await expect(page.locator('text=mail1.test')).toBeVisible()
  })
})
```

**File:** `admin/e2e/mailboxes.spec.ts` (new)

```typescript
import { test, expect } from '@playwright/test'
import { login } from './utils/auth'
import { TEST_MAILBOX } from './utils/test-data'

test.describe('Mailbox Management', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
    await page.goto('/admin/mailboxes')
  })

  test('should create new mailbox', async ({ page }) => {
    await page.click('text=New Mailbox')

    await page.selectOption('select[name="domain_id"]', { label: 'mail3.test' })
    await page.fill('input[name="local_part"]', TEST_MAILBOX.local_part)
    await page.fill('input[name="display_name"]', TEST_MAILBOX.display_name)
    await page.fill('input[name="password"]', TEST_MAILBOX.password)
    await page.fill('input[name="quota"]', String(TEST_MAILBOX.quota))
    await page.click('button[type="submit"]')

    await expect(page.locator('text=Mailbox created')).toBeVisible()
  })

  test('should filter mailboxes by domain', async ({ page }) => {
    await page.selectOption('select[name="domain_filter"]', { label: 'mail1.test' })

    // All visible mailboxes should be from mail1.test
    const rows = page.locator('table tbody tr')
    await expect(rows.first()).toContainText('@mail1.test')
  })

  test('should reset mailbox password', async ({ page }) => {
    await page.click('table tbody tr:first-child')

    await page.fill('input[name="password"]', 'NewPassword123!@')
    await page.click('button[type="submit"]')

    await expect(page.locator('text=Password updated')).toBeVisible()
  })
})
```

**File:** `admin/e2e/queue.spec.ts` (new)

```typescript
import { test, expect } from '@playwright/test'
import { login } from './utils/auth'

test.describe('Queue Management', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
    await page.goto('/admin/queue')
  })

  test('should display queue metrics', async ({ page }) => {
    await expect(page.locator('text=Pending')).toBeVisible()
    await expect(page.locator('text=Delivering')).toBeVisible()
    await expect(page.locator('text=Deferred')).toBeVisible()
  })

  test('should filter queue by status', async ({ page }) => {
    await page.selectOption('select[name="status"]', 'deferred')

    // Wait for filter to apply
    await page.waitForTimeout(500)

    const rows = page.locator('table tbody tr')
    if (await rows.count() > 0) {
      await expect(rows.first()).toContainText('deferred')
    }
  })

  test('should retry queue entry', async ({ page }) => {
    // Find a deferred entry
    await page.selectOption('select[name="status"]', 'deferred')

    const retryButton = page.locator('button[aria-label="Retry"]').first()
    if (await retryButton.isVisible()) {
      await retryButton.click()
      await expect(page.locator('text=Retry initiated')).toBeVisible()
    }
  })

  test('should bulk select and retry', async ({ page }) => {
    // Select first 3 entries
    await page.click('input[type="checkbox"]#select-all')

    await page.click('text=Retry Selected')
    await page.click('text=Confirm')

    await expect(page.locator('text=Bulk retry initiated')).toBeVisible()
  })
})
```

**File:** `admin/e2e/admin-users.spec.ts` (new)

```typescript
import { test, expect } from '@playwright/test'
import { login } from './utils/auth'
import { TEST_ADMIN_USER } from './utils/test-data'

test.describe('Admin User Management', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
    await page.goto('/admin/admin-users')
  })

  test('should create admin user with roles', async ({ page }) => {
    await page.click('text=New Admin User')

    await page.fill('input[name="username"]', TEST_ADMIN_USER.username)
    await page.fill('input[name="email"]', TEST_ADMIN_USER.email)
    await page.fill('input[name="password"]', TEST_ADMIN_USER.password)

    // Select admin role
    await page.click('label:has-text("admin")')

    await page.click('button[type="submit"]')

    await expect(page.locator('text=Admin user created')).toBeVisible()
  })

  test('should update admin user roles', async ({ page }) => {
    await page.click('table tbody tr:first-child')

    // Toggle readonly role
    await page.click('label:has-text("readonly")')

    await page.click('button[type="submit"]')

    await expect(page.locator('text=Admin user updated')).toBeVisible()
  })
})
```

### 8.4 Test Data Setup Strategy

**Approach 1: Seed database before tests**

**File:** `admin/e2e/global-setup.ts` (new)

```typescript
import { chromium } from '@playwright/test'

export default async function globalSetup() {
  // Run seed script
  const response = await fetch('http://localhost:8080/api/test/seed', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: 'Bearer test-token',
    },
  })

  if (!response.ok) {
    throw new Error('Failed to seed test data')
  }
}
```

**Approach 2: Create test data in beforeEach**

```typescript
test.beforeEach(async ({ request }) => {
  // Create test domain
  await request.post('/api/v1/admin/domains', {
    data: TEST_DOMAIN,
    headers: {
      Authorization: `Bearer ${process.env.TEST_TOKEN}`,
    },
  })
})

test.afterEach(async ({ request }) => {
  // Cleanup test data
  await request.delete(`/api/v1/admin/domains/${testDomainId}`)
})
```

**Approach 3: Use API mocking (MSW)**

```typescript
import { setupServer } from 'msw/node'
import { rest } from 'msw'

const server = setupServer(
  rest.get('/api/v1/admin/domains', (req, res, ctx) => {
    return res(ctx.json([TEST_DOMAIN]))
  })
)

test.beforeAll(() => server.listen())
test.afterAll(() => server.close())
```

### 8.5 Running Tests

**Add to package.json:**
```json
{
  "scripts": {
    "test:e2e": "playwright test",
    "test:e2e:ui": "playwright test --ui",
    "test:e2e:debug": "playwright test --debug"
  }
}
```

**Run:**
```bash
npm run test:e2e
npm run test:e2e:ui  # Interactive mode
npm run test:e2e:debug  # Debug mode
```

---

## 9. Performance Optimization

### 9.1 Bundle Size Analysis

**Install:**
```bash
npm install -D vite-plugin-bundle-analyzer
```

**File:** `admin/vite.config.ts` (enhance)

```typescript
import { visualizer } from 'rollup-plugin-visualizer'

export default defineConfig({
  plugins: [
    // ... existing plugins
    visualizer({
      open: true,
      filename: 'dist/stats.html',
    }),
  ],
})
```

**Run:**
```bash
npm run build
# Opens stats.html in browser
```

**Targets:**
- Total bundle size < 500 KB gzipped
- Initial load < 200 KB
- Lazy load routes and heavy components

### 9.2 Code Splitting Strategy

**Route-based splitting (automatic with TanStack Router):**
```typescript
// Already handled by TanStack Router
// Each route is a separate chunk
```

**Component lazy loading:**
```typescript
import { lazy, Suspense } from 'react'

const HeavyChart = lazy(() => import('#/components/HeavyChart'))

function Dashboard() {
  return (
    <Suspense fallback={<Skeleton className="h-64" />}>
      <HeavyChart />
    </Suspense>
  )
}
```

**Vendor chunking:**
```typescript
// vite.config.ts
export default defineConfig({
  build: {
    rollupOptions: {
      output: {
        manualChunks: {
          'react-vendor': ['react', 'react-dom'],
          'router-vendor': ['@tanstack/react-router'],
          'ui-vendor': ['lucide-react', 'recharts'],
        },
      },
    },
  },
})
```

### 9.3 Image Optimization

**Use modern formats:**
```typescript
<img
  src="/logo.avif"
  srcSet="/logo.avif 1x, /logo@2x.avif 2x"
  alt="REST Mail"
  loading="lazy"
/>
```

**Optimize images:**
```bash
# Install image optimizer
npm install -D vite-plugin-imagemin

# vite.config.ts
import viteImagemin from 'vite-plugin-imagemin'

export default defineConfig({
  plugins: [
    viteImagemin({
      gifsicle: { optimizationLevel: 7 },
      optipng: { optimizationLevel: 7 },
      mozjpeg: { quality: 80 },
      pngquant: { quality: [0.8, 0.9] },
      svgo: {
        plugins: [{ name: 'removeViewBox' }, { name: 'removeEmptyAttrs' }],
      },
    }),
  ],
})
```

### 9.4 Caching Strategy

**API response caching:**
```typescript
// Already handled by Zustand stores
// Cache domains, mailboxes, etc. in memory
```

**Service worker (optional):**
```typescript
// public/sw.js
self.addEventListener('fetch', (event) => {
  if (event.request.url.includes('/api/')) {
    // Network-first for API requests
    event.respondWith(
      fetch(event.request).catch(() => caches.match(event.request))
    )
  } else {
    // Cache-first for static assets
    event.respondWith(
      caches.match(event.request).then((response) => {
        return response || fetch(event.request)
      })
    )
  }
})
```

### 9.5 Performance Metrics

**Add web vitals tracking:**
```typescript
// File: admin/src/lib/analytics.ts
import { onCLS, onFID, onFCP, onLCP, onTTFB } from 'web-vitals'

function sendToAnalytics(metric) {
  console.log(metric)
  // TODO: Send to analytics service
}

onCLS(sendToAnalytics)
onFID(sendToAnalytics)
onFCP(sendToAnalytics)
onLCP(sendToAnalytics)
onTTFB(sendToAnalytics)
```

**Targets:**
- LCP (Largest Contentful Paint) < 2.5s
- FID (First Input Delay) < 100ms
- CLS (Cumulative Layout Shift) < 0.1
- TTFB (Time to First Byte) < 800ms

---

## 10. Security Hardening

### 10.1 XSS Protection

**Content Security Policy (CSP):**

**File:** `admin/index.html` (add meta tag)

```html
<meta
  http-equiv="Content-Security-Policy"
  content="
    default-src 'self';
    script-src 'self' 'unsafe-inline' 'unsafe-eval';
    style-src 'self' 'unsafe-inline';
    img-src 'self' data: https:;
    font-src 'self' data:;
    connect-src 'self' http://localhost:8080;
    frame-ancestors 'none';
    base-uri 'self';
    form-action 'self';
  "
/>
```

**Or via HTTP header (recommended):**

**File:** Update backend to serve CSP header for `/admin` route

```go
// internal/api/middleware/security.go
func SecurityHeaders() func(http.Handler) http.Handler {
  return func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
      w.Header().Set("Content-Security-Policy",
        "default-src 'self'; "+
        "script-src 'self'; "+
        "style-src 'self' 'unsafe-inline'; "+
        "img-src 'self' data: https:; "+
        "connect-src 'self'; "+
        "frame-ancestors 'none'")

      w.Header().Set("X-Content-Type-Options", "nosniff")
      w.Header().Set("X-Frame-Options", "DENY")
      w.Header().Set("X-XSS-Protection", "1; mode=block")
      w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

      next.ServeHTTP(w, r)
    })
  }
}
```

**Sanitize user input:**
```typescript
// Never use dangerouslySetInnerHTML
// If necessary, sanitize with DOMPurify:
import DOMPurify from 'dompurify'

function SafeHTML({ html }: { html: string }) {
  const clean = DOMPurify.sanitize(html)
  return <div dangerouslySetInnerHTML={{ __html: clean }} />
}
```

### 10.2 CSRF Protection

**Backend implementation (already in place):**
- Use SameSite cookies for refresh tokens
- Require Authorization header for API calls
- Validate token origin

**Frontend:**
```typescript
// apiClient already sends tokens in headers (not cookies)
// This prevents CSRF attacks
```

### 10.3 Input Validation

**Client-side (Zod schemas already cover this):**
```typescript
// All forms use Zod validation
// Prevent script injection in inputs
const domainSchema = z.object({
  name: z.string().regex(/^[a-z0-9.-]+$/) // Only safe chars
})
```

**Server-side (ensure backend validates):**
- All API endpoints must validate input
- Use same validation rules as frontend
- Return 422 for validation errors

### 10.4 Rate Limiting

**Backend (enhance existing):**
```go
// internal/api/middleware/ratelimit.go
func AdminRateLimit() func(http.Handler) http.Handler {
  limiter := rate.NewLimiter(rate.Every(time.Second), 10) // 10 req/sec

  return func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
      if !limiter.Allow() {
        http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
        return
      }
      next.ServeHTTP(w, r)
    })
  }
}
```

**Frontend (show error):**
```typescript
if (error.status === 429) {
  toast.error('Too many requests. Please slow down.')
}
```

### 10.5 Secure Cookie Settings

**Backend (already implemented):**
```go
http.SetCookie(w, &http.Cookie{
  Name:     "refresh_token",
  Value:    refreshToken,
  HttpOnly: true,
  Secure:   true, // HTTPS only
  SameSite: http.SameSiteStrictMode,
  Path:     "/api/v1/auth/refresh",
  MaxAge:   7 * 24 * 60 * 60, // 7 days
})
```

### 10.6 Security Headers

**Add to backend:**
```go
w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
```

---

## 11. Documentation

### 11.1 User Guide

**File:** `admin/docs/USER_GUIDE.md` (new)

**Sections:**
1. **Getting Started**
   - Logging in
   - Dashboard overview
   - Navigation

2. **Domain Management**
   - Creating a domain
   - Configuring DNS records
   - Managing DKIM keys
   - Domain sender rules

3. **Mailbox Management**
   - Creating mailboxes
   - Setting quotas
   - Resetting passwords
   - Bulk operations

4. **Alias Management**
   - Creating aliases
   - Forwarding rules
   - Catch-all configuration

5. **Queue Management**
   - Understanding queue states
   - Retrying deliveries
   - Bouncing messages
   - Bulk operations

6. **Admin User Management**
   - Creating admin users
   - Assigning roles
   - Understanding capabilities
   - Security best practices

7. **Settings**
   - DKIM configuration
   - SSL/TLS certificates
   - IP ban management
   - TLS-RPT reports

8. **Troubleshooting**
   - Common issues
   - Error messages
   - Getting support

### 11.2 API Documentation

**File:** `admin/docs/API_INTEGRATION.md` (new)

**Sections:**
1. **Authentication**
   - Login flow
   - Token refresh
   - Logout

2. **API Endpoints**
   - List all endpoints used by admin
   - Request/response examples
   - Error codes

3. **Rate Limiting**
   - Limits per endpoint
   - Handling 429 errors

4. **Webhooks** (if applicable)
   - Event types
   - Payload format

### 11.3 Deployment Guide

**File:** `admin/docs/DEPLOYMENT.md` (new)

**Sections:**
1. **Prerequisites**
   - Node.js version
   - Build tools
   - Environment variables

2. **Build for Production**
   ```bash
   cd admin
   npm install
   npm run build
   # Output: dist/
   ```

3. **Deployment Options**

   **Option A: Same server as API**
   ```bash
   # Copy build to public directory
   cp -r admin/dist/* /var/www/admin/

   # Configure nginx
   server {
     listen 80;
     server_name admin.restmail.test;
     root /var/www/admin;

     location / {
       try_files $uri $uri/ /index.html;
     }

     location /api {
       proxy_pass http://localhost:8080;
     }
   }
   ```

   **Option B: Docker container**
   ```dockerfile
   # Dockerfile (already exists in admin/)
   FROM node:20-alpine AS builder
   WORKDIR /app
   COPY package*.json ./
   RUN npm ci
   COPY . .
   RUN npm run build

   FROM nginx:alpine
   COPY --from=builder /app/dist /usr/share/nginx/html
   COPY nginx.conf /etc/nginx/conf.d/default.conf
   EXPOSE 80
   CMD ["nginx", "-g", "daemon off;"]
   ```

   **Option C: Vercel/Netlify**
   - Connect GitHub repo
   - Set build command: `npm run build`
   - Set output directory: `dist`
   - Add environment variables

4. **Environment Variables**
   ```bash
   VITE_API_URL=https://api.restmail.com/api/v1
   ```

5. **Health Checks**
   - Verify `/admin` loads
   - Check API connectivity
   - Test authentication flow

6. **Monitoring**
   - Set up error tracking (Sentry)
   - Configure uptime monitoring
   - Enable performance monitoring

### 11.4 Component Documentation

**Add JSDoc comments to components:**

```typescript
/**
 * Domain list component with search, filter, and CRUD operations.
 *
 * @example
 * ```tsx
 * <DomainList />
 * ```
 */
export function DomainList() {
  // ...
}

/**
 * Reusable table skeleton loader.
 *
 * @param rows - Number of skeleton rows to display (default: 5)
 *
 * @example
 * ```tsx
 * <TableSkeleton rows={10} />
 * ```
 */
export function TableSkeleton({ rows = 5 }: { rows?: number }) {
  // ...
}
```

---

## 12. Production Deployment Checklist

### 12.1 Pre-Deployment

**Code Quality:**
- [ ] All TypeScript errors resolved (`npm run typecheck`)
- [ ] All ESLint warnings fixed
- [ ] All console.log statements removed
- [ ] No hardcoded credentials or API keys
- [ ] Environment variables configured

**Testing:**
- [ ] All E2E tests passing
- [ ] Manual testing on staging environment
- [ ] Accessibility audit score > 90
- [ ] Performance audit score > 90
- [ ] Cross-browser testing (Chrome, Firefox, Safari)
- [ ] Mobile responsive testing

**Security:**
- [ ] CSP headers configured
- [ ] HTTPS enabled
- [ ] Secure cookies configured
- [ ] Rate limiting enabled
- [ ] Input validation on all forms
- [ ] XSS protection verified

**Performance:**
- [ ] Bundle size < 500 KB gzipped
- [ ] Images optimized
- [ ] Code splitting implemented
- [ ] Lazy loading for heavy components
- [ ] Service worker configured (optional)

**Documentation:**
- [ ] User guide complete
- [ ] API documentation up to date
- [ ] Deployment guide tested
- [ ] Component documentation added
- [ ] README updated

### 12.2 Deployment Steps

**Build:**
```bash
cd admin
npm install
npm run build
npm run test:e2e
```

**Deploy:**
```bash
# Option 1: Copy to server
scp -r dist/* user@server:/var/www/admin/

# Option 2: Docker
docker build -t restmail-admin:latest .
docker push restmail-admin:latest

# Option 3: Git push (Vercel/Netlify)
git push origin main
```

**Verify:**
- [ ] Admin website loads at https://admin.restmail.com
- [ ] API calls succeed
- [ ] Login works
- [ ] All routes accessible
- [ ] Error tracking receiving events
- [ ] Metrics dashboard showing data

### 12.3 Post-Deployment

**Monitoring:**
- [ ] Set up uptime monitoring (UptimeRobot, Pingdom)
- [ ] Configure error alerts (Sentry, email)
- [ ] Monitor performance metrics
- [ ] Watch error logs

**User Acceptance:**
- [ ] Admin team training
- [ ] User guide shared
- [ ] Feedback collection process
- [ ] Support channel established

**Maintenance:**
- [ ] Schedule regular updates
- [ ] Monitor dependency vulnerabilities
- [ ] Plan for feature requests
- [ ] Establish backup strategy

---

## 13. Success Metrics

### 13.1 Technical Metrics

**Performance:**
- [ ] Lighthouse score > 90 (all categories)
- [ ] Bundle size < 500 KB gzipped
- [ ] LCP < 2.5s
- [ ] FID < 100ms
- [ ] CLS < 0.1

**Reliability:**
- [ ] Uptime > 99.9%
- [ ] API error rate < 1%
- [ ] Zero critical bugs in production
- [ ] E2E test pass rate > 95%

**Security:**
- [ ] Zero high-severity vulnerabilities
- [ ] CSP violations < 0.1%
- [ ] Failed auth attempts logged
- [ ] Rate limiting effective

### 13.2 User Experience Metrics

**Usability:**
- [ ] Task completion rate > 95%
- [ ] Average task completion time < 2 minutes
- [ ] User satisfaction score > 4/5
- [ ] Support tickets < 5/week

**Accessibility:**
- [ ] WCAG 2.1 AA compliant
- [ ] Keyboard navigation 100% functional
- [ ] Screen reader compatible
- [ ] Color contrast ratio > 4.5:1

---

## 14. Timeline

**Week 1: Error Handling & Loading States**
- Days 1-2: Implement error boundaries and API error handling
- Days 3-4: Add loading states and skeleton screens
- Day 5: Add toast notifications

**Week 2: Form Validation & Responsive Design**
- Days 1-2: Create Zod schemas for all forms
- Day 3: Implement form validation
- Days 4-5: Responsive design testing and fixes

**Week 3: Accessibility & Keyboard Navigation**
- Days 1-2: Accessibility audit and fixes
- Day 3: Keyboard navigation testing
- Days 4-5: Screen reader compatibility

**Week 4: E2E Testing**
- Days 1-2: Set up Playwright and write auth tests
- Days 3-4: Write domain, mailbox, queue tests
- Day 5: Write admin user tests

**Week 5: Performance & Security**
- Days 1-2: Bundle size optimization
- Day 3: Image optimization
- Days 4-5: Security hardening (CSP, XSS protection)

**Week 6: Documentation & Deployment**
- Days 1-2: Write user guide
- Day 3: Write deployment guide
- Days 4-5: Production deployment and testing

---

## 15. Blockers & Dependencies

**Backend Dependencies:**
- [ ] Admin user API endpoints implemented (Stage 6)
- [ ] Pipeline/filter API endpoints implemented (Stage 5)
- [ ] Alias API endpoints implemented (Stage 3)
- [ ] Settings API endpoints implemented (Stage 7)

**Frontend Dependencies:**
- [ ] All core features complete (Stages 1-7)
- [ ] Design system finalized
- [ ] API client stable

**Infrastructure Dependencies:**
- [ ] Staging environment available
- [ ] Production environment ready
- [ ] CI/CD pipeline configured
- [ ] Monitoring tools set up

---

## 16. Risk Mitigation

**Risk: E2E tests flaky**
- Mitigation: Add retry logic, use stable selectors, increase timeouts

**Risk: Bundle size too large**
- Mitigation: Code splitting, lazy loading, remove unused dependencies

**Risk: Accessibility issues discovered late**
- Mitigation: Run automated audits weekly, manual testing throughout development

**Risk: Security vulnerabilities**
- Mitigation: Regular dependency updates, security audits, penetration testing

**Risk: Performance regressions**
- Mitigation: Performance budget, automated Lighthouse CI, real user monitoring

---

## 17. Definition of Done

Stage 8 is complete when:

1. **Error Handling:**
   - [ ] Error boundaries on all major components
   - [ ] API errors handled gracefully
   - [ ] Validation errors displayed clearly
   - [ ] Retry mechanisms in place

2. **Loading States:**
   - [ ] Skeleton screens on all data-fetching components
   - [ ] Button loading states on all forms
   - [ ] Optimistic updates where applicable

3. **Form Validation:**
   - [ ] Zod schemas for all forms
   - [ ] Client-side validation working
   - [ ] Server-side validation errors mapped to fields

4. **Toast Notifications:**
   - [ ] Toast system implemented
   - [ ] Success/error/warning/info toasts working
   - [ ] Toasts auto-dismiss after 5 seconds

5. **Responsive Design:**
   - [ ] Mobile (375px) tested
   - [ ] Tablet (768px) tested
   - [ ] Desktop (1920px) tested
   - [ ] All breakpoints working

6. **Accessibility:**
   - [ ] WCAG 2.1 AA compliant
   - [ ] Lighthouse accessibility score > 90
   - [ ] Keyboard navigation 100% functional
   - [ ] Screen reader compatible

7. **E2E Testing:**
   - [ ] Auth flow tests passing
   - [ ] Domain CRUD tests passing
   - [ ] Mailbox CRUD tests passing
   - [ ] Queue management tests passing
   - [ ] Admin user tests passing
   - [ ] Test coverage > 80% for critical flows

8. **Performance:**
   - [ ] Lighthouse performance score > 90
   - [ ] Bundle size < 500 KB gzipped
   - [ ] Web Vitals within targets
   - [ ] Code splitting implemented

9. **Security:**
   - [ ] CSP headers configured
   - [ ] XSS protection verified
   - [ ] CSRF protection in place
   - [ ] Rate limiting enabled
   - [ ] Input sanitization working

10. **Documentation:**
    - [ ] User guide complete
    - [ ] API documentation complete
    - [ ] Deployment guide complete
    - [ ] Component documentation added

11. **Production Deployment:**
    - [ ] Deployed to production environment
    - [ ] Health checks passing
    - [ ] Monitoring configured
    - [ ] Error tracking active
    - [ ] User acceptance testing complete

---

## 18. Next Steps After Completion

1. **Monitor production metrics** for first week
2. **Collect user feedback** and create improvement backlog
3. **Plan Stage 9** (if needed): Advanced features, analytics, reporting
4. **Regular maintenance:**
   - Weekly dependency updates
   - Monthly security audits
   - Quarterly performance reviews
5. **Continuous improvement:**
   - A/B testing for UX improvements
   - Feature flags for gradual rollouts
   - User feedback integration

---

**Plan Created:** 2026-02-23
**Status:** NOT STARTED
**Estimated Completion:** 6 weeks from start
**Owner:** Development Team
**Reviewers:** Product, Design, Security teams
