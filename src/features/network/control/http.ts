/**
 * The one HTTP entry point of the control plane.
 *
 * Every control-plane call goes through request(): it is the only place that
 * knows the API prefix, the only place that decodes an error body, and the only
 * place that decides what a failed call throws. That concentration is the point —
 * a command either reaches the network server or comes back as a CommandRejected
 * carrying the server's own reason, and no caller has to parse a response to find
 * that out.
 */

import { API_PREFIX } from '../constants'

/** A command the network server refused, carrying the reason it gave. */
export class CommandRejected extends Error {
  readonly status: number
  readonly code: string
  readonly detail: string

  constructor(status: number, code: string, detail: string) {
    super(detail || code)
    this.status = status
    this.code = code
    this.detail = detail
  }
}

/**
 * What a rejection means to the operator.
 *
 * The status code is the actionable part. 502 and 503 look the same in a generic
 * error and call for different fixes: one is a network server that is running but
 * unreachable from the console, the other is a console started without one.
 */
const REJECTIONS: Record<number, string> = {
  400: 'The network server rejected the request. Check the payload and port.',
  404: 'This device has no session yet, so there is nothing to send to. It needs to join first.',
  409: 'Another frame is already queued for this device.',
  502: 'The network server could not be reached. Check that it is running.',
  503: 'This console is not connected to a network server.',
  504: 'The network server did not answer in time.',
}

export async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  /**
   * Per-operation wording for the statuses whose meaning depends on what was
   * asked. A 404 on a session read means "this device has not joined"; the same
   * status on a delete means the device is not provisioned at all. Sharing one
   * string for both would make one of them wrong.
   */
  rejections?: Partial<Record<number, string>>,
): Promise<T> {
  const response = await fetch(`${API_PREFIX}${path}`, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await response.text()
  if (!response.ok) {
    let code = `http_${response.status}`
    let detail = ''
    try {
      const parsed = JSON.parse(text) as { error?: string; detail?: string }
      code = parsed.error ?? code
      detail = parsed.detail ?? ''
    } catch {
      // A proxy's HTML error page is not JSON; the status still says what to do.
    }
    throw new CommandRejected(
      response.status,
      code,
      detail || rejections?.[response.status] || REJECTIONS[response.status] || 'The request failed.',
    )
  }
  return (text ? JSON.parse(text) : {}) as T
}

/**
 * The message to show for whatever a control-plane call threw.
 *
 * A CommandRejected already carries the network server's own reason, which is
 * better than anything the console can invent; a plain Error is a fetch that
 * never got a response, whose message is the only clue there is. Handling both
 * in one place is what stops a component from printing "undefined" or, worse,
 * swallowing the failure and leaving the operator to guess whether the command
 * went out.
 */
export function failureMessage(error: unknown, fallback = 'The request failed.'): string {
  if (error instanceof CommandRejected) return error.detail || error.code
  if (error instanceof Error && error.message) return error.message
  return fallback
}

/**
 * Whether the failure means "there is nothing here to act on" (HTTP 404).
 *
 * This one status deserves its own question because the right response is not an
 * error message but a different screen: a device with no session has not joined
 * yet, so the console should say what would make it join rather than report a
 * failure the operator cannot fix.
 */
export function isNotFound(error: unknown): boolean {
  return error instanceof CommandRejected && error.status === 404
}

/** Whether the failure means the network server itself is unreachable (502/503/504). */
export function isServerUnavailable(error: unknown): boolean {
  return error instanceof CommandRejected && error.status >= 502
}
