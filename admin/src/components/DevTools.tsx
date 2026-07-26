import { TanStackDevtools } from '@tanstack/react-devtools'
import { TanStackRouterDevtoolsPanel } from '@tanstack/react-router-devtools'

/**
 * Development-only devtools panel.
 *
 * Kept in its own module so it can be pulled in through a dev-gated dynamic
 * import (see __root.tsx). In a production build `import.meta.env.DEV` folds to
 * `false`, the dynamic `import()` is dead-code-eliminated, and neither this
 * component nor the TanStack devtools packages are emitted into the bundle.
 */
export function DevTools() {
  return (
    <TanStackDevtools
      config={{
        position: 'bottom-right',
      }}
      plugins={[
        {
          name: 'TanStack Router',
          render: <TanStackRouterDevtoolsPanel />,
        },
      ]}
    />
  )
}
