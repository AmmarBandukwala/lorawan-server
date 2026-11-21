import { useCallback, useEffect, useState } from 'react'
import { Check, Plus, ShieldOff, Trash2, X } from 'lucide-react'
import { attributeMap, attributeRows, deleteDevice, describeDeviceEdit, diffDeviceEdit, editDevice, fetchDevice, locationFields, locationProblem } from './control/device'
import { fetchModels } from './control/models'
import type { AttributeRow, DeviceEdit, DeviceModel, ProvisionedDevice } from './control/types'
import type { Device } from './types'

/**
 * The settings an operator can change on a device that is already provisioned.
 *
 * This lives apart from the session panel because it answers a different
 * question: the session panel is what the device is doing right now, this is what
 * the network is allowed to do with it. The two also fail differently — a device
 * that has never joined still has settings to edit.
 *
 * The form loads the stored values before it lets you change them. That costs a
 * request and is worth it: a form that assumed defaults would write those
 * assumptions over settings nobody looked at, which is how a device quietly loses
 * an ADR restriction someone configured months ago.
 *
 * Only fields that differ from what was loaded are sent. The network server
 * treats an omitted field as "leave alone", so posting the whole form every time
 * would make an unrendered checkbox an instruction.
 */
export function DeviceSettings({ device, onChanged, onDeleted }: {
  device: Device
  onChanged: () => void
  /** Called after the device is revoked, so the console can close and refresh. */
  onDeleted: () => void
}) {
  const [stored, setStored] = useState<ProvisionedDevice | null>(null)
  const [loadError, setLoadError] = useState('')
  const [name, setName] = useState('')
  const [group, setGroup] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [disableAdr, setDisableAdr] = useState(false)
  const [supportsClassC, setSupportsClassC] = useState(false)
  const [maxDataRate, setMaxDataRate] = useState('-1')
  const [attributes, setAttributes] = useState<AttributeRow[]>([])
  const [latitude, setLatitude] = useState('')
  const [longitude, setLongitude] = useState('')
  const [altitude, setAltitude] = useState('')
  const [modelId, setModelId] = useState('')
  // The picker's options. Fetched separately from the device because the list is
  // the same for every device, and a failure here must not stop the settings form:
  // a device whose model list could not be read still has a name and an ADR
  // setting to edit.
  const [models, setModels] = useState<DeviceModel[]>([])
  const [modelsUnavailable, setModelsUnavailable] = useState(false)

  const [saving, setSaving] = useState(false)
  const [feedback, setFeedback] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)
  const [confirmRevoke, setConfirmRevoke] = useState(false)
  const [revoking, setRevoking] = useState(false)

  const load = useCallback(async () => {
    try {
      const found = await fetchDevice(device.eui)
      setStored(found)
      setName(found.name ?? '')
      setGroup(found.group ?? '')
      setEnabled(found.enabled)
      setDisableAdr(found.disable_adr)
      setSupportsClassC(found.supports_class_c)
      setMaxDataRate(String(found.max_data_rate))
      setAttributes(attributeRows(found.attributes))
      const position = locationFields(found.location)
      setLatitude(position.latitude)
      setLongitude(position.longitude)
      setAltitude(position.altitude)
      setModelId(found.model_id ?? '')
      setLoadError('')
    } catch (error) {
      setStored(null)
      setLoadError(error instanceof Error ? error.message : 'Settings unavailable')
    }
  }, [device.eui])

  useEffect(() => {
    // Deferred by a task rather than called inline, matching how the rest of the
    // console starts its first read.
    const first = setTimeout(() => void load(), 0)
    return () => clearTimeout(first)
  }, [load])

  useEffect(() => {
    let active = true
    const timer = setTimeout(async () => {
      try {
        const list = await fetchModels()
        if (!active) return
        setModels(list)
        setModelsUnavailable(false)
      } catch {
        // An empty picker is survivable; the stored model_id still displays as
        // its own option, so the association is never silently dropped.
        if (!active) return
        setModels([])
        setModelsUnavailable(true)
      }
    }, 0)
    return () => {
      active = false
      clearTimeout(timer)
    }
  }, [])

  // The diff, not the form. A field that still matches what was loaded is left
  // out of the request entirely; see diffDeviceEdit for why that is the safety
  // property rather than an optimisation.
  const edit: DeviceEdit = stored ? diffDeviceEdit(stored, {
    name, group, enabled, disableAdr, supportsClassC, maxDataRate,
    attributes, latitude, longitude, altitude, modelId,
  }) : {}
  const changed = Object.keys(edit).length > 0
  const nameProblem = name.trim().length === 0 ? 'A device needs a name to be findable in the list.' : ''
  // Both are computed from the current rows rather than stored in state, so the
  // message disappears the moment the row is fixed instead of lingering until the
  // next save attempt.
  const attributeProblem = attributeMap(attributes).problem ?? ''
  const positionProblem = locationProblem({
    name, group, enabled, disableAdr, supportsClassC, maxDataRate,
    attributes, latitude, longitude, altitude, modelId,
  })

  async function save() {
    if (!changed || nameProblem || attributeProblem || positionProblem) return
    setSaving(true)
    setFeedback(null)
    try {
      const updated = await editDevice(device.eui, edit)
      setStored(updated)
      setName(updated.name ?? '')
      setGroup(updated.group ?? '')
      setEnabled(updated.enabled)
      setDisableAdr(updated.disable_adr)
      setSupportsClassC(updated.supports_class_c)
      setMaxDataRate(String(updated.max_data_rate))
      setAttributes(attributeRows(updated.attributes))
      setModelId(updated.model_id ?? '')
      const position = locationFields(updated.location)
      setLatitude(position.latitude)
      setLongitude(position.longitude)
      setAltitude(position.altitude)
      setFeedback({ kind: 'ok', text: describeDeviceEdit(edit) })
      onChanged()
    } catch (error) {
      setFeedback({ kind: 'error', text: error instanceof Error ? error.message : 'Could not save.' })
    } finally {
      setSaving(false)
    }
  }

  async function revoke() {
    setRevoking(true)
    setFeedback(null)
    try {
      await deleteDevice(device.eui)
      onDeleted()
    } catch (error) {
      setFeedback({ kind: 'error', text: error instanceof Error ? error.message : 'Could not revoke.' })
      setConfirmRevoke(false)
    } finally {
      setRevoking(false)
    }
  }

  if (!stored) {
    return (
      <section className="panel console-settings">
        <div className="panel-heading"><div><h2>Settings</h2></div></div>
        <p className="console-empty-note">
          {loadError || 'Reading the device settings…'}
        </p>
      </section>
    )
  }


  return (
    <section className="panel console-settings">
      <div className="panel-heading">
        <div>
          <h2>Settings</h2>
          <p>{stored.activation === 'otaa' ? 'OTAA' : `ABP · ${stored.dev_addr}`} · keys are never shown</p>
        </div>
      </div>

      <div className="console-settings-grid">
        <label>
          Name
          <input value={name} onChange={(event) => setName(event.target.value)} maxLength={120} />
        </label>
        <label>
          Group
          <input value={group} onChange={(event) => setGroup(event.target.value)} maxLength={60}
            placeholder="Unassigned" />
        </label>
        <label>
          Highest data rate
          <select value={maxDataRate} onChange={(event) => setMaxDataRate(event.target.value)}>
            <option value="-1">Band default (no device limit)</option>
            {[0, 1, 2, 3, 4, 5, 6, 7].map((dr) => <option key={dr} value={dr}>DR{dr}</option>)}
          </select>
        </label>
        <label>
          Device model
          <select value={modelId} onChange={(event) => setModelId(event.target.value)}>
            <option value="">No model — payloads stay as hex</option>
            {/* A stored association is always renderable, even when the list
                could not be read: an option that vanished would silently offer
                to clear a model the device actually has. */}
            {modelId !== '' && !models.some((model) => model.model_id === modelId) && (
              <option value={modelId}>{modelId} (not in the list)</option>
            )}
            {models.map((model) => (
              <option key={model.model_id} value={model.model_id}>
                {model.name} · {model.model_id}{model.codec_present ? '' : ' — no formatter'}
              </option>
            ))}
          </select>
          <small className="field-hint">
            {modelsUnavailable
              ? 'The model list could not be read, so the stored association is shown as saved.'
              : models.length === 0
                ? 'No models exist yet. Add one under Models to turn this device’s bytes into readings.'
                : 'The model decides how this device’s uplinks are decoded. Changing it affects new uplinks only — stored readings keep what they were decoded with.'}
          </small>
        </label>
      </div>
      {nameProblem && <p className="form-error">{nameProblem}</p>}

      <div className="console-toggles">
        <label className="console-toggle">
          <input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />
          <b>Allowed on the air</b>
          <span>Unchecking keeps the device provisioned but refuses its frames — the difference between pausing a device and removing it.</span>
        </label>
        <label className="console-toggle">
          <input type="checkbox" checked={disableAdr} onChange={(event) => setDisableAdr(event.target.checked)} />
          <b>Hold its own data rate</b>
          <span>When set, the network stops telling this device to change its spreading factor or power.</span>
        </label>
        <label className="console-toggle">
          <input type="checkbox" checked={supportsClassC} onChange={(event) => setSupportsClassC(event.target.checked)} />
          <b>Class C capable</b>
          <span>
            Recorded on the device, but the network does not act on it yet: class-C
            continuous listening is not implemented, so checking this does not make a
            device reachable between its own uplinks.
          </span>
        </label>
      </div>

      {/* Attributes and position: what the device is for and where it sits.
          Neither changes how the network behaves — the server stores them and
          never reads them — so they are kept below the radio settings rather
          than mixed in with the ones that do. */}
      <div className="console-attributes">
        <div className="console-subheading">
          <b>Context</b>
          <span>Free-form tags and an installed position. Recorded for the people reading the dashboard later; the network does not act on either.</span>
        </div>

        <div className="attribute-rows">
          {attributes.map((row, index) => (
            <div className="attribute-row" key={index}>
              <input
                value={row.key}
                placeholder="key, e.g. line"
                aria-label={`Attribute ${index + 1} key`}
                maxLength={40}
                onChange={(event) => setAttributes(attributes.map((r, i) => (i === index ? { ...r, key: event.target.value } : r)))}
              />
              <input
                value={row.value}
                placeholder="value, e.g. cold store 3"
                aria-label={`Attribute ${index + 1} value`}
                maxLength={200}
                onChange={(event) => setAttributes(attributes.map((r, i) => (i === index ? { ...r, value: event.target.value } : r)))}
              />
              <button
                className="text-action"
                aria-label={`Remove attribute ${row.key || index + 1}`}
                onClick={() => setAttributes(attributes.filter((_, i) => i !== index))}
              >
                <X size={13} />
              </button>
            </div>
          ))}
        </div>

        <button
          className="text-action"
          onClick={() => setAttributes([...attributes, { key: '', value: '' }])}
          disabled={attributes.length >= 32}
        >
          <Plus size={13} /> Add attribute
        </button>
        {attributeProblem && <p className="form-error">{attributeProblem}</p>}

        <div className="console-settings-grid console-location">
          <label>
            Latitude
            <input value={latitude} onChange={(event) => setLatitude(event.target.value)}
              placeholder="51.5074" inputMode="decimal" />
          </label>
          <label>
            Longitude
            <input value={longitude} onChange={(event) => setLongitude(event.target.value)}
              placeholder="-0.1278" inputMode="decimal" />
          </label>
          <label>
            Altitude (m)
            <input value={altitude} onChange={(event) => setAltitude(event.target.value)}
              placeholder="optional" inputMode="numeric" />
          </label>
        </div>
        {positionProblem && <p className="form-error">{positionProblem}</p>}
        {!positionProblem && stored.location && latitude === '' && longitude === '' && altitude === '' && (
          <p className="console-empty-note">Saving with all three cleared removes the recorded position.</p>
        )}
      </div>

      <div className="console-settings-actions">
        <button
          className="button button-primary"
          onClick={() => void save()}
          disabled={saving || !changed || Boolean(nameProblem) || Boolean(attributeProblem) || Boolean(positionProblem)}
        >
          <Check size={14} /> {saving ? 'Saving…' : changed ? `Save ${Object.keys(edit).length} change(s)` : 'No changes'}
        </button>
      </div>

      {feedback && (
        <p
          className={feedback.kind === 'ok' ? 'form-success' : 'form-error'}
          role={feedback.kind === 'ok' ? 'status' : 'alert'}
        >
          {feedback.kind === 'ok' && <Check size={13} />} {feedback.text}
        </p>
      )}

      <div className="console-revoke">
        {!confirmRevoke ? (
          <button className="text-action danger" onClick={() => setConfirmRevoke(true)}>
            <ShieldOff size={13} /> Revoke this device
          </button>
        ) : (
          <div className="console-revoke-confirm">
            <b>Revoke {device.name}?</b>
            <span>
              This deletes the device and its session. It will not be able to join or be
              reached again until it is provisioned from scratch with its keys.
            </span>
            <div>
              <button className="button button-danger" onClick={() => void revoke()} disabled={revoking}>
                <Trash2 size={14} /> {revoking ? 'Revoking…' : 'Yes, revoke it'}
              </button>
              <button className="text-action" onClick={() => setConfirmRevoke(false)}>Keep it</button>
            </div>
          </div>
        )}
      </div>
    </section>
  )
}
