import { useCallback, useEffect, useState } from 'react'
import { Braces, CircleCheck, Plus, TriangleAlert, X } from 'lucide-react'
import { createModel, deleteModel, fetchModel, fetchModels, modelIdProblem, testModelDecode, updateModel } from './control/models'
import type { DecodeTrial } from './control/models'
import { formatDecoded } from './control/traffic'
import type { DeviceModel, NetworkStatus } from './control/types'
import { DecodeHealthPanel } from './NetworkHealth'
import { failureMessage } from './control/http'

/**
 * Device models: the sensor types on this network and how their bytes are read.
 *
 * This is where a vendor's payload formatter lives. The view is built around the
 * loop an operator actually follows: paste the JavaScript the vendor printed in a
 * datasheet, run it against a frame from that same datasheet, look at the numbers,
 * save it, then point devices at it. Everything else here keeps that honest:
 *
 * - The test runs the script in the editor, not the stored one, so an edit can be
 *   proven before it reaches a fleet.
 * - A script that will not compile is refused by the server with the parser's own
 *   message, which is shown verbatim rather than turned into "save failed".
 * - Deleting says how many devices lose their association, because those devices
 *   keep reporting and simply stop being decoded — a consequence otherwise
 *   invisible until someone wonders where the readings went.
 */
export function DeviceModels({ decoding }: { decoding?: NetworkStatus['decoding'] } = {}) {
  const [models, setModels] = useState<DeviceModel[]>([])
  const [loadError, setLoadError] = useState('')
  const [loaded, setLoaded] = useState(false)

  // The editor works on one model at a time. `editing` is null when the list is
  // showing; a new model is the same editor with no stored record behind it.
  const [editing, setEditing] = useState<{ modelId: string; isNew: boolean } | null>(null)

  // Which of these models the server has actually failed to compile. Only the
  // network server knows: it is the one that runs the script, and a model can
  // look perfectly configured in the database while every frame from its devices
  // goes undecoded.
  const brokenIds = new Set((decoding?.broken_models ?? []).map((b) => b.model_id))

  const load = useCallback(async () => {
    try {
      setModels(await fetchModels())
      setLoadError('')
    } catch (error) {
      // A console without a network server, or one whose database cannot hold
      // models, answers 503. Saying so beats an empty list that implies nobody has
      // created a model yet.
      setModels([])
      setLoadError(failureMessage(error, 'Device models are unavailable.'))
    } finally {
      setLoaded(true)
    }
  }, [])

  useEffect(() => {
    const first = setTimeout(() => void load(), 0)
    return () => clearTimeout(first)
  }, [load])

  function close() {
    setEditing(null)
    void load()
  }

  return (
    <div className="model-view">
      <section className="panel model-panel">
        <div className="panel-heading">
          <div>
            <h2>Device models</h2>
            <p>What each kind of sensor is, and how its payload is decoded</p>
          </div>
          <button className="button button-primary" onClick={() => setEditing({ modelId: '', isNew: true })}>
            <Plus size={15} />New model
          </button>
        </div>

        {loadError && <p className="form-error" role="alert">{loadError}</p>}

        {loaded && !loadError && models.length === 0 && (
          <div className="empty-result">
            No device models yet. A model is what turns a device&apos;s bytes into readings —
            until one exists and a device points at it, uplinks are stored and shown as hex.
          </div>
        )}

        {models.length > 0 && (
          <div className="model-list">
            {models.map((model) => (
              <button
                key={model.model_id}
                className="model-row"
                onClick={() => setEditing({ modelId: model.model_id, isNew: false })}
              >
                <span className="model-row-main">
                  <b>{model.name}</b>
                  <small>{model.description || 'No description'}</small>
                </span>
                <code>{model.model_id}</code>
                {/* The server has actually tried to compile this one, so its
                    verdict outranks "a script is present". */}
                {brokenIds.has(model.model_id) && (
                  <span className="model-codec model-codec-broken">
                    <TriangleAlert size={12} />Formatter failing
                  </span>
                )}
                <span className={`model-codec ${model.codec_present ? 'model-codec-on' : ''}`}>
                  {model.codec_present && <Braces size={12} />}
                  {model.codec_present ? 'JS formatter' : 'No formatter'}
                </span>
              </button>
            ))}
          </div>
        )}
      </section>

      <DecodeHealthPanel decoding={decoding} />

      {editing && (
        <ModelEditor
          modelId={editing.modelId}
          isNew={editing.isNew}
          onClose={close}
          onCreated={(modelId) => setEditing({ modelId, isNew: false })}
        />
      )}
    </div>
  )
}

/**
 * One model's editor: identity, formatter, and a test bench for the formatter.
 *
 * The stored record is loaded before the form is filled, for the same reason the
 * device settings panel does it: a form that assumed defaults would write those
 * assumptions over a formatter nobody looked at — and here the thing being
 * overwritten is the code that decodes a whole group of devices.
 */
function ModelEditor({ modelId, isNew, onClose, onCreated }: {
  modelId: string
  isNew: boolean
  onClose: () => void
  /** After a create the editor becomes an edit of the same slug. */
  onCreated: (modelId: string) => void
}) {
  const [stored, setStored] = useState<DeviceModel | null>(null)
  const [loadError, setLoadError] = useState('')
  const [id, setId] = useState(modelId)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [kind, setKind] = useState<'NONE' | 'JS'>('JS')
  const [script, setScript] = useState('')

  const [saving, setSaving] = useState(false)
  const [feedback, setFeedback] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [deleting, setDeleting] = useState(false)

  // The test bench. Running a test never saves, which is what makes it safe to
  // point at a production model.
  const [payload, setPayload] = useState('')
  const [fPort, setFPort] = useState('10')
  const [trial, setTrial] = useState<DecodeTrial | null>(null)
  const [testing, setTesting] = useState(false)
  const [testProblem, setTestProblem] = useState('')

  useEffect(() => {
    let active = true
    // One deferred task for both branches. A new model has nothing stored, and
    // the reset still has to happen: React reuses this component instance when
    // the operator goes from editing one model to creating another, so the
    // previous model's script would otherwise still be in the textarea.
    const timer = setTimeout(async () => {
      if (isNew) {
        if (!active) return
        setStored(null)
        setLoadError('')
        return
      }
      try {
        const found = await fetchModel(modelId)
        if (!active) return
        setStored(found)
        setId(found.model_id)
        setName(found.name)
        setDescription(found.description ?? '')
        setKind(found.codec_kind)
        setScript(found.codec_script ?? '')
        setLoadError('')
      } catch (error) {
        if (!active) return
        setStored(null)
        setLoadError(failureMessage(error, 'This model could not be read.'))
      }
    }, 0)
    return () => {
      active = false
      clearTimeout(timer)
    }
  }, [modelId, isNew])

  const idProblem = isNew ? modelIdProblem(id.trim()) : ''
  const nameProblem = name.trim() === '' ? 'A model needs a name an operator will recognise later.' : ''
  const scriptProblem = kind === 'JS' && script.trim() === ''
    ? 'A JS model needs a formatter. Choose “None” to keep this model’s payloads as hex.'
    : ''
  // Compared against the loaded record, not against the form's own defaults: an
  // unsaved edit is worth naming, and a form that always looked dirty would leave
  // the save button enabled over a formatter that was never changed.
  const changed = stored === null
    ? isNew
    : name.trim() !== stored.name
      || description.trim() !== (stored.description ?? '')
      || kind !== stored.codec_kind
      || script !== (stored.codec_script ?? '')
  const blocked = Boolean(idProblem || nameProblem || scriptProblem) || (!changed && !isNew)

  async function save() {
    if (blocked || saving) return
    setSaving(true)
    setFeedback(null)
    const slug = id.trim()
    const input = { name: name.trim(), description: description.trim(), codecKind: kind, codecScript: script }
    try {
      const saved = isNew ? await createModel(slug, input) : await updateModel(slug, input)
      setStored(saved)
      setId(saved.model_id)
      setName(saved.name)
      setDescription(saved.description ?? '')
      setKind(saved.codec_kind)
      setScript(saved.codec_script ?? '')
      if (isNew) onCreated(saved.model_id)
      setFeedback({
        kind: 'ok',
        text: `Saved. Devices on “${saved.model_id}” are decoded with this formatter from their next uplink.`,
      })
    } catch (error) {
      setFeedback({
        kind: 'error',
        // The server's sentence is the useful one: for a bad formatter it names the
        // line and token, which is the difference between a two-minute fix and a
        // bug report about the console.
        text: failureMessage(error, 'The model could not be saved.'),
      })
    } finally {
      setSaving(false)
    }
  }

  async function remove() {
    if (deleting) return
    setDeleting(true)
    setFeedback(null)
    try {
      const result = await deleteModel(modelId)
      const lost = result.devices_unassociated
      setFeedback({
        kind: 'ok',
        text: lost > 0
          ? `Deleted. ${lost} device${lost === 1 ? '' : 's'} lost their model and keep reporting without being decoded.`
          : 'Deleted.',
      })
      // Held briefly so the consequence is read before the panel closes.
      setTimeout(onClose, 1800)
    } catch (error) {
      setFeedback({ kind: 'error', text: failureMessage(error, 'The model could not be deleted.') })
      setDeleting(false)
      setConfirmDelete(false)
    }
  }

  async function runTest() {
    if (testing) return
    const hex = payload.replace(/\s+/g, '')
    if (hex === '') {
      setTestProblem('Paste the frame payload as hex, for example 0A2BFF.')
      setTrial(null)
      return
    }
    if (!/^[0-9a-fA-F]+$/.test(hex) || hex.length % 2 !== 0) {
      setTestProblem('Use hexadecimal digits, an even number of them.')
      setTrial(null)
      return
    }
    const port = Number(fPort.trim())
    if (!Number.isInteger(port) || port < 0 || port > 255) {
      setTestProblem('The port must be a whole number between 0 and 255.')
      setTrial(null)
      return
    }
    setTestProblem('')
    setTesting(true)
    setTrial(null)
    try {
      // The script travels with the request, so what gets tested is what is in the
      // editor right now rather than whatever was saved last.
      const result = await testModelDecode(id.trim() || 'untitled', {
        payload: hex, fPort: port, script: script.trim() || undefined,
      })
      setTrial(result)
    } catch (error) {
      setTestProblem(failureMessage(error, 'The test could not run.'))
    } finally {
      setTesting(false)
    }
  }

  return (
    <section className="panel model-editor">
      <div className="panel-heading">
        <div>
          <h2>{isNew ? 'New device model' : `Edit ${modelId}`}</h2>
          <p>The formatter runs on every uplink of every device using this model</p>
        </div>
        <button className="icon-button" onClick={onClose} aria-label="Close model editor"><X size={16} /></button>
      </div>

      {loadError && <p className="form-error" role="alert">{loadError}</p>}

      <div className="model-form">
        <label>
          Model id
          <input
            value={id}
            onChange={(event) => setId(event.target.value)}
            disabled={!isNew}
            placeholder="cold-chain-probe-v2"
            spellCheck={false}
          />
          {isNew
            ? <small className="field-hint">A stable slug. It appears in device records and in tickets, so choose something a person will still recognise in a year.</small>
            : <small className="field-hint">The id cannot be renamed — devices reference it. Create a new model and re-point devices instead.</small>}
          {idProblem && <small className="form-error">{idProblem}</small>}
        </label>

        <label>
          Name
          <input value={name} onChange={(event) => setName(event.target.value)} maxLength={120} placeholder="Cold-chain probe, revision 2" />
          {nameProblem && <small className="form-error">{nameProblem}</small>}
        </label>

        <label>
          Description
          <input
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            maxLength={400}
            placeholder="Vendor, model number, what the fields mean"
          />
        </label>

        <label>
          Payload formatter
          <select value={kind} onChange={(event) => setKind(event.target.value as 'NONE' | 'JS')}>
            <option value="JS">JavaScript (vendor decoder)</option>
            <option value="NONE">None — keep payloads as hex</option>
          </select>
          {scriptProblem && <small className="form-error">{scriptProblem}</small>}
        </label>
      </div>

      {kind === 'JS' && (
        <label className="model-script">
          <span>Decoder script — define <code>decodeUplink(bytes, fPort)</code></span>
          <textarea
            value={script}
            onChange={(event) => setScript(event.target.value)}
            spellCheck={false}
            rows={14}
            placeholder={'function decodeUplink(bytes, fPort) {\n  return { temperature: bytes[0] / 2 }\n}'}
          />
        </label>
      )}

      {kind === 'JS' && (
        <div className="model-test">
          <div className="model-test-controls">
            <label>
              Frame payload (hex)
              <input
                value={payload}
                onChange={(event) => setPayload(event.target.value)}
                placeholder="0A2BFF"
                spellCheck={false}
              />
            </label>
            <label>
              Port
              <input value={fPort} onChange={(event) => setFPort(event.target.value)} inputMode="numeric" />
            </label>
            <button className="button button-secondary" onClick={() => void runTest()} disabled={testing}>
              {testing ? 'Running…' : 'Test decode'}
            </button>
          </div>
          {testProblem && <p className="form-error" role="alert">{testProblem}</p>}
          {trial && <DecodeTrialResult trial={trial} />}
          {!trial && !testProblem && (
            <p className="form-note">
              Runs the script above against the bytes you paste, without saving anything. A
              frame from the vendor&apos;s datasheet is the usual input.
            </p>
          )}
        </div>
      )}

      {feedback && (
        <p
          className={feedback.kind === 'ok' ? 'form-success' : 'form-error'}
          role={feedback.kind === 'ok' ? 'status' : 'alert'}
        >
          {feedback.text}
        </p>
      )}

      <div className="modal-actions">
        <button className="button button-primary" onClick={() => void save()} disabled={blocked || saving}>
          {saving ? 'Saving…' : isNew ? 'Create model' : 'Save changes'}
        </button>
        {!isNew && !confirmDelete && (
          <button className="button button-secondary" onClick={() => setConfirmDelete(true)}>Delete model</button>
        )}
        {confirmDelete && (
          <span className="model-delete-confirm">
            Delete {modelId}? Devices using it keep reporting but stop being decoded.
            <button className="button button-danger" onClick={() => void remove()} disabled={deleting}>
              {deleting ? 'Deleting…' : 'Yes, delete'}
            </button>
            <button className="button button-secondary" onClick={() => setConfirmDelete(false)}>Keep it</button>
          </span>
        )}
      </div>
    </section>
  )
}

/**
 * A test result.
 *
 * Success and failure are both ordinary outcomes here, so both render as content
 * rather than as an error banner: the question this panel answers is "does this
 * script read these bytes", and "no, because…" is an answer worth reading in full.
 */
function DecodeTrialResult({ trial }: { trial: DecodeTrial }) {
  const lines = formatDecoded(trial.data)
  if (!trial.ok) {
    return (
      <p className="form-error" role="alert">
        <TriangleAlert size={13} /> {trial.error || 'The formatter rejected these bytes.'}
        {trial.elapsed_ms !== undefined && <small> · {trial.elapsed_ms} ms</small>}
      </p>
    )
  }
  return (
    <div className="model-test-result">
      <b>
        <CircleCheck size={13} /> Decoded
        {trial.elapsed_ms !== undefined ? ` in ${trial.elapsed_ms} ms` : ''}
      </b>
      {lines.length === 0 ? (
        <p className="form-note">The formatter returned an object with no fields.</p>
      ) : (
        <div className="model-reading">
          {lines.map((line) => (
            <div key={line.key}><span>{line.key}</span><b>{line.value}</b></div>
          ))}
        </div>
      )}
    </div>
  )
}