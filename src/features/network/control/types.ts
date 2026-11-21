/**
 * The control plane's wire types.
 *
 * These mirror the network server's JSON field-for-field, in snake_case, on
 * purpose. Renaming fields to camelCase here would put a translation layer
 * between the operator and the thing that knows the answer, and a field the
 * console does not translate is a field that silently disappears from the UI
 * after a network-server upgrade.
 *
 * Types only — this module imports nothing and holds no logic, so every other
 * control-plane module can depend on it without a cycle.
 */


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
  /**
   * Payload-formatter health.
   *
   * Absent when this build has no decoder wired up, which is why every field
   * below is read defensively. The counters are process-lifetime and only cover
   * frames that actually arrived: a model nobody has heard from yet is not
   * counted, so this is a report about traffic, not an audit of the fleet.
   */
  decoding?: {
    enabled: boolean
    frames_decoded: number
    frames_failed: number
    compiled_formatters: number
    broken_formatters: number
    broken_models?: { model_id: string; reason: string }[]
  }
}

/**
 * What the payload-formatter panel should say.
 *
 * The distinction it draws is the one an operator cannot infer from the numbers
 * alone: a formatter that will not compile means every frame from every device on
 * that model is stored as raw bytes while the devices themselves look perfectly
 * healthy, whereas a frame the formatter refused is one bad frame on a formatter
 * that is otherwise working. The first is a fleet-wide configuration fault, the
 * second is normal noise, and rendering them identically would send someone to
 * check the radios.
 *
 * Returns null when the server reports no decoder at all, so the panel renders
 * nothing instead of a wall of zeros that reads like a healthy fleet.
 */

export type RegionList = {
  regions: string[]
  source: 'network-server' | 'fallback'
  /** The band the facility profile currently records, which is not necessarily
   *  the one the network server is enforcing. */
  configured: string
}

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
  /**
   * Re-associates the device with a model. '' clears the association — the
   * device stops being decoded — and undefined leaves it alone, which is the
   * same distinction the location field makes.
   */
  modelId?: string
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
  /**
   * The device model this device is an instance of, and therefore the payload
   * formatter its uplinks are decoded with. Absent when nobody has said what
   * kind of device it is, which is the normal state for anything provisioned
   * before models existed; such a device still reports, its bytes simply arrive
   * as hex.
   */
  model_id?: string
}

/**
 * Reads one device's settings.
 *
 * Needed before an edit rather than at save time so the form can show what is
 * actually stored. A form that assumed defaults would write those assumptions
 * over settings nobody reviewed.
 */

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
  /**
   * The model slug, or '' for "no model". Text because a select can legitimately
   * hold the empty choice, and '' must mean "clear the association" rather than
   * "leave alone".
   */
  modelId: string
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

export interface DeviceModel {
  model_id: string
  name: string
  description?: string
  /** 'NONE' stores no formatter, so its uplinks stay hex. */
  codec_kind: 'NONE' | 'JS'
  /** Absent from list responses; present when a single model is read. */
  codec_script?: string
  /** True when a JS formatter is actually stored, without shipping it. */
  codec_present: boolean
  created_at?: string
  updated_at?: string
}

/** What creating or replacing a model sends. */
export interface ModelInput {
  name: string
  description?: string
  codecKind: 'NONE' | 'JS'
  codecScript?: string
}

/** What deleting a model reports, including the consequence the UI must state. */
export type ModelDeletion = {
  deleted: string
  devices_unassociated: number
  note?: string
}

export type UplinkRow = {
  uplink_id: number
  received_at: string
  dev_eui: string
  dev_addr?: string
  f_cnt_up: number
  /** -1 marks a frame that carried only MAC commands and no application port. */
  f_port: number
  /** Decrypted frame payload, base64. */
  data?: string
  /** The whole frame as it came off the radio, hex. */
  raw_payload?: string
  gateway_id?: string
  frequency_hz?: number
  data_rate_index: number
  rssi: number
  snr: number
  retransmission?: boolean
  decoded?: Record<string, unknown>
  decode_error?: string
  /**
   * Whether a formatter ran at all. Without it, "this device has no model" and
   * "the formatter rejected this frame" look identical when both optional fields
   * above are absent.
   */
  has_codec: boolean
}

/**
 * Integration destinations: where a decoded event is pushed as it arrives.
 *
 * There is deliberately no credential field in any of these types. The network
 * server accepts a secret on write and never returns one, so the console cannot
 * display it and a field for it would only ever hold something the operator just
 * typed. `has_secret` is the honest substitute: it says a credential exists
 * without being one.
 */
export type IntegrationKind = 'webhook' | 'mqtt'

export type WebhookSettings = {
  url: string
}

export type MQTTSettings = {
  broker_url: string
  topic?: string
  client_id?: string
  username?: string
  qos?: number
  retain?: boolean
  /** Encrypted but unauthenticated. The panel has to say so out loud. */
  insecure_skip_verify?: boolean
}

export type Integration = {
  id: string
  kind: IntegrationKind
  name: string
  enabled: boolean
  webhook: WebhookSettings
  mqtt: MQTTSettings
  has_secret: boolean

  /**
   * Whether the running process currently has a live connection to this
   * destination. It is what separates "I saved it" from "it is delivering", and
   * only an MQTT destination can truly answer: a webhook has no persistent
   * connection, so false there means "not applicable", not "broken".
   */
  connected: boolean
  delivery_error?: string

  /**
   * True for a destination configured through the network server's environment,
   * which has no row behind it. Shown so the page accounts for everything that
   * is delivering, and read-only because there is nothing here to edit.
   */
  managed_by_environment: boolean
}

/** What the operator fills in. `secret` absent means "leave it as it is". */
export type IntegrationInput = {
  id: string
  kind: IntegrationKind
  name: string
  enabled: boolean
  webhook: WebhookSettings
  mqtt: MQTTSettings
  /**
   * undefined: do not touch the stored credential. '': remove it. Anything else:
   * set it. A plain string cannot tell those three apart, which is why this is
   * optional rather than defaulted to empty.
   */
  secret?: string
}

/** The verdict of the "test" button. */
export type IntegrationProbe = {
  ok: boolean
  detail: string
}
