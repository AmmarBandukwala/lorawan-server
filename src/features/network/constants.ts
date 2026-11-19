import type { DeviceFilter } from './types'

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
 * The band a new facility starts on, matching the network server's default.
 *
 * It is a separate value from the list's first entry so that reordering the
 * dropdown for readability cannot silently change what a fresh installation
 * enforces.
 */
export const DEFAULT_REGION: string = 'US915'

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
