/**
 * Control plane: what the console asks the network to *do*.
 *
 * Separate from api.ts because the two have different failure semantics. A
 * snapshot read that fails degrades one panel and keeps its last good values; a
 * command that fails means a device did not get a frame, and the operator needs
 * the network server's own reason rather than a degraded badge.
 *
 * The response types mirror the network server's JSON field-for-field, in
 * snake_case, on purpose. Renaming fields to camelCase here would put a
 * translation layer between the operator and the thing that knows the answer, and
 * a field the console does not translate is a field that silently disappears from
 * the UI after a network-server upgrade.
 */

const API_PREFIX = '/api/v1'

/** The session the network is currently running for one device. */
export type DeviceSession = {
  dev_eui: string
  dev_addr: string
  region: string
  f_cnt_up: number
  f_cnt_down: number
  data_rate: number
  tx_power_index: number
  nb_trans: number
  /** FCtrl.ADR of the last uplink: whether the device is following ADR. */
  adr: boolean
  rx1_delay: number
  rx1_dr_offset: number
  rx2_frequency_hz: number
  rx2_data_rate: number
  /** Duty-cycle fraction the device was told to obey; 0 means band default. */
  duty_cycle: number
  last_rssi: number
  last_snr: number
  /** Absent until the session's first uplink. */
  last_uplink_at?: string
  /**
   * LoRaWAN battery level, 0..255 where 255 means "the device cannot report".
   * Absent until the device has answered a DevStatusReq, which keeps "unknown"
   * distinguishable from "empty".
   */
  battery?: number
  /** Link margin in dB above the demodulation floor, same condition as battery. */
  margin_db?: number
  pending_downlink?: PendingDownlink
}

export type PendingDownlink = {
  confirmed: boolean
  /** Absent for a frame that carries only MAC commands. */
  f_port?: number
  /** Frame payload as hex. */
  data?: string
  /** How many MAC commands are riding in FOpts. */
  mac_commands?: number
}

export type GatewayHealth = {
  gateway_eui: string
  address: string
  protocol_version: number
  online: boolean
  last_seen: string
  packets: number
  downlinks_sent: number
  downlink_errors: number
  /** The concentrator's own words about the last refused transmission. */
  last_tx_ack_error?: string
  stats_at?: string
  rx_received: number
  rx_ok: number
  tx_received: number
  latitude?: number
  longitude?: number
  altitude?: number
}

export type SubBandUsage = {
  gateway: string
  region: string
  sub_band: string
  /** Durations arrive as Go time.Duration strings, e.g. "1.234s". */
  used: string
  budget: string
  window: string
  transmits: number
}

export type NetworkStatus = {
  uptime_seconds: number
  region: string
  net_id: string
  downlink_power_dbm: number
  gateways_total: number
  gateways_online: number
  gateways: GatewayHealth[]
  integrations?: {
    sinks: string[]
    published: number
    delivery_failures: number
    dropped: number
    queued: number
  }
  duty_cycle?: {
    window: string
    usage: SubBandUsage[]
    denials: Record<string, number>
  }
}

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

async function request<T>(
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

export const fetchSession = (devEUI: string) =>
  request<DeviceSession>('GET', `/devices/${devEUI}/session`)

export const fetchStatus = () => request<NetworkStatus>('GET', '/status')

/**
 * Queues a downlink.
 *
 * Resolving means queued, not transmitted: a class-A device listens for two
 * seconds after it speaks and then goes quiet, so the frame leaves in whatever
 * window it opens next. The UI must not claim otherwise.
 */
export const enqueueDownlink = (
  devEUI: string,
  input: { data: string; fPort?: number; confirmed?: boolean },
) =>
  request<{ dev_eui: string; queued: boolean; f_port: number; confirmed: boolean; explanation: string }>(
    'POST',
    `/devices/${devEUI}/downlink`,
    { data: input.data, f_port: input.fPort, confirmed: input.confirmed ?? false },
  )

export const cancelDownlink = (devEUI: string) =>
  request<{ dev_eui: string; cancelled: boolean }>('DELETE', `/devices/${devEUI}/downlink`)

/** Asks the device for a battery and link-margin report on its next uplink. */
export const requestDeviceStatus = (devEUI: string) =>
  request<{ dev_eui: string; scheduled: boolean }>('POST', `/devices/${devEUI}/devstatus`, {})

/**
 * What an operator can change about a device once it is on the network.
 *
 * Every field is optional and omitted rather than sent as null, because the
 * network server distinguishes "not mentioned" from "set to false". A form that
 * sent every field on every save would silently re-enable ADR on a device whose
 * name was the only thing edited.
 *
 * Credentials are not among them. The network server never returns keys, so a
 * console could not resupply them; editing works precisely because it does not
 * ask for what it cannot have.
 */
export interface DeviceEdit {
  name?: string
  group?: string
  enabled?: boolean
  disableAdr?: boolean
  supportsClassC?: boolean
  /** -1 means "whatever the band allows". */
  maxDataRate?: number
  minDataRate?: number
  maxTxPower?: number
}

/** The network server's view of a provisioned device. Keys are never included. */
export interface ProvisionedDevice {
  dev_eui: string
  join_eui: string
  name?: string
  enabled: boolean
  activation: 'otaa' | 'abp'
  dev_addr?: string
  group?: string
  disable_adr: boolean
  supports_class_c: boolean
  max_data_rate: number
  min_data_rate: number
  max_tx_power: number
}

/**
 * Reads one device's settings.
 *
 * Needed before an edit rather than at save time so the form can show what is
 * actually stored. A form that assumed defaults would write those assumptions
 * over settings nobody reviewed.
 */
export const fetchDevice = (devEUI: string) =>
  request<ProvisionedDevice>('GET', `/devices/${devEUI}`, undefined, {
    404: 'This device is not provisioned on the network.',
  })

/** The form values an edit panel holds. */
export interface DeviceForm {
  name: string
  group: string
  enabled: boolean
  disableAdr: boolean
  supportsClassC: boolean
  /** Kept as text because a select or an empty field is not yet a number. */
  maxDataRate: string
}

/**
 * The difference between what the form shows and what is stored.
 *
 * This is the whole safety property of the edit form in one function. The network
 * server treats an omitted field as "leave alone", so a panel that posted every
 * field it was showing would turn an unrendered checkbox into an instruction —
 * renaming a device would silently re-enable ADR on it. Comparing against the
 * loaded record rather than the form's own defaults is what prevents that.
 *
 * Returns an empty object when nothing changed, which the caller uses to disable
 * the save button rather than issue a request that would be refused.
 */
export function diffDeviceEdit(stored: ProvisionedDevice, form: DeviceForm): DeviceEdit {
  const edit: DeviceEdit = {}
  if (form.name.trim() !== (stored.name ?? '')) edit.name = form.name.trim()
  if (form.group.trim() !== (stored.group ?? '')) edit.group = form.group.trim()
  if (form.enabled !== stored.enabled) edit.enabled = form.enabled
  if (form.disableAdr !== stored.disable_adr) edit.disableAdr = form.disableAdr
  if (form.supportsClassC !== stored.supports_class_c) edit.supportsClassC = form.supportsClassC
  // Number('') is 0 and Number(' ') is 0 as well, which would silently set a
  // limit of DR0 — the tightest one there is — whenever the select has no value.
  // Only a non-empty, genuinely numeric entry counts as a number.
  const raw = form.maxDataRate.trim()
  const parsed = raw === '' ? Number.NaN : Number(raw)
  if (Number.isInteger(parsed) && parsed !== stored.max_data_rate) edit.maxDataRate = parsed
  return edit
}

/**
 * What an edit will change, in the operator's terms.
 *
 * Naming the fields rather than saying "saved" matters because two of them change
 * radio behaviour: an operator who toggled ADR should see that reflected rather
 * than a generic confirmation that leaves them checking the panel.
 */
export function describeDeviceEdit(edit: DeviceEdit): string {
  const parts: string[] = []
  if (edit.name !== undefined) parts.push('name')
  if (edit.group !== undefined) parts.push('group')
  if (edit.enabled !== undefined) parts.push(edit.enabled ? 'allowed on the air' : 'paused')
  if (edit.disableAdr !== undefined) parts.push(edit.disableAdr ? 'ADR off' : 'ADR on')
  if (edit.supportsClassC !== undefined) parts.push(edit.supportsClassC ? 'class C' : 'class A')
  if (edit.maxDataRate !== undefined) {
    parts.push(edit.maxDataRate === -1 ? 'data-rate limit lifted' : `limited to DR${edit.maxDataRate}`)
  }
  if (parts.length === 0) return 'Saved.'
  const radio = parts.some((p) => p.includes('ADR') || p.includes('DR') || p.includes('class'))
  return `Saved: ${parts.join(', ')}.${radio ? ' Takes effect on the device’s next uplink.' : ''}`
}

/**
 * Changes a device's settings.
 *
 * Only the fields the operator actually changed are sent, for the reason above.
 * An empty edit is refused locally rather than round-tripped for a 200 that
 * changed nothing.
 */
export async function editDevice(devEUI: string, edit: DeviceEdit): Promise<ProvisionedDevice> {
  const body: Record<string, unknown> = {}
  if (edit.name !== undefined) body.name = edit.name
  if (edit.group !== undefined) body.group = edit.group
  if (edit.enabled !== undefined) body.enabled = edit.enabled
  if (edit.disableAdr !== undefined) body.disable_adr = edit.disableAdr
  if (edit.supportsClassC !== undefined) body.supports_class_c = edit.supportsClassC
  if (edit.maxDataRate !== undefined) body.max_data_rate = edit.maxDataRate
  if (edit.minDataRate !== undefined) body.min_data_rate = edit.minDataRate
  if (edit.maxTxPower !== undefined) body.max_tx_power = edit.maxTxPower
  if (Object.keys(body).length === 0) {
    throw new Error('Nothing to save — no setting was changed.')
  }
  return request<ProvisionedDevice>('PATCH', `/devices/${devEUI}`, body, {
    404: 'This device is no longer provisioned on the network. It may have been removed.',
  })
}

/**
 * Revokes a device.
 *
 * The network server removes the session with the device, so this stops it
 * talking rather than merely hiding it. That is worth saying in the UI: an
 * operator who means "pause this device" wants the enabled toggle, and someone
 * who means "this sensor is gone" wants this one.
 */
export const deleteDevice = (devEUI: string) =>
  request<Record<string, never>>('DELETE', `/devices/${devEUI}`, undefined, {
    404: 'This device is not provisioned on the network.',
  })

/**
 * Battery as an operator reads it.
 *
 * The LoRaWAN field is 0..255 with 255 reserved for "cannot measure", which is
 * not a full charge, and an absent value means the device has never been asked.
 * Collapsing those three into one number would report a device that cannot
 * measure its cell as fully charged.
 */
export function batteryPercent(battery: number | undefined): string {
  if (battery === undefined) return 'Not reported'
  if (battery === 255) return 'Not measurable'
  if (battery === 0) return 'Exhausted'
  return `${Math.round((battery / 255) * 100)}%`
}

/** Renders a Go duration string ("1.5s", "250ms") as something compact. */
export function formatDuration(value: string): string {
  const match = /^(\d+(?:\.\d+)?)(ns|µs|us|ms|s|m|h)$/.exec(value)
  if (!match) return value
  const amount = Number(match[1])
  switch (match[2]) {
    case 'ms':
      return `${amount < 10 ? amount.toFixed(1) : Math.round(amount)} ms`
    case 's':
      return `${amount < 10 ? amount.toFixed(2) : amount.toFixed(1)} s`
    case 'm':
      return `${amount.toFixed(1)} min`
    case 'h':
      return `${amount.toFixed(1)} h`
    default:
      return value
  }
}

/**
 * Whether a hex string is something the network will accept.
 *
 * Checked here so the Send button explains itself instead of firing a request
 * that comes back with a code the operator has to decode. The server enforces
 * the same rules; this only moves the feedback earlier.
 */
export function validatePayload(hex: string, maxBytes = 51): string {
  const cleaned = hex.replace(/\s+/g, '')
  if (cleaned === '') return 'Enter a payload to send.'
  if (!/^[0-9a-fA-F]+$/.test(cleaned)) return 'Use hexadecimal digits only (0-9, a-f).'
  if (cleaned.length % 2 !== 0) return 'Hex needs an even number of digits.'
  if (cleaned.length / 2 > maxBytes) return `At most ${maxBytes} bytes per downlink.`
  return ''
}

