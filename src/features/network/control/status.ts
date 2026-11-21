/**
 * The network server's own health report, and the reading of it the console shows.
 *
 * This is process state that no table holds: which gateways are still sending
 * PULL_DATA, how much duty-cycle time is used, how many webhook deliveries were
 * dropped. It is deliberately outside the snapshot in api/snapshot.ts, because a
 * network server that is not answering must not degrade the panels that did load.
 */

import type { NetworkStatus, RegionList } from './types'
import { request } from './http'

export function decodeHealth(decoding: NetworkStatus['decoding']): {
  enabled: boolean
  framesDecoded: number
  framesFailed: number
  compiled: number
  broken: { model_id: string; reason: string }[]
  problem: string
} | null {
  if (!decoding) return null
  const broken = decoding.broken_models ?? []

  let problem = ''
  if (broken.length > 0) {
    // Naming the models is the point: "2 broken formatters" is a number to walk
    // past, "aqi-pro: unexpected token" is a task.
    const names = broken.map((model) => model.model_id).join(', ')
    const noun = broken.length === 1 ? 'model has' : 'models have'
    problem = `${broken.length} device ${noun} a payload formatter that will not run (${names}). `
      + 'Every frame from those devices is stored undecoded, and the devices themselves look healthy.'
    if (broken[0].reason) problem += ` First reported: ${broken[0].reason}`
  } else if (decoding.frames_failed > 0) {
    const verb = decoding.frames_failed === 1 ? 'frame was' : 'frames were'
    problem = `${decoding.frames_failed.toLocaleString()} ${verb} rejected by its formatter since the server started. `
      + 'The formatters compile, so these are frames the scripts refused.'
  }

  return {
    enabled: decoding.enabled,
    framesDecoded: decoding.frames_decoded,
    framesFailed: decoding.frames_failed,
    compiled: decoding.compiled_formatters,
    broken,
    problem,
  }
}

/**
 * A process lifetime as an operator reads it: coarse, and only as precise as the
 * unit that matters. Nobody wants "3720 seconds"; they want to know whether this
 * is the process that has been up all week or one that restarted a minute ago.
 */
export function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return 'Unknown'
  const total = Math.floor(seconds)
  const days = Math.floor(total / 86400)
  const hours = Math.floor((total % 86400) / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const secs = total % 60

  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m ${secs}s`
  return `${secs}s`
}

/**
 * Whether an uptime this short means the server has been restarting.
 *
 * A console that shows "up 12s" next to a device list that has not changed is
 * the evidence for "the container is crash-looping", which is otherwise one of
 * the hardest things to notice from a browser.
 */
export function recentlyRestarted(seconds: number): boolean {
  return Number.isFinite(seconds) && seconds >= 0 && seconds < 120
}

/** How many gateways there are, and how many are answering right now. */
export type GatewaySummary = {
  online: number
  total: number
  /**
   * Which population the numbers describe. `network-server` means the live
   * presence list was used and the inventory only added rows nobody has heard
   * from; `inventory` means the network server could not be reached, so nothing
   * here says whether a single radio is actually working.
   */
  source: 'network-server' | 'inventory'
}

/**
 * Reconciles the two places gateway counts come from.
 *
 * The database's inventory and the network server's presence list are different
 * populations, and showing one of them as "the number of gateways" is what makes
 * the Overview contradict the Gateways view: a gateway someone typed into the
 * inventory but never powered on is counted by one and not the other, and a
 * gateway that arrived unannounced is counted by the reverse. The honest figure
 * is the union, and the honest label for "online" is whatever the process that
 * receives the packets says.
 *
 * When the server is unreachable the inventory is still worth showing, but it is
 * labelled as such — "3 registered" is not a claim about radio health, and
 * presenting it as one is how an outage gets read as a configuration problem.
 */
export function gatewaySummary(status: NetworkStatus | null, inventoryIds: string[]): GatewaySummary {
  if (!status) {
    return { online: 0, total: inventoryIds.length, source: 'inventory' }
  }
  const heard = new Set(status.gateways.map((gateway) => gateway.gateway_eui.toUpperCase()))
  const unheard = inventoryIds.filter((id) => !heard.has(id.toUpperCase())).length
  return {
    online: status.gateways_online,
    total: status.gateways_total + unheard,
    source: 'network-server',
  }
}

export const fetchStatus = () => request<NetworkStatus>('GET', '/status')

/**
 * The bands this deployment can be configured for.
 *
 * `source` is part of the contract rather than decoration: "network-server"
 * means the list is what the running server enforces, and "fallback" means the
 * server could not be asked and the console is showing its own guess. An
 * operator choosing a regulatory band deserves to know which of those happened.
 */

export const fetchRegions = () => request<RegionList>('GET', '/regions')

/**
 * Queues a downlink.
 *
 * Resolving means queued, not transmitted: a class-A device listens for two
 * seconds after it speaks and then goes quiet, so the frame leaves in whatever
 * window it opens next. The UI must not claim otherwise.
 */

export function batteryPercent(battery: number | undefined): string {
  if (battery === undefined) return 'Not reported'
  if (battery === 255) return 'Not measurable'
  if (battery === 0) return 'Exhausted'
  return `${Math.round((battery / 255) * 100)}%`
}

/**
 * Whether a hex string is something the network will accept.
 *
 * Checked here so the Send button explains itself instead of firing a request
 * that comes back with a code the operator has to decode. The server enforces
 * the same rules; this only moves the feedback earlier.
 */
