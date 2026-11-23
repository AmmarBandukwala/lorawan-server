import { useState } from 'react'
import { Check, Copy, Info } from 'lucide-react'

/**
 * A value on screen, with a way to put it on the clipboard.
 *
 * Extracted from the gateways panel, which had this logic inline, because a second
 * copy of the clipboard handling is how two places end up behaving differently —
 * one reporting success where the other admits the browser refused. The failure mode
 * that matters is the same in both places and is the worst one available: claiming a
 * copy worked when it did not. An operator pastes nothing into a config, sees no
 * error, and goes looking for a problem somewhere else entirely.
 *
 * Browsers only expose the clipboard in a secure context, so a console reached over
 * a plain-HTTP LAN address genuinely cannot use it. That is why the fallback is a
 * visible instruction to select the text, not a silent no-op.
 */
export function CopyValue({ value, label, note, multiline = false }: {
  /** The exact text that will be copied. Displayed verbatim beside the button. */
  value: string
  /** What the button's accessible name calls this, e.g. "the UDP endpoint". */
  label: string
  /** One line under the value, saying where it goes or what it is for. */
  note?: string
  /** True for a config block, which needs to keep its line breaks. */
  multiline?: boolean
}) {
  const [copied, setCopied] = useState<'idle' | 'done' | 'unavailable'>('idle')

  async function copy() {
    // Read defensively: navigator.clipboard is absent entirely outside a secure
    // context, and writeText rejects if the document has lost focus.
    if (!navigator.clipboard?.writeText) {
      setCopied('unavailable')
      return
    }
    try {
      await navigator.clipboard.writeText(value)
      setCopied('done')
    } catch {
      setCopied('unavailable')
    }
  }

  const button = (
    <button className="copy-endpoint" type="button" onClick={() => void copy()}
      aria-label={`Copy ${label}`}>
      {copied === 'done' ? <Check size={12} /> : <Copy size={12} />}
      {copied === 'done' ? 'Copied' : 'Copy'}
    </button>
  )

  return (
    <div className="connect-endpoint">
      {multiline ? (
        <>
          <div className="copy-value-head">
            <span className="copy-value-label">{label}</span>
            {button}
          </div>
          {/* pre, not code: a config block whose newlines collapsed would copy as
              one line and fail in the other machine with no clue why. */}
          <pre className="copy-value-block">{value}</pre>
        </>
      ) : (
        <div className="connect-endpoint-line">
          <code>{value}</code>
          {button}
        </div>
      )}
      {note && <span>{note}</span>}
      {copied === 'unavailable' && (
        <p className="connect-note">
          <Info size={13} /> This browser will not hand out the clipboard over an
          insecure origin. Select the text above and copy it manually.
        </p>
      )}
    </div>
  )
}
