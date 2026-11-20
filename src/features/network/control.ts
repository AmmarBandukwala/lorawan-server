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
  /**
   * Which gateway protocol this gateway is connected over — "semtech-udp" or
   * "basic-station". It is the first thing to check when a gateway behaves
   * differently than expected, because the two transports differ in timing
   * precision and in what they report.
   */
  transport: string
  /**
   * The transport's own version string: the packet-forwarder protocol revision
   * for UDP gateways, the station firmware for Basics Station. The network
   * server sends whatever the gateway told it, so this is a string rather than
   * a number — an earlier version of this type declared it numeric and the
   * console rendered "Protocol vundefined" for every gateway.
   */
  protocol: string
  address: string
  firmware?: string
  model?: string
  online: boolean
  last_seen: string
  packets: number
  downlinks_sent: number
  downlink_errors: number
  /**
   * Transmissions the gateway confirmed it actually radiated, and the
   * difference from downlinks_sent is what "scheduled but never emitted" looks
   * like. A dnmsg can leave the server and still never reach the air; only the
   * confirmation says otherwise.
   */
  downlinks_confirmed: number
  downlinks_unconfirmed: number
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

/**
 * One gateway-facing endpoint the network server has open.
 *
 * The console cannot invent these: NS_LBS_ADDR=off removes a transport entirely,
 * so which endpoints exist is a fact about the running process and not about the
 * build of the console.
 */
export type GatewayListener = {
  /** "semtech-udp" or "basic-station", matching GatewayHealth.transport. */
  transport: string
  /** As configured, for example ":6858". */
  bind_addr: string
  port: number
  /**
   * True when the server bound every interface, which is the usual case. The
   * gateway still needs a host the operator knows is reachable, so the console
   * supplies the one it was loaded from and says that is what it did.
   */
  host_wildcard: boolean
  /** Whether the endpoint authenticates the gateway that connects to it. */
  secured: boolean
}

export type NetworkStatus = {
  uptime_seconds: number
  region: string
  net_id: string
  downlink_power_dbm: number
  gateways_total: number
  gateways_online: number
  gateways: GatewayHealth[]
  /**
   * Every band this build implements. The console should offer these rather than
   * its own list, which is how the settings form and the server came to disagree
   * about which regions exist.
   */
  supported_regions?: string[]
  /**
   * The gateway protocols this process is listening for. A station that will not
   * connect while UDP gateways are fine is explained here: if "basic-station" is
   * absent, the port is not open, which is a configuration answer rather than a
   * gateway fault.
   */
  transports?: string[]
  /**
   * Where each open transport is listening, so the console can tell an operator
   * what to type into a gateway. `transports` says a protocol is reachable; this
   * says on which port, which is the half that actually unblocks someone
   * standing in front of the gateway.
   *
   * Optional because a network server older than this field reports the
   * transports only; the connect panel then shows the protocol names without
   * addresses rather than nothing at all.
   */
  listeners?: GatewayListener[]
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
 * The bands this deployment can be configured for.
 *
 * `source` is part of the contract rather than decoration: "network-server"
 * means the list is what the running server enforces, and "fallback" means the
 * server could not be asked and the console is showing its own guess. An
 * operator choosing a regulatory band deserves to know which of those happened.
 */
export type RegionList = {
  regions: string[]
  source: 'network-server' | 'fallback'
  /** The band the facility profile currently records, which is not necessarily
   *  the one the network server is enforcing. */
  configured: string
}

export const fetchRegions = () => request<RegionList>('GET', '/regions')

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
  /**
   * Replaces the whole attribute map. An empty object is a deliberate clearing,
   * which is why this cannot be expressed as "omit the field" — that already
   * means "leave alone".
   */
  attributes?: Record<string, string>
  /** An explicit null clears the stored position; undefined leaves it alone. */
  location?: DeviceLocation | null
}

/** Where a device is installed. Altitude is metres above mean sea level. */
export interface DeviceLocation {
  latitude: number
  longitude: number
  altitude?: number
}

/** One attribute as an editor row. Order is the operator's, not the server's. */
export interface AttributeRow {
  key: string
  value: string
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
  /** Absent when the device carries none, rather than an empty object. */
  attributes?: Record<string, string>
  /** Absent when no position has been recorded — distinct from 0,0. */
  location?: DeviceLocation
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
  /**
   * Editor rows rather than a map, because a map has no order and a form needs
   * one: rows are added, edited and removed by position, and re-rendering a
   * device's tags in a different order on every keystroke reads as a bug.
   */
  attributes: AttributeRow[]
  /** Text for the same reason as maxDataRate: blank must not become zero. */
  latitude: string
  longitude: string
  altitude: string
}

/**
 * Attribute rows as the map that gets stored, or a message when they cannot be
 * one.
 *
 * Keys are lower-cased and trimmed here, matching the network server, so what the
 * operator sees in the form is what comes back after a save. Leaving the
 * normalisation to the server would make a saved device appear to have acquired
 * different tags than the ones that were typed.
 *
 * Two rows that normalise to the same key are refused rather than collapsed:
 * "Line" and "line " describing different things is a question only the operator
 * can answer.
 */
export function attributeMap(rows: AttributeRow[]): { map?: Record<string, string>; problem?: string } {
  const map: Record<string, string> = {}
  for (const row of rows) {
    const key = row.key.trim().toLowerCase()
    const value = row.value.trim()
    // A completely blank row is someone who pressed "Add" and moved on, not a
    // tag they meant to store.
    if (key === '' && value === '') continue
    if (key === '') return { problem: 'An attribute needs a key, or remove the empty row.' }
    if (!/^[a-z0-9_-]+$/.test(key)) {
      return { problem: `“${key}” can only use letters, numbers, dash and underscore.` }
    }
    if (value === '') return { problem: `“${key}” has no value. Fill it in or remove the row.` }
    if (key in map) return { problem: `“${key}” is listed twice.` }
    map[key] = value
  }
  return { map }
}

/** Stored attributes as editor rows, in a stable order. */
export function attributeRows(map: Record<string, string> | undefined): AttributeRow[] {
  if (!map) return []
  return Object.keys(map).sort().map((key) => ({ key, value: map[key] }))
}

/**
 * Whether two attribute maps hold the same pairs.
 *
 * Object comparison would always report a change, because building the map from
 * the form produces a new object every render.
 */
export function sameAttributes(
  a: Record<string, string> | undefined,
  b: Record<string, string>,
): boolean {
  const existing = a ?? {}
  const existingKeys = Object.keys(existing)
  if (existingKeys.length !== Object.keys(b).length) return false
  return existingKeys.every((key) => existing[key] === b[key])
}

/** A position as form text, for loading a stored device into the editor. */
export function locationFields(location: DeviceLocation | undefined): {
  latitude: string
  longitude: string
  altitude: string
} {
  if (!location) return { latitude: '', longitude: '', altitude: '' }
  return {
    latitude: String(location.latitude),
    longitude: String(location.longitude),
    // An absent altitude must not render as "0": that is a real height, and
    // showing it would invite the operator to save a height nobody measured.
    altitude: location.altitude === undefined ? '' : String(location.altitude),
  }
}

/** Whether a position matches what is stored, altitude included. */
export function sameLocation(a: DeviceLocation, b: DeviceLocation): boolean {
  return a.latitude === b.latitude && a.longitude === b.longitude &&
    (a.altitude ?? null) === (b.altitude ?? null)
}

/**
 * The position the form is asking for, or undefined for "no change".
 *
 * Three cases have to be told apart, and the empty-field case is the one that
 * would otherwise go wrong: clearing all three boxes on a device that has a
 * position means "remove it", which is an explicit null rather than silence.
 * A half-typed position returns undefined so it cannot be sent — the caller
 * shows locationProblem instead.
 */
export function positionFromForm(
  form: DeviceForm,
  stored: DeviceLocation | undefined,
): DeviceLocation | null | undefined {
  const lat = form.latitude.trim()
  const long = form.longitude.trim()
  const alt = form.altitude.trim()

  if (lat === '' && long === '' && alt === '') {
    return stored ? null : undefined
  }
  if (lat === '' || long === '') return undefined

  const latitude = Number(lat)
  const longitude = Number(long)
  if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) return undefined

  const next: DeviceLocation = { latitude, longitude }
  if (alt !== '') {
    const altitude = Number(alt)
    if (!Number.isFinite(altitude)) return undefined
    next.altitude = altitude
  }
  if (stored && sameLocation(stored, next)) return undefined
  return next
}

/**
 * What is wrong with the position fields, in operator terms, or '' when they are
 * usable.
 *
 * Ranges are checked here because a latitude of 91 does not mean anything, and
 * finding that out after a round trip costs the operator the context of what they
 * were typing.
 */
export function locationProblem(form: DeviceForm): string {
  const lat = form.latitude.trim()
  const long = form.longitude.trim()
  const alt = form.altitude.trim()
  if (lat === '' && long === '' && alt === '') return ''
  if (lat === '' || long === '') return 'A position needs both a latitude and a longitude. Clear all three to remove it.'
  const latitude = Number(lat)
  const longitude = Number(long)
  if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) return 'Latitude and longitude must be numbers.'
  if (latitude < -90 || latitude > 90) return 'Latitude must be between -90 and 90.'
  if (longitude < -180 || longitude > 180) return 'Longitude must be between -180 and 180.'
  if (alt !== '') {
    const altitude = Number(alt)
    if (!Number.isFinite(altitude)) return 'Altitude must be a number of metres.'
    if (altitude < -1000 || altitude > 10000) return 'Altitude must be between -1000 and 10000 metres.'
  }
  return ''
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

  const { map, problem } = attributeMap(form.attributes)
  // An unusable row set produces no edit at all rather than a partial one: the
  // caller shows the problem and keeps the save button disabled.
  if (!problem && map && !sameAttributes(stored.attributes, map)) edit.attributes = map

  const position = positionFromForm(form, stored.location)
  if (position !== undefined) edit.location = position
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
  if (edit.attributes !== undefined) {
    const count = Object.keys(edit.attributes).length
    parts.push(count === 0 ? 'attributes cleared' : `${count} attribute${count === 1 ? '' : 's'}`)
  }
  if (edit.location !== undefined) {
    parts.push(edit.location === null
      ? 'position removed'
      : `position ${edit.location.latitude.toFixed(5)}, ${edit.location.longitude.toFixed(5)}`)
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
  // Both are sent as-is, including an empty object and an explicit null: those
  // are the clearing instructions, and dropping them would make a device's tags
  // and position impossible to remove from the console.
  if (edit.attributes !== undefined) body.attributes = edit.attributes
  if (edit.location !== undefined) body.location = edit.location
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

