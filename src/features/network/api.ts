import type { Device, FacilitySettings, GatewayRecord, NetworkEvent, Overview } from './types'
import { emptyOverview, emptySettings } from './types'

export type SectionKey = 'devices' | 'overview' | 'gateways' | 'events' | 'settings'

export type NetworkSnapshot = {
  devices: Device[]
  overview: Overview
  gateways: GatewayRecord[]
  events: NetworkEvent[]
  settings: FacilitySettings
  /**
   * Sections whose request failed during this refresh. Their values above are
   * the previously known ones, not zeros, so a single failing endpoint degrades
   * one panel instead of blanking the whole dashboard.
   */
  failed: SectionKey[]
}

export const emptySnapshot: NetworkSnapshot = {
  devices: [],
  overview: emptyOverview,
  gateways: [],
  events: [],
  settings: emptySettings,
  failed: [],
}

const endpoints: Record<SectionKey, string> = {
  devices: '/api/v1/devices',
  overview: '/api/v1/overview',
  gateways: '/api/v1/gateways',
  events: '/api/v1/events',
  settings: '/api/v1/settings',
}

/** Every section the dashboard reads, so callers can tell "all failed" from "some failed". */
export const sectionKeys = Object.keys(endpoints) as SectionKey[]

/**
 * Structural equality for facility settings. Used to decide whether a background
 * refresh may overwrite the settings draft: comparing object identity would
 * always say "changed", because every fetch decodes a new object.
 */
export function sameSettings(a: FacilitySettings, b: FacilitySettings): boolean {
  return a.configured === b.configured &&
    a.facilityName === b.facilityName &&
    a.region === b.region &&
    a.operatorName === b.operatorName &&
    a.operatorRole === b.operatorRole
}

/**
 * Fetches every section independently.
 *
 * The previous version fetched with `Promise.all` and threw if any single
 * response was not ok, which made the least reliable endpoint the owner of the
 * whole page: one broken route reset every panel to empty and reported the
 * entire network as offline. Each section is now fetched and decoded on its own
 * so a failure costs that section alone, and the caller keeps showing the last
 * good data for it.
 *
 * `previous` supplies the values to keep for sections that fail. A refresh that
 * fails completely still reports which sections failed rather than throwing, so
 * the caller can distinguish "nothing reachable" from "partially degraded".
 */
export async function loadNetworkSnapshot(
  previous: NetworkSnapshot = emptySnapshot,
  signal?: AbortSignal,
): Promise<NetworkSnapshot> {
  const keys = Object.keys(endpoints) as SectionKey[]
  const results = await Promise.all(
    keys.map((key) => fetchSection<Device[] | Overview | GatewayRecord[] | NetworkEvent[] | FacilitySettings>(key, signal)),
  )

  const next: NetworkSnapshot = { ...previous, failed: [] }
  keys.forEach((key, i) => {
    const result = results[i]
    if (result.ok) {
      // A section that legitimately returns nothing must not inherit stale
      // rows, so a success always replaces the value.
      // @ts-expect-error each key's value type matches its own endpoint's
      next[key] = result.value
    } else {
      next.failed.push(key)
    }
  })
  return next
}

type SectionResult<T> = { ok: true; value: T } | { ok: false; error: string }

async function fetchSection<T>(key: SectionKey, signal?: AbortSignal): Promise<SectionResult<T>> {
  try {
    const response = await fetch(endpoints[key], { signal })
    if (!response.ok) return { ok: false, error: `${key}: HTTP ${response.status}` }
    return { ok: true, value: (await response.json()) as T }
  } catch (error) {
    // An abort is a caller cancelling an outdated refresh, not a network
    // failure; rethrowing lets the caller's `signal.aborted` check ignore it.
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    return { ok: false, error: `${key}: ${error instanceof Error ? error.message : 'unreachable'}` }
  }
}

/** The fields a device registration collects. */
export type NewDevice = {
  name: string
  devEui: string
  /** Optional: the network server applies its configured default when empty. */
  joinEui: string
  /** The OTAA root key, printed on the device label. Sent once, never stored here. */
  appKey: string
  group: string
}

/**
 * Registers a device, which means provisioning it on the network.
 *
 * This used to write an inventory row that nothing read, so a "registered"
 * device could never join and the dashboard kept listing it as absent. The
 * request now carries the join credentials and goes to the network server, which
 * is what makes the device able to join.
 *
 * Errors are phrased as the action the operator can take, because the response
 * body's own wording describes our internals.
 */
export async function registerDevice(input: NewDevice): Promise<Device> {
  const response = await fetch('/api/v1/devices', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name: input.name,
      devEui: input.devEui,
      joinEui: input.joinEui || undefined,
      appKey: input.appKey,
      group: input.group,
    }),
  })
  if (response.status === 409) throw new Error('That DevEUI is already provisioned on this network.')
  if (response.status === 503) throw new Error('This console is not connected to a network server, so it cannot provision a device.')
  if (response.status === 504) throw new Error('The network server did not answer in time. Check that it is running before retrying.')
  if (response.status === 502) throw new Error('The network server could not be reached, so the device was not provisioned.')
  if (!response.ok) throw new Error('The device was not accepted. Check the DevEUI, JoinEUI and AppKey against the device label.')
  return response.json() as Promise<Device>
}


export async function saveFacilitySettings(settings: FacilitySettings): Promise<FacilitySettings> {
  const response = await fetch('/api/v1/settings', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  })
  if (!response.ok) throw new Error('Settings could not be saved. Check the values and try again.')
  return response.json() as Promise<FacilitySettings>
}
