import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { Battery, Check, RefreshCw, Send, Trash2, X, Zap } from 'lucide-react'
import { cancelDownlink, enqueueDownlink, fetchSession, requestDeviceStatus, validatePayload } from './control/session'
import { batteryPercent } from './control/status'
import { fetchUplinks, formatDecoded } from './control/traffic'
import type { DeviceSession, UplinkRow } from './control/types'
import type { Device } from './types'
import { DeviceSettings } from './DeviceSettings'
import { failureMessage, isServerUnavailable } from './control/http'

/**
 * One device's live session, plus the controls that make it do something.
 *
 * This panel is the reason the console stops being an inventory list. Everything
 * it shows comes from the network server's memory rather than from a persisted
 * row, so it answers "what is this device doing right now" — the counters, the
 * data rate ADR settled on, what is waiting for the next window — which is the
 * question an operator actually arrives with.
 *
 * The send control is deliberately worded as "queued" rather than "sent". A
 * class-A device listens for two seconds after it transmits and then goes quiet,
 * so nothing has happened on the air when the request returns, and a UI that said
 * otherwise would teach the operator a false model of the network.
 */
export function DeviceConsole({ device, onClose, onChanged, onDeleted }: {
  device: Device
  onClose: () => void
  /** Called after a command succeeds so the dashboard can refresh. */
  onChanged: () => void
  /** Called after the device is revoked, so the panel can close. */
  onDeleted: () => void
}) {
  const [session, setSession] = useState<DeviceSession | null>(null)
  const [loadError, setLoadError] = useState('')
  /**
   * Whether the failure means the network server could not be asked, as opposed
   * to having been asked and answered "no session".
   *
   * These two used to render the same panel, which told an operator with a dead
   * network server that their device had not joined — sending them to check the
   * device while the actual problem was the process behind this page.
   */
  const [serverUnreachable, setServerUnreachable] = useState(false)
  const [loading, setLoading] = useState(true)

  const [payload, setPayload] = useState('')
  const [fPort, setFPort] = useState('10')
  const [confirmed, setConfirmed] = useState(false)
  const [sending, setSending] = useState(false)
  const [feedback, setFeedback] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)

  const reload = useCallback(async () => {
    try {
      const next = await fetchSession(device.eui)
      setSession(next)
      setLoadError('')
      setServerUnreachable(false)
    } catch (error) {
      // A device that has never joined legitimately has no session, and that is
      // a state of the device rather than a fault. Anything from 502 up is a
      // fault of ours, and must not be described as one of theirs.
      setSession(null)
      setServerUnreachable(isServerUnavailable(error))
      setLoadError(failureMessage(error, 'Session unavailable'))
    } finally {
      setLoading(false)
    }
  }, [device.eui])

  useEffect(() => {
    // Deferred by a task rather than called inline, matching how App.tsx starts
    // its first refresh: the effect body must not set state synchronously.
    const first = setTimeout(() => void reload(), 0)
    return () => clearTimeout(first)
  }, [reload])

  const payloadProblem = validatePayload(payload)

  async function send() {
    if (payloadProblem) return
    setSending(true)
    setFeedback(null)
    try {
      const result = await enqueueDownlink(device.eui, {
        data: payload,
        fPort: Number(fPort) || undefined,
        confirmed,
      })
      setFeedback({
        kind: 'ok',
        text: `Queued on port ${result.f_port}. ${result.explanation}.`,
      })
      setPayload('')
      void reload()
      onChanged()
    } catch (error) {
      setFeedback({ kind: 'error', text: failureMessage(error, 'Could not queue the frame.') })
    } finally {
      setSending(false)
    }
  }

  async function cancel() {
    setFeedback(null)
    try {
      await cancelDownlink(device.eui)
      setFeedback({ kind: 'ok', text: 'The queued frame was dropped.' })
      void reload()
      onChanged()
    } catch (error) {
      setFeedback({ kind: 'error', text: failureMessage(error, 'Could not cancel.') })
    }
  }

  async function askStatus() {
    setFeedback(null)
    try {
      await requestDeviceStatus(device.eui)
      setFeedback({
        kind: 'ok',
        text: 'Asked for a battery report. It arrives with the device’s next uplink.',
      })
      onChanged()
    } catch (error) {
      setFeedback({ kind: 'error', text: failureMessage(error, 'Could not schedule the report.') })
    }
  }

  return (
    <aside className="panel device-console" aria-label={`Session for ${device.name}`}>
      <div className="panel-heading">
        <div>
          <h2>{device.name}</h2>
          <p>{device.eui}</p>
        </div>
        <button className="row-menu" aria-label="Close device session" onClick={onClose}><X size={15} /></button>
      </div>

      {loading && <div className="console-loading">Reading the session…</div>}
      {!loading && loadError && !session && (
        <div className="console-empty">
          <b>{serverUnreachable
            ? 'The network server could not be reached'
            : 'No session on this network yet'}</b>
          <span>{loadError}</span>
        </div>
      )}

      {session && (
        <>
          <div className="console-grid">
            <Field label="Device address" value={session.dev_addr} />
            <Field label="Region" value={session.region} />
            <Field label="Uplink counter" value={session.f_cnt_up.toLocaleString()} />
            <Field label="Downlink counter" value={session.f_cnt_down.toLocaleString()} />
            <Field label="Data rate" value={`DR${session.data_rate}`} />
            <Field label="TX power" value={`index ${session.tx_power_index}`} />
            <Field label="Retransmissions" value={`NbTrans ${session.nb_trans}`} />
            <Field label="ADR" value={session.adr ? 'Followed by device' : 'Device holds its rate'} />
            <Field label="RX1 delay" value={`${session.rx1_delay} s`} />
            <Field label="RX2" value={`${(session.rx2_frequency_hz / 1e6).toFixed(6)} MHz · DR${session.rx2_data_rate}`} />
            <Field label="Last RSSI" value={`${session.last_rssi.toFixed(0)} dBm`} />
            <Field label="Last SNR" value={`${session.last_snr.toFixed(1)} dB`} />
            <Field label="Battery" value={batteryPercent(session.battery)} icon={<Battery size={13} />} />
            <Field
              label="Link margin"
              value={session.margin_db === undefined ? 'Not reported' : `${session.margin_db} dB`}
              icon={<Zap size={13} />}
            />
          </div>

          <div className="console-queue">
            <b>Waiting for the next window</b>
            {session.pending_downlink ? (
              <div className="console-queue-row">
                <span>
                  {session.pending_downlink.f_port === undefined
                    ? `MAC-only frame${session.pending_downlink.mac_commands ? ` · ${session.pending_downlink.mac_commands} command(s)` : ''}`
                    : `Port ${session.pending_downlink.f_port}`}
                  {session.pending_downlink.confirmed ? ' · confirmed' : ''}
                </span>
                {session.pending_downlink.data && <code>{session.pending_downlink.data}</code>}
                <button className="text-action" onClick={() => void cancel()}><Trash2 size={13} /> Cancel</button>
              </div>
            ) : (
              <span className="console-queue-empty">Nothing queued.</span>
            )}
          </div>

          <div className="console-composer">
            <label>
              Payload (hex)
              <input
                value={payload}
                onChange={(event) => setPayload(event.target.value)}
                placeholder="37 21 00"
                spellCheck={false}
              />
            </label>
            <label>
              FPort
              <input
                value={fPort}
                onChange={(event) => setFPort(event.target.value.replace(/\D/g, ''))}
                inputMode="numeric"
                maxLength={3}
              />
            </label>
            <label className="console-confirm">
              <input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />
              Confirmed
            </label>
            <button
              className="button button-primary"
              onClick={() => void send()}
              disabled={sending || payload.length > 0 && Boolean(payloadProblem)}
            >
              <Send size={14} /> {sending ? 'Queuing…' : 'Queue downlink'}
            </button>
            {payload.length > 0 && payloadProblem && <p className="form-error">{payloadProblem}</p>}
            <button className="text-action" onClick={() => void askStatus()}>
              <Battery size={13} /> Ask for a battery report
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
        </>
      )}

      {/* Readings come from the store rather than from the session, so they show
          even when the live session read failed — and they are the only place the
          console proves a payload formatter is doing anything. */}
      <RecentReadings devEUI={device.eui} />

      {/* Settings sit outside the session guard on purpose: a device that has
          never joined still has settings, and being unable to rename or pause a
          device before its first join would make provisioning a two-step process
          with no way to correct a mistake. */}
      <DeviceSettings device={device} onChanged={onChanged} onDeleted={onDeleted} />
    </aside>
  )
}

function Field({ label, value, icon }: { label: string; value: string; icon?: ReactNode }) {
  return (
    <div className="console-field">
      <span>{label}</span>
      <b>{icon}{value}</b>
    </div>
  )
}

/**
 * A compact duty-cycle readout for the gateways view.
 *
 * A sub-band sitting at its budget is the explanation for downlinks that simply
 * never appear, and without this the only way to find it is to read server logs.
 */
export function DutyCyclePanel({ usage, window: windowLabel, denials }: {
  usage: { gateway: string; sub_band: string; used: string; budget: string; transmits: number }[]
  window: string
  denials: Record<string, number>
}) {
  if (usage.length === 0) return null
  return (
    <article className="panel duty-panel">
      <div className="panel-heading">
        <div><h2>Channel occupancy</h2><p>Duty-cycle use per gateway, {windowLabel} window</p></div>
      </div>
      <div className="duty-list">
        {usage.map((row) => (
          <div className="duty-row" key={`${row.gateway}-${row.sub_band}`}>
            <code>{row.gateway}</code>
            <span>{row.sub_band}</span>
            <span>{row.transmits} tx</span>
            <span>{row.used} / {row.budget}</span>
          </div>
        ))}
      </div>
      {Object.keys(denials).length > 0 && (
        <p className="form-error" role="alert">
          Refused for exceeding the limit: {Object.entries(denials).map(([k, v]) => `${k} ×${v}`).join(', ')}
        </p>
      )}
    </article>
  )
}

/**
 * The device's recent stored uplinks, decoded.
 *
 * This is where a payload formatter either earns its keep or reveals that it has
 * never run. The session panel says the device is talking; this says what it said.
 * Three states stay distinct because each calls for a different action: no
 * formatter (hex on screen, so go associate a model), a formatter that rejected
 * the frame (its own error, per frame), and a reading (the fields themselves).
 */
function RecentReadings({ devEUI }: { devEUI: string }) {
  const [rows, setRows] = useState<UplinkRow[]>([])
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [problem, setProblem] = useState('')

  const load = useCallback(async () => {
    try {
      setRows(await fetchUplinks(devEUI, 10))
      setState('ready')
      setProblem('')
    } catch (error) {
      // An in-memory network server has no uplink history at all, and that is a
      // note rather than a failure: everything else on this panel still works.
      setRows([])
      setState('error')
      setProblem(failureMessage(error, 'Uplink history is unavailable.'))
    }
  }, [devEUI])

  useEffect(() => {
    // The reset rides along inside the deferred task rather than running in the
    // effect body: setting state there renders an empty panel for a frame before
    // the read has even started.
    const first = setTimeout(() => {
      setState('loading')
      void load()
    }, 0)
    return () => clearTimeout(first)
  }, [load])

  return (
    <div className="console-readings">
      <div className="console-subheading">
        <b>Recent readings</b>
        <button className="text-action" onClick={() => void load()}>
          <RefreshCw size={13} /> Reload
        </button>
      </div>
      {state === 'loading' && <p className="form-note">Reading stored uplinks…</p>}
      {state === 'error' && <p className="form-note">{problem}</p>}
      {state === 'ready' && rows.length === 0 && (
        <p className="form-note">No stored uplinks for this device yet.</p>
      )}
      {rows.map((row) => <ReadingRow key={row.uplink_id} row={row} />)}
    </div>
  )
}

/** One stored uplink: when it arrived, and what it turned into. */
function ReadingRow({ row }: { row: UplinkRow }) {
  const lines = formatDecoded(row.decoded)
  const at = new Date(row.received_at)
  return (
    <div className="console-reading">
      <div className="console-reading-top">
        <b>{Number.isNaN(at.getTime()) ? row.received_at : at.toLocaleTimeString()}</b>
        <span>
          #{row.f_cnt_up}
          {row.f_port >= 0 ? ` · port ${row.f_port}` : ' · MAC only'}
          {row.retransmission ? ' · repeat' : ''}
        </span>
      </div>
      {lines.length > 0 && (
        <div className="console-reading-values">
          {lines.map((line) => (
            <span key={line.key}><small>{line.key}</small>{line.value}</span>
          ))}
        </div>
      )}
      {lines.length === 0 && row.decode_error && (
        <p className="form-error">Decoder: {row.decode_error}</p>
      )}
      {lines.length === 0 && !row.decode_error && row.data && (
        <code title={row.has_codec ? 'The formatter returned nothing for this frame' : 'No payload formatter on this device’s model'}>
          {row.data}
        </code>
      )}
    </div>
  )
}

