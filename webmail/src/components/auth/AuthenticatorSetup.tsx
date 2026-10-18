import { useState } from 'react'
import { QRCodeSVG } from 'qrcode.react'

interface Props {
  secret: string
  otpauthUrl: string
}

export function AuthenticatorSetup({ secret, otpauthUrl }: Props) {
  const [copyStatus, setCopyStatus] = useState('')

  return (
    <div className="space-y-3">
      <p className="text-sm">Scan this QR code with your authenticator app.</p>
      {/* Render locally: the provisioning URI must never go to a QR service. */}
      {otpauthUrl && <QRCodeSVG value={otpauthUrl} size={256} level="M" marginSize={4}
        bgColor="#FFFFFF" fgColor="#000000" role="img" aria-label="Authenticator setup QR code"
        className="block mx-auto max-w-full h-auto" />}
      <details open={!otpauthUrl} className="text-sm">
        <summary className="cursor-pointer underline">Can’t scan? Enter a setup key</summary>
        <div className="mt-3 space-y-2">
          <p>Add a time-based account in your authenticator app using this setup key:</p>
          <code className="block break-all border p-3 select-all">{secret}</code>
          <button type="button" className="underline" onClick={async () => {
            try {
              await navigator.clipboard.writeText(secret)
              setCopyStatus('Setup key copied.')
            } catch {
              setCopyStatus('Copy unavailable. Select the setup key to copy it manually.')
            }
          }}>Copy setup key</button>
          {copyStatus && <p role="status">{copyStatus}</p>}
          <p>Type: time-based, 6 digits, 30 seconds.</p>
        </div>
      </details>
    </div>
  )
}
