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
 * Regions this console offers. It has to match what the API accepts and what the
 * network server was built with; the API is the authority and rejects anything
 * else, so this list only decides what can be picked without an error.
 */
export const SUPPORTED_REGIONS = ['EU868', 'US915', 'AS923'] as const

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
