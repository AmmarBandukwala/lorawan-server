/**
 * Recent uplink traffic, decoded.
 *
 * Read-only history for one device or the whole network, as the network server
 * stored it — the same rows the dashboard's counters are computed from.
 */

import type { UplinkRow } from './types'
import { request } from './http'

export const fetchUplinks = (devEUI?: string, limit = 20) => {
  const params = new URLSearchParams({ limit: String(limit) })
  if (devEUI) params.set('dev_eui', devEUI)
  return request<{ uplinks: UplinkRow[] }>('GET', `/uplinks?${params}`).then((r) => r.uplinks ?? [])
}

/**
 * A decoded reading as lines an operator can read.
 *
 * Values are rendered rather than JSON-stringified because the point of a
 * formatter is to turn bytes into engineering units: a nested object is flattened
 * to dotted keys so a reading inside a channel array is still one line.
 */
export function formatDecoded(decoded: Record<string, unknown> | undefined): { key: string; value: string }[] {
  if (!decoded) return []
  const out: { key: string; value: string }[] = []
  const walk = (value: unknown, prefix: string) => {
    if (value === null || value === undefined) {
      out.push({ key: prefix, value: '—' })
      return
    }
    if (Array.isArray(value)) {
      value.forEach((item, index) => walk(item, `${prefix}[${index}]`))
      return
    }
    if (typeof value === 'object') {
      for (const [key, child] of Object.entries(value)) {
        walk(child, prefix ? `${prefix}.${key}` : key)
      }
      return
    }
    out.push({ key: prefix, value: String(value) })
  }
  walk(decoded, '')
  return out
}
