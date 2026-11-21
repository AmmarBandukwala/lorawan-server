/**
 * Integration destinations: pushing decoded events somewhere in real time.
 *
 * The console never talks to a broker or a webhook itself. It asks the network
 * server to do it, because that is the process holding the connection, the
 * credential and the delivery counters — and it is the only thing that can say
 * whether a destination actually works.
 */

import type { Integration, IntegrationInput, IntegrationProbe } from './types'
import { CommandRejected, request } from './http'

/** The id rule the server enforces, mirrored here for a local message. */
export const INTEGRATION_ID_PATTERN = /^[a-z0-9][a-z0-9_-]{1,63}$/

export function integrationIdProblem(id: string): string {
  if (id === '') return 'Give the destination an id, for example "cloud-mq".'
  if (!INTEGRATION_ID_PATTERN.test(id)) {
    return 'An id is 2 to 64 characters of lower-case letters, digits, "-" or "_", starting with a letter or digit.'
  }
  return ''
}

/**
 * Whether a broker URL looks usable, before sending it anywhere.
 *
 * Only the shape is checked. Reachability is the network server's job — it is the
 * process that has to hold the connection, and it is the only one whose network
 * the broker is actually reachable from. A check here that went further would
 * report a broker reachable from the server but not from a browser as broken.
 */
export function brokerURLProblem(raw: string): string {
  const value = raw.trim()
  if (value === '') return 'Enter the broker address, for example "tls://broker.example.com:8883".'
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return 'That is not a valid address. Use tcp://host:1883 or tls://host:8883.'
  }
  if (!['tcp:', 'tls:', 'ws:', 'wss:'].includes(parsed.protocol)) {
    return 'Use tcp:// for a plain connection, or tls:// when the broker expects TLS.'
  }
  if (parsed.hostname === '') return 'The address needs a host, for example "tls://broker.example.com:8883".'
  if (parsed.protocol === 'tcp:' && !parsed.hostname.includes('localhost') && parsed.hostname !== '127.0.0.1') {
    // Worth saying, not worth refusing: a plain connection across the internet
    // carries the device readings and the credential in the clear.
    return 'tcp:// sends payloads and credentials unencrypted. Use tls:// unless this broker is on your own network.'
  }
  return ''
}

/** The same reasoning for a webhook target. */
export function webhookURLProblem(raw: string): string {
  const value = raw.trim()
  if (value === '') return 'Enter the URL that should receive the events.'
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return 'That is not a valid URL.'
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return 'A webhook URL has to start with http:// or https://.'
  }
  if (parsed.protocol === 'http:' && !parsed.hostname.includes('localhost') && parsed.hostname !== '127.0.0.1') {
    return 'http:// sends payloads and the signing secret unencrypted. Use https:// unless this endpoint is on your own network.'
  }
  return ''
}

/**
 * Whether a problem string should stop a save.
 *
 * The two checks above also return advice — "use tls://" — that reads exactly
 * like a refusal. Only the ones that make a destination impossible should block
 * it; the rest are shown and let through, because refusing to save a working
 * local broker because it is not encrypted is its own kind of unhelpful.
 */
export function isBlockingProblem(problem: string): boolean {
  if (problem === '') return false
  return !problem.startsWith('tcp://') && !problem.startsWith('http://')
}

export const fetchIntegrations = () =>
  request<{ integrations: Integration[] }>('GET', '/integrations').then((r) => r.integrations ?? [])

export const createIntegration = (input: IntegrationInput) =>
  request<Integration>('POST', '/integrations', payload(input), {
    503: 'This console is not connected to a network server that can store integrations.',
  })

export const updateIntegration = (id: string, input: IntegrationInput) =>
  request<Integration>('PUT', `/integrations/${id}`, payload(input), {
    404: 'That destination no longer exists. It may have been deleted in another tab.',
  })

export const deleteIntegration = (id: string) =>
  request<void>('DELETE', `/integrations/${id}`, undefined, {
    404: 'That destination no longer exists.',
  })

/**
 * Sends one probe event to a stored destination.
 *
 * A refused probe is a resolved promise with ok:false, not a rejection: the
 * request worked and the destination did not, and the reason is the useful part.
 * Only a transport or console failure rejects.
 */
export async function testIntegration(id: string): Promise<IntegrationProbe> {
  try {
    return await request<IntegrationProbe>('POST', `/integrations/${id}/test`)
  } catch (error) {
    // A 200 with ok:false already came back as a value, so anything reaching here
    // is a genuine failure. The network server's own reason is still the best
    // thing to show.
    if (error instanceof CommandRejected) {
      return { ok: false, detail: error.detail || error.code }
    }
    return { ok: false, detail: 'The test could not be sent.' }
  }
}

/**
 * The request body. Built explicitly rather than by spreading the input so a
 * credential the operator left alone is genuinely absent from the JSON, which is
 * how the server knows not to overwrite it.
 */
function payload(input: IntegrationInput) {
  const body: Record<string, unknown> = {
    id: input.id,
    kind: input.kind,
    name: input.name,
    enabled: input.enabled,
    webhook: input.webhook,
    mqtt: input.mqtt,
  }
  if (input.secret !== undefined) body.secret = input.secret
  return body
}
