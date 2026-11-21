import { useState } from 'react'
import { Plus, ShieldCheck, X } from 'lucide-react'
import { registerDevice } from '../snapshot'
import { APP_KEY_HEX_DIGITS, EUI_HEX_DIGITS } from '../constants'
import { deviceFormError, deviceFormRequest, type DeviceFormValues } from '../validation'
import type { Device } from '../types'
import { failureMessage } from '../control/http'

const emptyForm: DeviceFormValues = { name: '', devEui: '', joinEui: '', appKey: '', group: '' }

/**
 * The register-a-device dialog.
 *
 * It owns its own draft, error and in-flight state. Those were held in App
 * alongside everything else, which meant the component that renders a form and
 * the component that renders the whole console shared fifteen pieces of state —
 * and reading App meant holding all of them to understand one text input.
 *
 * What stays outside is deliberate: `unreachable` belongs to the connection, and
 * `onRegistered` has to reach the snapshot that App owns.
 */
export function RegisterDeviceDialog({
  onClose,
  onRegistered,
  unreachable,
}: {
  onClose: () => void
  onRegistered: (device: Device) => void
  unreachable: boolean
}) {
  const [form, setForm] = useState<DeviceFormValues>(emptyForm)
  const [registering, setRegistering] = useState(false)
  const [error, setError] = useState('')

  const field = (key: keyof DeviceFormValues) => (event: { target: { value: string } }) =>
    setForm({ ...form, [key]: event.target.value })

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (unreachable) {
      setError('Connect to the database API before registering a device.')
      return
    }
    const problem = deviceFormError(form)
    if (problem) {
      setError(problem)
      return
    }

    setRegistering(true)
    setError('')
    try {
      // A degraded API still accepts writes: one unreadable panel is not a
      // reason to refuse an action whose own endpoint answered fine.
      onRegistered(await registerDevice(deviceFormRequest(form)))
    } catch (cause) {
      setError(failureMessage(cause, 'Device could not be provisioned.'))
      setRegistering(false)
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <section className="modal" role="dialog" aria-modal="true" aria-labelledby="modal-title" onClick={(event) => event.stopPropagation()}>
        <div className="modal-heading">
          <div>
            <span className="modal-icon"><Plus size={18} /></span>
            <div>
              <h2 id="modal-title">Register a device</h2>
              <p>Provision an OTAA end device on this network</p>
            </div>
          </div>
          <button className="icon-button" aria-label="Close dialog" onClick={onClose}><X size={18} /></button>
        </div>
        <form onSubmit={submit}>
          <label>Device name
            <input autoFocus required value={form.name} onChange={field('name')} placeholder="Enter device name" />
          </label>
          <label>DevEUI <span>{EUI_HEX_DIGITS} hexadecimal characters</span>
            <input required value={form.devEui} onChange={field('devEui')} placeholder="0011223344556677" spellCheck={false} />
          </label>
          <label>AppKey <span>{APP_KEY_HEX_DIGITS} hexadecimal characters, printed on the device label</span>
            <input required value={form.appKey} onChange={field('appKey')} placeholder="2b7e151628aed2a6abf7158809cf4f3c" spellCheck={false} />
          </label>
          <label>JoinEUI <span>optional — leave empty to accept any JoinEUI this device presents</span>
            <input value={form.joinEui} onChange={field('joinEui')} placeholder="0102030405060708" spellCheck={false} />
          </label>
          <label>Group <span>optional label for the device list</span>
            <input value={form.group} onChange={field('group')} placeholder="Unassigned" />
          </label>
          {error && <p className="form-error" role="alert">{error}</p>}
          <div className="form-note">
            <ShieldCheck size={15} />
            {/* The honest version of the old note, which said this only added an
                inventory record. The key is what makes the device able to join,
                and the operator needs to know where it goes. */}
            The AppKey is sent once to the network server and is never shown again. Keep a copy on the device label.
          </div>
          <div className="modal-actions">
            <button type="button" className="button button-secondary" onClick={onClose}>Cancel</button>
            <button type="submit" className="button button-primary" disabled={registering}>
              <Plus size={15} />{registering ? 'Provisioning…' : 'Register device'}
            </button>
          </div>
        </form>
      </section>
    </div>
  )
}
