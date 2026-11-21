import { useCallback, useEffect, useState } from 'react'
import { ArrowLeftRight, CircleCheck, CircleOff, Plus, ShieldAlert, Trash2, TriangleAlert, X } from 'lucide-react'
import {
  brokerURLProblem,
  createIntegration,
  deleteIntegration,
  fetchIntegrations,
  integrationIdProblem,
  isBlockingProblem,
  testIntegration,
  updateIntegration,
  webhookURLProblem,
} from '../control/integrations'
import { failureMessage } from '../control/http'
import type { Integration, IntegrationInput, IntegrationKind, IntegrationProbe } from '../control/types'

/**
 * Where a decoded event goes the moment it arrives.
 *
 * Two destinations types, one loop: name it, point it at a broker or a URL, press
 * test, and see whether the far end accepted a real event. The test matters more
 * here than anywhere else in the console, because everything about an integration
 * is invisible until it fails — a wrong topic silently delivers nothing to nobody,
 * and the only symptom is a dashboard somewhere that quietly stops updating.
 *
 * Three things this page is careful about:
 *
 * - A credential is write-only. It is sent on save and never read back, so the
 *   form shows "a credential is stored" rather than the credential, and leaving
 *   the field blank means "keep what is there" rather than "clear it".
 * - "Saved" and "delivering" are different claims, so a destination shows whether
 *   the running process actually holds a connection.
 * - Destinations configured outside this console (through the network server's
 *   environment) are listed too, read-only. Omitting them would leave the page
 *   unable to account for traffic the operator can see arriving.
 */
export function Integrations() {
  const [integrations, setIntegrations] = useState<Integration[]>([])
  const [loadError, setLoadError] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [editing, setEditing] = useState<{ id: string; isNew: boolean } | null>(null)

  const load = useCallback(async () => {
    try {
      setIntegrations(await fetchIntegrations())
      setLoadError('')
    } catch (error) {
      // 503 means this console has no network server that can store destinations.
      // Saying so beats an empty list that implies nobody has configured one.
      setIntegrations([])
      setLoadError(failureMessage(error, 'Integrations are unavailable.'))
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
            <h2>Event destinations</h2>
            <p>Push each decoded reading to your own systems as it arrives</p>
          </div>
          <button className="button button-primary" onClick={() => setEditing({ id: '', isNew: true })}>
            <Plus size={15} />New destination
          </button>
        </div>

        {loadError && <p className="form-error" role="alert">{loadError}</p>}

        {loaded && !loadError && integrations.length === 0 && (
          <div className="empty-result">
            Nothing is being pushed out yet. Add an MQTT broker or a webhook URL and every
            decoded uplink is sent there the moment it is read.
          </div>
        )}

        {integrations.length > 0 && (
          <div className="model-list">
            {integrations.map((integration) => (
              <button
                key={`${integration.kind}:${integration.id}`}
                className="model-row"
                disabled={integration.managed_by_environment}
                onClick={() => setEditing({ id: integration.id, isNew: false })}
              >
                <span className="model-row-main">
                  <b>{integration.name}</b>
                  <small>
                    {integration.kind === 'mqtt'
                      ? `${integration.mqtt.broker_url}${integration.mqtt.topic ? ` → ${integration.mqtt.topic}` : ''}`
                      : integration.webhook.url}
                  </small>
                </span>
                <code>{integration.id}</code>
                {integration.managed_by_environment && (
                  <span className="model-codec">From server config</span>
                )}
                {!integration.enabled && (
                  <span className="model-codec model-codec-broken">
                    <CircleOff size={12} />Paused
                  </span>
                )}
                {integration.enabled && integration.kind === 'mqtt' && (
                  integration.connected
                    ? <span className="model-codec model-codec-on"><CircleCheck size={12} />Connected</span>
                    : <span className="model-codec model-codec-broken"><TriangleAlert size={12} />Not connected</span>
                )}
                {integration.enabled && integration.kind === 'webhook' && (
                  <span className="model-codec model-codec-on">Webhook</span>
                )}
                {integration.has_secret && <span className="model-codec">Credential stored</span>}
              </button>
            ))}
          </div>
        )}
      </section>

      {editing && (
        <IntegrationEditor
          id={editing.id}
          isNew={editing.isNew}
          known={integrations.find((i) => i.id === editing.id)}
          onClose={close}
          onCreated={(id) => setEditing({ id, isNew: false })}
        />
      )}
    </div>
  )
}

/**
 * One destination's editor.
 *
 * The credential field starts empty on an edit and is only sent when the operator
 * types something, which is what lets the server tell "keep it" from "clear it".
 * The test button probes the *stored* destination rather than the form, so it
 * answers the question an operator can act on: after saving, does the far end
 * accept our events?
 */
export function IntegrationEditor({ id, isNew, known, onClose, onCreated }: {
  id: string
  isNew: boolean
  /** The stored record, when editing. Used to prefill without inventing defaults. */
  known?: Integration
  onClose: () => void
  onCreated: (id: string) => void
}) {
  const [kind, setKind] = useState<IntegrationKind>(known?.kind ?? 'mqtt')
  const [destinationID, setDestinationID] = useState(id)
  const [name, setName] = useState(known?.name ?? '')
  const [enabled, setEnabled] = useState(known?.enabled ?? true)
  const [brokerURL, setBrokerURL] = useState(known?.mqtt.broker_url ?? '')
  const [topic, setTopic] = useState(known?.mqtt.topic ?? 'fieldnode/events')
  const [username, setUsername] = useState(known?.mqtt.username ?? '')
  const [qos, setQos] = useState(known?.mqtt.qos ?? 0)
  const [retain, setRetain] = useState(known?.mqtt.retain ?? false)
  const [insecure, setInsecure] = useState(known?.mqtt.insecure_skip_verify ?? false)
  const [webhookURL, setWebhookURL] = useState(known?.webhook.url ?? '')
  const [secret, setSecret] = useState('')

  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [probing, setProbing] = useState(false)
  const [probe, setProbe] = useState<IntegrationProbe | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const hasStoredSecret = Boolean(known?.has_secret)
  const idProblem = integrationIdProblem(destinationID)
  const targetProblem = kind === 'mqtt' ? brokerURLProblem(brokerURL) : webhookURLProblem(webhookURL)
  const nameProblem = name.trim() === '' ? 'Give the destination a name you will recognise in an alert.' : ''
  const blocked = Boolean(idProblem || nameProblem || isBlockingProblem(targetProblem))

  function input(): IntegrationInput {
    return {
      id: destinationID.trim(),
      kind,
      name: name.trim(),
      enabled,
      webhook: { url: webhookURL.trim() },
      mqtt: {
        broker_url: brokerURL.trim(),
        topic: topic.trim(),
        username: username.trim(),
        qos,
        retain,
        insecure_skip_verify: insecure,
      },
      // Absent unless the operator typed one, which is how the server knows to
      // leave a stored credential alone.
      secret: secret === '' ? undefined : secret,
    }
  }

  async function save() {
    if (blocked) return
    setSaving(true)
    setError('')
    try {
      const saved = isNew
        ? await createIntegration(input())
        : await updateIntegration(destinationID, input())
      if (isNew) onCreated(saved.id)
      // The typed credential is dropped from the form either way: keeping it
      // would leave a password sitting in page state for no reason.
      setSecret('')
    } catch (failure) {
      // The network server's own wording survives here, which is what turns
      // "save failed" into "tls:// needs a reachable broker".
      setError(failureMessage(failure, 'The destination could not be saved.'))
    } finally {
      setSaving(false)
    }
  }

  async function probeNow() {
    setProbing(true)
    setProbe(await testIntegration(destinationID))
    setProbing(false)
  }

  async function remove() {
    try {
      await deleteIntegration(destinationID)
      onClose()
    } catch (failure) {
      setError(failureMessage(failure, 'The destination could not be deleted.'))
      setConfirmDelete(false)
    }
  }

  return (
    <section className="panel model-editor">
      <div className="panel-heading">
        <div>
          <h2>{isNew ? 'New destination' : name || destinationID}</h2>
          <p>Where decoded events are pushed, and how the network server reaches it</p>
        </div>
        <button className="icon-button" onClick={onClose} aria-label="Close"><X size={16} /></button>
      </div>

      <div className="model-form integration-form">
        {/* A fieldset rather than a div so assistive tech announces two radio
            buttons inside a named group, and the native inputs stay native so
            the arrow keys move between them. Each option carries its own
            description: the point of showing both at once is to compare them,
            and text that changes with the selection cannot be compared. */}
        <fieldset className="option-group">
          <legend>Destination type</legend>
          <div className="option-row">
            <label className={`option${kind === 'mqtt' ? ' is-selected' : ''}`}>
              <input type="radio" name="integration-kind" checked={kind === 'mqtt'} onChange={() => setKind('mqtt')} />
              <b>MQTT broker</b>
              <span>The connection stays open, so an event leaves as it is produced. Your systems subscribe to a topic.</span>
            </label>
            <label className={`option${kind === 'webhook' ? ' is-selected' : ''}`}>
              <input type="radio" name="integration-kind" checked={kind === 'webhook'} onChange={() => setKind('webhook')} />
              <b>Webhook</b>
              <span>One JSON POST per event, which an existing HTTP service can accept without running a broker.</span>
            </label>
          </div>
        </fieldset>

        {/* Each field is one grid cell: caption, control, help and error. The
            error lives inside the label because the form is an auto-fit grid,
            and a sibling paragraph becomes a cell of its own column — which is
            how a validation message ended up beside the wrong field. A span
            rather than a p because a label may only hold phrasing content. */}
        <label>
          Destination id
          <input
            value={destinationID}
            onChange={(event) => setDestinationID(event.target.value)}
            disabled={!isNew}
            placeholder="cloud-mq"
            spellCheck={false}
          />
          {isNew
            ? <small className="field-hint">A stable slug. It appears in logs and in tickets, so pick something a person will still recognise in a year.</small>
            : <small className="field-hint">The id cannot be changed afterwards; delete and re-add to rename it.</small>}
          {isNew && idProblem && <span className="form-error">{idProblem}</span>}
        </label>

        <label>
          Display name
          <input value={name} onChange={(event) => setName(event.target.value)} placeholder="Cloud broker" />
          {nameProblem && <span className="form-error">{nameProblem}</span>}
        </label>

        <div className="check-block">
          <label className="console-toggle">
            <input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />
            Delivering
          </label>
          <p className="form-note">
            Pausing keeps the settings and stops the network server connecting or sending.
          </p>
        </div>

        {kind === 'mqtt' ? (
          <>
            <label>
              Broker address
              <input
                value={brokerURL}
                onChange={(event) => setBrokerURL(event.target.value)}
                placeholder="tls://broker.example.com:8883"
                spellCheck={false}
              />
              <small className="field-hint">
                tcp:// for a plain connection, tls:// when the broker expects TLS. 8883 is the usual TLS port, 1883 the plain one.
              </small>
              {targetProblem && (
                <span className={isBlockingProblem(targetProblem) ? 'form-error' : 'form-note'}>{targetProblem}</span>
              )}
            </label>

            <label>
              Topic
              <input value={topic} onChange={(event) => setTopic(event.target.value)} placeholder="fieldnode/events" spellCheck={false} />
              <small className="field-hint">
                The device&apos;s id and the event kind are appended, so a subscriber can filter on either.
              </small>
            </label>

            <label>
              Username
              <input value={username} onChange={(event) => setUsername(event.target.value)} spellCheck={false} />
              <small className="field-hint">Optional. The client id is derived from the destination id when one is not set.</small>
            </label>

            <label>
              Quality of service
              <select value={qos} onChange={(event) => setQos(Number(event.target.value))}>
                <option value={0}>0 — at most once, fastest, may drop</option>
                <option value={1}>1 — at least once, may repeat</option>
                <option value={2}>2 — exactly once, slowest</option>
              </select>
              <small className="field-hint">
                1 is the usual choice: nothing is lost, and a subscriber that keys on the event id can ignore the repeats it allows.
              </small>
            </label>

            {/* Both options and the warning they can raise in one cell, so the
                warning sits under the box that caused it rather than in
                whichever column the grid happened to reach. */}
            <div className="check-block">
              <label className="console-toggle">
                <input type="checkbox" checked={retain} onChange={(event) => setRetain(event.target.checked)} />
                Retain the last message per topic
              </label>

              <label className="console-toggle">
                <input type="checkbox" checked={insecure} onChange={(event) => setInsecure(event.target.checked)} />
                Accept any broker certificate
              </label>
              {insecure && (
                <p className="form-note">
                  <ShieldAlert size={13} /> This encrypts the traffic but does not check who is on the other end,
                  so someone on the path could read device data or impersonate the broker.
                </p>
              )}
            </div>
          </>
        ) : (
          <label>
            Webhook URL
            <input
              value={webhookURL}
              onChange={(event) => setWebhookURL(event.target.value)}
              placeholder="https://example.com/lorawan/events"
              spellCheck={false}
            />
            <small className="field-hint">
              Each event is a JSON POST. When a signing secret is stored it arrives in the X-Fieldnode-Signature header as an HMAC of the body.
            </small>
            {targetProblem && (
              <span className={isBlockingProblem(targetProblem) ? 'form-error' : 'form-note'}>{targetProblem}</span>
            )}
          </label>
        )}

        <label>
          {kind === 'mqtt' ? 'Password or token' : 'Signing secret'}
          <input
            type="password"
            value={secret}
            onChange={(event) => setSecret(event.target.value)}
            placeholder={hasStoredSecret ? 'A credential is stored — type a new one to replace it' : 'Optional'}
            autoComplete="new-password"
            spellCheck={false}
          />
          <small className="field-hint">
            {hasStoredSecret
              ? 'A stored credential is never shown and cannot be read back. Leaving this blank keeps it.'
              : 'Never displayed again after saving, and never sent back to this page.'}
          </small>
        </label>
        {hasStoredSecret && !secret && (
          <p className="form-note"><CircleCheck size={13} /> A credential is stored for this destination.</p>
        )}
      </div>

      {error && <p className="form-error" role="alert">{error}</p>}

      {probe && (
        <p className={probe.ok ? 'form-note' : 'form-error'} role="status">
          {probe.ok ? <CircleCheck size={13} /> : <TriangleAlert size={13} />} {probe.detail}
        </p>
      )}

      <div className="modal-actions">
        {!isNew && (
          <button className="button button-secondary" onClick={() => void probeNow()} disabled={probing || saving}>
            <ArrowLeftRight size={15} />{probing ? 'Testing…' : 'Send a test event'}
          </button>
        )}
        {!isNew && (
          confirmDelete
            ? (
              <span className="model-delete-confirm">
                Delete this destination?
                <button className="button button-danger" onClick={() => void remove()}>Yes, delete</button>
                <button className="button button-secondary" onClick={() => setConfirmDelete(false)}>Keep it</button>
              </span>
            )
            : <button className="text-action danger" onClick={() => setConfirmDelete(true)}><Trash2 size={14} />Delete</button>
        )}
        <button className="button button-primary" onClick={() => void save()} disabled={saving || blocked}>
          {saving ? 'Saving…' : isNew ? 'Create destination' : 'Save'}
        </button>
      </div>
    </section>
  )
}

