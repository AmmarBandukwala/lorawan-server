import { useCallback, useEffect, useState } from 'react'
import { Check, ShieldOff, Trash2 } from 'lucide-react'
import {
  deleteDevice,
  describeDeviceEdit,
  diffDeviceEdit,
  editDevice,
  fetchDevice,
  type DeviceEdit,
  type ProvisionedDevice,
} from './control'
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

  // The diff, not the form. A field that still matches what was loaded is left
  // out of the request entirely; see diffDeviceEdit for why that is the safety
  // property rather than an optimisation.
  const edit: DeviceEdit = stored ? diffDeviceEdit(stored, {
    name, group, enabled, disableAdr, supportsClassC, maxDataRate,
  }) : {}
  const changed = Object.keys(edit).length > 0
  const nameProblem = name.trim().length === 0 ? 'A device needs a name to be findable in the list.' : ''

  async function save() {
    if (!changed || nameProblem) return
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

      <div className="console-settings-actions">
        <button
          className="button button-primary"
          onClick={() => void save()}
          disabled={saving || !changed || Boolean(nameProblem)}
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
