# Admin Users Routes

This directory contains the admin user management routes for the REST Mail admin interface.

## Routes

### `/admin-users/` (index.tsx)
- **Purpose:** List all admin users
- **Features:**
  - Search by username or email
  - Display user roles with colored badges
  - Show active/inactive status
  - Delete confirmation dialog
  - Edit and delete actions per user
- **Layout:** Uses AppShell with Swiss Clean design

### `/admin-users/new` (new.tsx)
- **Purpose:** Create a new admin user
- **Features:**
  - Username and email fields
  - Password with confirmation
  - Role selection (multi-select checkboxes)
  - Form validation
  - Error handling
- **Validations:**
  - Username: Required, minimum 3 characters
  - Email: Optional, valid email format
  - Password: Required, minimum 8 characters
  - Password confirmation: Must match password

### `/admin-users/$id` ($id.tsx)
- **Purpose:** Edit an existing admin user
- **Features:**
  - Display user info (username, created date, last password change)
  - Update email
  - Optional password reset
  - Toggle active status
  - Toggle password change required
  - Manage role assignments
  - View computed capabilities from roles
- **Layout:** Uses AppShell with form sections

## Store

### `adminUserStore.ts`
- **State:**
  - `adminUsers`: Array of admin users
  - `roles`: Available roles
  - `capabilities`: Available capabilities
  - `isLoading`: Loading state
  - `error`: Error message

- **Actions:**
  - `fetchAdminUsers()`: Load all admin users
  - `fetchRoles()`: Load available roles
  - `fetchCapabilities()`: Load available capabilities
  - `createAdminUser(data)`: Create a new admin user
  - `updateAdminUser(id, data)`: Update an existing admin user
  - `deleteAdminUser(id)`: Delete an admin user
  - `setError(error)`: Set error message
  - `clearError()`: Clear error message

## Design System

### Role Badge Colors
- **Superadmin:** Red (`#E42313`)
- **Admin:** Gray (`#7A7A7A`)
- **Readonly:** Success Green (`#22C55E`)
- **Default:** Muted Gray (`#B0B0B0`)

### Status Colors
- **Active:** Green background with dark green text
- **Inactive:** Red background with dark red text

## API Integration

The admin user routes use the following API endpoints:

- `GET /api/admin/admin-users` - List all admin users
- `POST /api/admin/admin-users` - Create a new admin user
- `GET /api/admin/admin-users/:id` - Get admin user details
- `PUT /api/admin/admin-users/:id` - Update an admin user
- `DELETE /api/admin/admin-users/:id` - Delete an admin user
- `GET /api/admin/roles` - List all roles
- `GET /api/admin/capabilities` - List all capabilities

## Authentication

All API requests include the JWT access token from the auth store:
```typescript
const token = localStorage.getItem('rest-mail-admin-auth')
const authData = JSON.parse(token)
headers: {
  'Authorization': `Bearer ${authData.state.accessToken}`
}
```

## Future Enhancements

- Bulk user operations
- User activity logs
- Advanced filtering and sorting
- Export user list to CSV
- Password strength indicator
- Two-factor authentication setup
