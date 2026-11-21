import { useEffect, useState } from 'react'
import { AppInfo } from "../bindings/github.com/rest-mail/rest-mail-server/desktop";

type Release = { version: string; commit: string; builtAt: string }

// The first shell: it proves the app launches and knows which build it is.
// The mail UI from webmail replaces this view as #2211's criteria land.
function App() {
  const [release, setRelease] = useState<Release | null>(null)
  useEffect(() => {
    AppInfo.Release().then((r) => setRelease(r as Release)).catch(() => setRelease(null))
  }, [])
  return (
    <div className="container" style={{ padding: 32 }}>
      <h1>REST Mail</h1>
      <p>Desktop client</p>
      {release ? (
        <p data-testid="release">
          Version {release.version} · commit {release.commit} · built {release.builtAt}
        </p>
      ) : (
        <p>Reading build information…</p>
      )}
    </div>
  )
}

export default App
