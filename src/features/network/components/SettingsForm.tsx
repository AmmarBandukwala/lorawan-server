import type { FormEvent } from 'react'
import {
  Check,
} from 'lucide-react'
import type { FacilitySettings } from '../types'

/**
 * The facility profile form.
 *
 * `regions` is the band list the console was given, and `regionsAreFallback`
 * says whether the network server could not be reached to supply it. The band is
 * a regulatory setting, so the form says where its options came from rather than
 * presenting a guess with the same confidence as an answer.
 */
export function SettingsForm({
  draft, setDraft, onSubmit, saving, error, message, disabled, regions, regionsAreFallback,
}: {
  draft: FacilitySettings
  setDraft: (settings: FacilitySettings) => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
  saving: boolean
  error: string
  message: string
  disabled: boolean
  regions: readonly string[]
  regionsAreFallback?: boolean
}) {
  // Every field edits one key of the same draft object, so each handler copies
  // the draft and replaces the one key it owns.
  return (
    <section className="panel settings-view">
      <div className="panel-heading">
        <div><h2>Facility profile</h2><p>These values are stored in the local database.</p></div>
      </div>

      <form className="settings-form" onSubmit={onSubmit}>
        <label>Facility name
          <input required maxLength={120} value={draft.facilityName}
            onChange={(event) => setDraft({ ...draft, facilityName: event.target.value })} />
        </label>

        <label>LoRaWAN region
          <select required value={draft.region}
            onChange={(event) => setDraft({ ...draft, region: event.target.value })}>
            <option value="">Select region</option>
            {regions.map((region) => <option key={region} value={region}>{region}</option>)}
          </select>
          {regionsAreFallback && (
            <small className="field-hint">The network server could not be reached, so this list is the console’s own. The band the network enforces comes from its NS_REGION setting.</small>
          )}
        </label>

        <label>Operator display name
          <input required maxLength={120} value={draft.operatorName}
            onChange={(event) => setDraft({ ...draft, operatorName: event.target.value })} />
        </label>

        <label>Operator role
          <input required maxLength={80} value={draft.operatorRole}
            onChange={(event) => setDraft({ ...draft, operatorRole: event.target.value })} />
        </label>

        {error && <p className="form-error" role="alert">{error}</p>}
        {message && <p className="form-success" role="status">{message}</p>}

        <div className="modal-actions">
          <button className="button button-primary" type="submit" disabled={disabled || saving}>
            <Check size={15} />{saving ? 'Saving…' : 'Save settings'}
          </button>
        </div>
      </form>
    </section>
  )
}
