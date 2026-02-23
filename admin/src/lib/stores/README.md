# Zustand Store Architecture

## Single Source of Truth

**All application state MUST be managed through Zustand stores. No exceptions.**

- ❌ **NO React Context** (except for framework-level needs)
- ❌ **NO useState** for shared data (only for local component UI state)
- ❌ **NO TanStack Query** for state (only for server sync if needed)
- ✅ **YES Zustand** for ALL shared application state

## Data Flow Architecture

```
┌─────────────┐
│   Backend   │ (REST API)
│   API       │
└──────┬──────┘
       │
       │ fetch/POST/PUT/DELETE
       ▼
┌──────────────────┐
│  Zustand Store   │ ◄── Single Source of Truth
│  (State + Logic) │
└────────┬─────────┘
         │
         │ useStore hooks
         ▼
┌──────────────────┐
│  React Components│
│  (View Layer)    │
└──────────────────┘
```

## Standard Store Pattern

Every store MUST follow this exact pattern:

```typescript
import { create } from 'zustand'
import { persist } from 'zustand/middleware' // Optional, for persistence

// 1. Define TypeScript interfaces
interface EntityData {
  id: string
  name: string
  // ... other fields
}

interface EntityStore {
  // State
  entities: EntityData[]
  isLoading: boolean
  error: string | null

  // Actions - CRUD operations
  fetchEntities: () => Promise<void>
  createEntity: (data: CreateEntityRequest) => Promise<void>
  updateEntity: (id: string, data: Partial<EntityData>) => Promise<void>
  deleteEntity: (id: string) => Promise<void>

  // Utility actions
  clearError: () => void
  reset: () => void
}

// 2. Create the store
export const useEntityStore = create<EntityStore>()(
  persist(  // Optional: only if data should persist across sessions
    (set, get) => ({
      // Initial state
      entities: [],
      isLoading: false,
      error: null,

      // Fetch all entities
      fetchEntities: async () => {
        set({ isLoading: true, error: null })

        try {
          const { accessToken } = useAuthStore.getState()
          const response = await fetch('/api/admin/entities', {
            headers: {
              'Authorization': `Bearer ${accessToken}`,
            },
          })

          if (!response.ok) {
            throw new Error('Failed to fetch entities')
          }

          const data = await response.json()
          set({ entities: data, isLoading: false })
        } catch (error) {
          set({
            error: error instanceof Error ? error.message : 'Unknown error',
            isLoading: false,
          })
        }
      },

      // Create entity
      createEntity: async (data) => {
        set({ isLoading: true, error: null })

        try {
          const { accessToken } = useAuthStore.getState()
          const response = await fetch('/api/admin/entities', {
            method: 'POST',
            headers: {
              'Authorization': `Bearer ${accessToken}`,
              'Content-Type': 'application/json',
            },
            body: JSON.stringify(data),
          })

          if (!response.ok) {
            throw new Error('Failed to create entity')
          }

          const newEntity = await response.json()
          set(state => ({
            entities: [...state.entities, newEntity],
            isLoading: false,
          }))
        } catch (error) {
          set({
            error: error instanceof Error ? error.message : 'Unknown error',
            isLoading: false,
          })
          throw error
        }
      },

      // Update entity
      updateEntity: async (id, updates) => {
        set({ isLoading: true, error: null })

        try {
          const { accessToken } = useAuthStore.getState()
          const response = await fetch(`/api/admin/entities/${id}`, {
            method: 'PUT',
            headers: {
              'Authorization': `Bearer ${accessToken}`,
              'Content-Type': 'application/json',
            },
            body: JSON.stringify(updates),
          })

          if (!response.ok) {
            throw new Error('Failed to update entity')
          }

          const updatedEntity = await response.json()
          set(state => ({
            entities: state.entities.map(e =>
              e.id === id ? updatedEntity : e
            ),
            isLoading: false,
          }))
        } catch (error) {
          set({
            error: error instanceof Error ? error.message : 'Unknown error',
            isLoading: false,
          })
          throw error
        }
      },

      // Delete entity
      deleteEntity: async (id) => {
        set({ isLoading: true, error: null })

        try {
          const { accessToken } = useAuthStore.getState()
          const response = await fetch(`/api/admin/entities/${id}`, {
            method: 'DELETE',
            headers: {
              'Authorization': `Bearer ${accessToken}`,
            },
          })

          if (!response.ok) {
            throw new Error('Failed to delete entity')
          }

          set(state => ({
            entities: state.entities.filter(e => e.id !== id),
            isLoading: false,
          }))
        } catch (error) {
          set({
            error: error instanceof Error ? error.message : 'Unknown error',
            isLoading: false,
          })
          throw error
        }
      },

      // Utility actions
      clearError: () => set({ error: null }),

      reset: () => set({
        entities: [],
        isLoading: false,
        error: null,
      }),
    }),
    {
      name: 'entity-store',  // localStorage key
      partialize: (state) => ({
        entities: state.entities,  // Only persist entities, not loading/error
      }),
    }
  )
)
```

## Component Usage Pattern

```typescript
import { useEntityStore } from '../lib/stores/entityStore'

function EntityList() {
  // 1. Subscribe to store
  const { entities, isLoading, error, fetchEntities, deleteEntity } = useEntityStore()

  // 2. Fetch on mount
  useEffect(() => {
    fetchEntities()
  }, [fetchEntities])

  // 3. Render based on store state
  if (isLoading) return <div>Loading...</div>
  if (error) return <div>Error: {error}</div>

  return (
    <div>
      {entities.map(entity => (
        <div key={entity.id}>
          {entity.name}
          <button onClick={() => deleteEntity(entity.id)}>Delete</button>
        </div>
      ))}
    </div>
  )
}
```

## Store Communication

Stores can access other stores using `.getState()`:

```typescript
// In domainStore.ts
import { useAuthStore } from './authStore'

// Inside an action
const { accessToken } = useAuthStore.getState()
```

## Rules

1. **All API calls happen in stores**, never in components
2. **All data mutations happen in stores**, never in components
3. **Components only read state and call actions**
4. **Loading and error states are always managed in stores**
5. **JWT token is always accessed from authStore**
6. **No prop drilling** - use stores directly in any component
7. **No Redux, MobX, Recoil, or other state libraries**
8. **Persist only necessary data** (not loading/error states)

## Existing Stores

- `authStore.ts` - Authentication state (user, token, login/logout)
- `dashboardStore.ts` - Dashboard metrics and stats
- `domainStore.ts` - Domain management (WIP)
- `mailboxStore.ts` - Mailbox management (WIP)
- `queueStore.ts` - Queue management (WIP)
- `adminUserStore.ts` - Admin user management (WIP)
- `uiStore.ts` - UI state (sidebar, modals, notifications) (WIP)

## Benefits

- ✅ **Predictable data flow** - Always store → component
- ✅ **No state conflicts** - Single source of truth
- ✅ **Easy debugging** - All state changes in one place
- ✅ **Type safety** - Full TypeScript support
- ✅ **Persistence** - Optional localStorage sync
- ✅ **Performance** - Only re-renders when subscribed data changes
- ✅ **Testability** - Stores are pure functions
