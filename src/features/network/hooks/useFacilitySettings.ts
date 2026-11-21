/**
 * Saving the facility profile.
 *
 * Separate from useNetworkSnapshot because the two have opposite failure rules:
 * a failed read keeps the last good values and says nothing, while a failed save
 * must be loud — the operator pressed "save" and needs to know the profile did
 * not change.
 */
import { useCallback, useState } from 'react'

import { saveFacilitySettings } from '../snapshot'
import type { NetworkSnapshot } from '../snapshot'
import type { ConnectionState, FacilitySettings } from '../types'

export type SettingsSave = {
  /** Wire this to the form's onSubmit. */
  save: (event: React.FormEvent<HTMLFormElement>) => Promise<void>
  saving: boolean
  error: string
  message: string
}

export function useFacilitySettings(input: {
  draft: FacilitySettings
  connection: ConnectionState
  setDraft: (settings: FacilitySettings) => void
  patch: (patch: Partial<NetworkSnapshot>) => void
}): SettingsSave {
  const { draft, connection, setDraft, patch } = input
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')

  const save = useCallback(async (event: React.FormEvent<HTMLFormElement>) => {
    // The form is a real form so it submits on Enter; the page behind it is a
    // dashboard with no document to navigate to.
    event.preventDefault()
    setError('')
    setMessage('')
    if (connection === 'offline' || connection === 'loading') {
      setError('Connect to the database API before saving facility settings.')
      return
    }
    setSaving(true)
    try {
      const saved = await saveFacilitySettings(draft)
      // Both places: the snapshot so the sidebar reflects it on the next paint,
      // and the draft so the form does not keep showing what was just rejected
      // or accepted.
      patch({ settings: saved })
      setDraft(saved)
      setMessage('Facility settings saved.')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Settings could not be saved.')
    } finally {
      setSaving(false)
    }
  }, [draft, connection, setDraft, patch])

  return { save, saving, error, message }
}
