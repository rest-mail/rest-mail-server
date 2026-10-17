import ReactDOM from 'react-dom/client'
import { RouterProvider, createRouter } from '@tanstack/react-router'
// Self-hosted fonts (latin subset) — replaces the previous Google Fonts CDN
// <link>s in index.html. Bundled by Vite and served same-origin, so a strict
// `font-src 'self'` CSP is satisfied with no third-party dependency.
import '@fontsource/inter/latin-400.css'
import '@fontsource/inter/latin-500.css'
import '@fontsource/inter/latin-600.css'
import '@fontsource/inter/latin-700.css'
import '@fontsource/space-grotesk/latin-400.css'
import '@fontsource/space-grotesk/latin-500.css'
import '@fontsource/space-grotesk/latin-600.css'
import '@fontsource/space-grotesk/latin-700.css'
import { routeTree } from './routeTree.gen'

const router = createRouter({
  routeTree,
  // Where the app is served, which only the deployment knows: at / on
  // admin.<domain>, or under /admin behind a shared proxy. The image's nginx
  // names it in the page's <base> (NGINX_BASE_PATH); the Vite dev server, which
  // adds none, serves at its own base.
  basepath: document.querySelector('base')
    ? new URL(document.baseURI).pathname
    : import.meta.env.BASE_URL,
  defaultPreload: 'intent',
  scrollRestoration: true,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

const rootElement = document.getElementById('app')!

if (!rootElement.innerHTML) {
  const root = ReactDOM.createRoot(rootElement)
  root.render(<RouterProvider router={router} />)
}
