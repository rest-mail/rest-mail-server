# Webmail Themes, Settings Page & Account Settings Design

**Date:** 2026-02-18
**Status:** Approved

## Problem

1. The user menu trigger (email address as ghost button) has no visual affordance — no avatar, no chevron — so it is not discoverable as a dropdown menu. The existing light/dark theme sub-menu exists in code but users cannot find it.
2. There are only two themes (light/dark). The user wants a richer set of named colour palettes.
3. There is no settings page for configuring webmail preferences.
4. There is no per-account settings shortcut in the sidebar — users have to navigate to each view individually.

---

## 1. User Menu Trigger

Replace the current `<Button variant="ghost">{user?.email}</Button>` with an avatar circle (user initials, e.g. "AL" for alice@...) plus a ChevronDown icon. Full email address moves inside the dropdown as a label. This makes the trigger obviously interactive.

---

## 2. Theme System

### Palettes (6 total)

| ID | Name | Tone | Primary accent |
|----|------|------|----------------|
| `dawn` | Dawn | Light | Blue |
| `linen` | Linen | Light-warm | Amber |
| `slate` | Slate | Mid | Violet |
| `dusk` | Dusk | Mid-warm | Rose |
| `midnight` | Midnight | Dark | Blue |
| `forest` | Forest | Dark-green | Sage |

`dawn` replaces the current `light` default. `midnight` replaces `dark`.

### Implementation

- `index.css`: add 5 new palette classes (`.linen`, `.slate`, `.dusk`, `.forest`; existing `:root` becomes `dawn`, existing `.dark` becomes `midnight`)
- `uiStore.ts`: `type Theme = 'dawn' | 'linen' | 'slate' | 'dusk' | 'midnight' | 'forest'`
- `applyTheme()`: sets `data-theme="<name>"` on `<html>` and applies the matching CSS class
- localStorage key: `restmail-theme` (existing, now stores palette name)

### Dropdown sub-menu

Replace the two-item list with a 3×2 grid of swatch chips: coloured circle + name + checkmark on active. Grouped by light / mid / dark with a sub-label.

---

## 3. Settings Page (from user dropdown)

### Routing

- New view ID: `'settings'` added to `View` type in `uiStore.ts`
- New menu item "Settings" in user dropdown (Settings icon, above Theme)
- New component: `webmail/src/components/settings/SettingsView.tsx`

### Tabs

**General**
- Reading pane: bottom (default) / right / off
- Message density: comfortable (default) / compact
- Auto-save drafts: toggle (default on)
- Keyboard shortcuts: static reference table (c = compose, r = reply, etc.)

**Accounts**
- Account selector (list of linked accounts)
- Sub-tabs per account: Details | Vacation | Quota | Danger Zone
- Danger Zone: "Remove this account from webmail" destructive button with confirmation dialog. Disabled (with explanation) for the primary account (the one the user authenticated as).

**Notifications**
- Desktop notification toggle
- New mail sound toggle

### Persistence

New `settingsStore.ts` (Zustand) persists to localStorage under `restmail-settings`. Reading pane position and density are consumed by `MailView` in `App.tsx`.

---

## 4. Sidebar Per-Account Gear Icon

- A `Settings` (gear) icon appears to the right of each account label in `Sidebar.tsx`
- Visible on hover of the account row (or always-visible if space allows)
- Clicking sets `view = 'accountSettings'` and `selectedAccountId` in `uiStore`
- New component: `webmail/src/components/account/AccountSettingsView.tsx`

### AccountSettingsView tabs

| Tab | Content |
|-----|---------|
| Details | Display name, email, quota bar, IMAP/SMTP server info |
| Vacation | Vacation responder toggle + message (same as existing VacationView) |
| Quota | Quota usage chart + breakdown by folder |
| Danger Zone | "Remove account from webmail" — disabled with note for primary account |

The primary account is identified by matching the account email to `authStore.user.email`.

---

## Files Changed

| File | Change |
|------|--------|
| `src/index.css` | Add 4 new palette classes |
| `src/stores/uiStore.ts` | Expand Theme type, add selectedAccountId, add 'settings'/'accountSettings' views |
| `src/stores/settingsStore.ts` | New: reading pane, density, notifications, auto-save |
| `src/components/layout/TopBar.tsx` | Avatar trigger, expanded theme picker, Settings menu item |
| `src/components/layout/Sidebar.tsx` | Gear icon per account |
| `src/components/settings/SettingsView.tsx` | New: tabbed settings page |
| `src/components/account/AccountSettingsView.tsx` | New: per-account settings panel |
| `src/App.tsx` | Wire 'settings' and 'accountSettings' views, pass density/reading-pane to MailView |
