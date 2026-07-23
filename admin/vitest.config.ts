import { defineConfig } from 'vitest/config'
import viteReact from '@vitejs/plugin-react'
import tsconfigPaths from 'vite-tsconfig-paths'

// Standalone vitest config: deliberately does NOT load the TanStack Router /
// devtools plugins from vite.config.ts — the unit suite exercises pure logic
// and prop/store-driven components, not the generated route tree, so we keep
// the test transform minimal (React JSX + tsconfig path aliases).
export default defineConfig({
  plugins: [tsconfigPaths({ projects: ['./tsconfig.json'] }), viteReact()],
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,tsx}'],
    // api.ts reads import.meta.env.VITE_API_URL at module load; pin it so the
    // client's URL-building is deterministic under test.
    env: {
      VITE_API_URL: 'https://api.test',
    },
  },
})
