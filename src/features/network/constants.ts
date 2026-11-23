import type { DeviceFilter, View } from './types'

/** Values the interface repeats, gathered so they can be changed once. */

/**
 * How often the dashboard re-reads the API. A facility console shows "last seen"
 * values, so it has to move on its own rather than wait for a click; the interval
 * trades a live-feeling page against load on a server that may be sharing a small
 * machine with the network server.
 */
export const REFRESH_INTERVAL_MS = 15_000

/**
 * Group a device lands in when the operator leaves the field empty. It is a
 * label rather than a sentinel: the device list shows it verbatim, so changing
 * it changes what existing operators see, not how their devices behave.
 */
export const UNASSIGNED_GROUP = 'Unassigned'

/**
 * Identifier lengths in hexadecimal digits, as printed on a device label:
 * 64-bit DevEUI and JoinEUI, 128-bit AppKey.
 */
export const EUI_HEX_DIGITS = 16
export const APP_KEY_HEX_DIGITS = 32

/**
 * Bands offered when the network server cannot be asked.
 *
 * The console normally reads the authoritative list from `GET /api/v1/regions`,
 * which relays what the network server implements. This exists for the case where
 * that read fails — a network server still starting, for instance — so the
 * settings form offers bands instead of an empty dropdown.
 *
 * Keeping a copy is a deliberate trade: it can drift from the server's list, and
 * the drift is harmless in the direction that matters, because the API validates
 * against the server's own list and rejects anything it does not implement.
 */
export const FALLBACK_REGIONS = ['US915', 'EU868', 'AS923', 'AS923-2', 'AS923-3', 'AS923-4'] as const

/**
 * Device-status filters, each with the value it filters on and the label shown.
 *
 * The value `all` is deliberately not a display string. It used to be
 * `'All devices'`, which meant the filter's internal state was a piece of UI
 * copy: renaming the label broke filtering, and comparing a device's state to
 * the filter read like a type error even when it was correct.
 */
export const DEVICE_FILTERS: { value: DeviceFilter; label: string }[] = [
  { value: 'all', label: 'All devices' },
  { value: 'Online', label: 'Online' },
  { value: 'Needs attention', label: 'Needs attention' },
  { value: 'Offline', label: 'Offline' },
]

/** Signal-quality bands for the gateway bars, as SNR thresholds in dB. */
export const SIGNAL_QUALITY = {
  excellentAbove: -7,
  goodAbove: -12,
} as const

/** Battery percentage below which a device is flagged for replacement. */
export const LOW_BATTERY_PERCENT = 20

/**
 * How each gateway transport the network server reports is written in the
 * interface. The server's own value is the wire name it also uses in its logs,
 * so it is kept in the payload and translated only here.
 *
 * An unknown transport is shown verbatim rather than as "unknown": a build that
 * adds a third gateway protocol should read as that protocol's name, not as a
 * gap in the console's vocabulary.
 */
const TRANSPORT_LABELS: Record<string, string> = {
  'semtech-udp': 'UDP forwarder',
  'basic-station': 'Basics Station',
}

export function transportLabel(transport: string): string {
  if (transport === '') return 'transport unknown'
  return TRANSPORT_LABELS[transport] ?? transport
}

/**
 * The address a gateway should be pointed at, for one transport.
 *
 * Lives here rather than beside the panel that renders it because a component
 * file may export only components — mixing in a function costs Fast Refresh for
 * the whole module. It is also where the string is defined once: the card and
 * the clipboard button must show and copy the identical address, and a copy that
 * silently differs from the display is worse than no copy button, because it
 * looks like it worked.
 *
 * Basics Station is a WebSocket endpoint and needs its scheme and path; the
 * Semtech forwarder takes a bare host and port for its `server_path`.
 */
export function endpointFor(transport: string, host: string, port?: string | number): string {
  const suffix = port && Number(port) > 0 ? `:${port}` : ''
  return transport === 'basic-station'
    ? `wss://${host}${suffix}/updates`
    : `${host}${suffix}`
}

/**
 * Where the console's own API lives. Every route in the app is derived from this
 * one constant — the snapshot reads in api/ and the control-plane calls in
 * control/ — because nginx maps the prefix, the API serves it, and a change here
 * has to be matched in both of those.
 */
export const API_PREFIX = '/api/v1'

/**
 * Which views offer device registration as their primary action.
 *
 * This used to be an exclusion list — `view !== 'Settings' && view !== 'Models'`
 * — which meant every view inherited the button unless someone remembered to
 * exclude it. The Gateways page consequently carried a primary "Add device"
 * action while its own first panel explains that gateways are not registered in
 * this console at all, and the Events page offered it for no reason. An
 * exclusion list fails silently every time a view is added; naming the views
 * that have the action fails visibly.
 *
 * The Overview is excluded as well. Its device panel is a five-row preview with
 * a "View all" route, so provisioning from there meant opening a form for a
 * device you could not then see in context — the full inventory, the filters and
 * the session console all live on the Devices page, which is where the action
 * belongs.
 */
export function viewOffersDeviceRegistration(view: View): boolean {
  return view === 'Devices'
}

/**
 * The default port the MCP listener is published on.
 *
 * Mirrors `MCP_HTTP_PORT` in compose. Repeated here because the console cannot read
 * the deployment's environment, and the page is generating a URL for an operator to
 * paste into another machine's config — a blank field they have to look up is a
 * field they will get wrong.
 */
export const MCP_DEFAULT_HTTP_PORT = '8092'

/**
 * Where an agent points at the MCP listener.
 *
 * Built from the host the *browser* used, not from anything the API reports, and
 * that distinction is why this sits beside an editable field rather than being read
 * from the server. The console API knows the listener's container address
 * (`http://mcp:8092`); an agent on an operator's laptop cannot resolve that. Only
 * the browser knows a hostname its own machine can reach — and even that is a guess
 * to confirm, since the listener may be reached through a published port, a reverse
 * proxy, or a different host entirely.
 */
export function agentEndpointFor(host: string, port: string): string {
  const cleanHost = host.trim()
  const cleanPort = port.trim()
  const suffix = cleanPort && cleanPort !== '80' && cleanPort !== '443' ? `:${cleanPort}` : ''
  return `http://${cleanHost || 'HOST'}${suffix}${MCP_ENDPOINT_PATH}`
}

/**
 * Paths on the MCP listener. Named so the page can state exactly what it checked
 * rather than saying "the health endpoint" and leaving the operator to find it.
 */
export const MCP_ENDPOINT_PATH = '/mcp'
export const MCP_HEALTH_PATH = '/healthz'

/**
 * Placeholders in a generated config.
 *
 * Deliberately placeholders, never real values. The console does not hold either
 * token and must not: the MCP token is separate from the network server's precisely
 * so that leaking one does not open both, and a console that fetched a secret to be
 * helpful would dissolve that split while looking like it was saving a step. A
 * placeholder that is obviously a placeholder gets filled in; a field that looks
 * finished does not get checked.
 */
export const MCP_TOKEN_PLACEHOLDER = 'PASTE_MCP_AUTH_TOKEN_FROM_ENV'
export const NS_TOKEN_PLACEHOLDER = 'PASTE_NS_API_TOKEN_FROM_ENV'

/**
 * A config for an MCP host that spawns the server as a child process.
 *
 * The common case, and the one with no network exposure: whoever can start the
 * process can use the token, so there is no second access control to get wrong.
 */
export function stdioConfigSnippet(command: string, apiURL: string, statusURL: string): string {
  return JSON.stringify({
    mcpServers: {
      fieldnode: {
        command,
        env: {
          NS_API_URL: apiURL,
          NS_STATUS_URL: statusURL,
          NS_API_TOKEN: NS_TOKEN_PLACEHOLDER,
        },
      },
    },
  }, null, 2)
}

/**
 * A config for an MCP host that connects over the network.
 *
 * Two lines rather than a JSON blob, because HTTP-capable hosts differ widely in
 * shape and there is no standard client config the way there is for a spawned
 * command. Inventing one that looked authoritative would be worse than stating the
 * two facts that are fixed: the URL, and that the scheme is Bearer.
 */
export function httpAccessSnippet(endpoint: string): string {
  return [
    `endpoint: ${endpoint}`,
    `Authorization: Bearer ${MCP_TOKEN_PLACEHOLDER}`,
  ].join('\n')
}

/**
 * What an agent can do through the MCP listener, as the console words it.
 *
 * This describes **the software**, not the deployment: it is the same for every
 * install of this build, which is why the page never presents it as live state.
 * Whether writes are actually switched on is a per-deployment setting the console
 * cannot see, and the page says so rather than implying it knows.
 *
 * The names are pinned against the Go server by a test in the mcp module that reads
 * this file as text and fails if the two lists disagree. Nothing else can: the
 * console and the MCP server share no code, so a renamed tool would otherwise leave
 * this table confidently describing something that no longer exists — on the one page
 * whose entire job is telling an operator what an agent is able to do.
 */
export type AgentTool = {
  name: string
  /** One line, in operator words rather than the tool's own description. */
  summary: string
  /** True when the tool changes something on the network. */
  writes: boolean
}

export const AGENT_TOOLS: AgentTool[] = [
  { name: 'network_status', summary: 'Band, uptime, every gateway and its counters, duty-cycle use', writes: false },
  { name: 'list_devices', summary: 'The whole inventory, as the network server sees it', writes: false },
  { name: 'get_device', summary: 'One device’s configuration', writes: false },
  { name: 'get_device_session', summary: 'One device’s radio state: counters, data rate, signal, battery', writes: false },
  { name: 'list_uplinks', summary: 'Recent uplinks with the formatter’s reading of them', writes: false },
  { name: 'list_events', summary: 'The trace, including every frame the network refused and why', writes: false },
  { name: 'list_models', summary: 'Device models and how their payloads are read', writes: false },
  { name: 'get_model', summary: 'One model, including its formatter script', writes: false },
  { name: 'list_integrations', summary: 'Where events are being delivered', writes: false },
  { name: 'get_integration', summary: 'One destination in full', writes: false },
  { name: 'explain_event_reason', summary: 'What a recorded reason means and what usually causes it', writes: false },
  { name: 'diagnose_device', summary: 'Device, session, events, uplinks and network state in one answer', writes: false },
  { name: 'enqueue_downlink', summary: 'Queue a frame for a device’s next RX window', writes: true },
  { name: 'cancel_downlink', summary: 'Discard the frame waiting in that slot', writes: true },
  { name: 'request_device_status', summary: 'Ask a device for battery and margin — it takes the same slot', writes: true },
  { name: 'patch_device', summary: 'Change one device’s configuration', writes: true },
]

/**
 * What no profile offers, whatever the deployment sets.
 *
 * Listed rather than omitted, because an operator who cannot find these in the table
 * will assume they are there under another name and go looking for them.
 */
export const AGENT_NEVER_ALLOWED: { what: string; why: string }[] = [
  {
    what: 'Create or delete a device',
    why: 'A provisioning request carries an AppKey. Routing it through a model puts '
      + 'that key into a conversation transcript, which breaks the rule that keys go '
      + 'in and never come out.',
  },
  {
    what: 'Add, change or delete an event destination',
    why: 'A destination the network delivers events to is an exfiltration channel and '
      + 'a way to make the server send requests to arbitrary addresses.',
  },
  {
    what: 'Install or edit a payload formatter',
    why: 'A formatter is JavaScript that runs inside the network server’s own process.',
  },
]
