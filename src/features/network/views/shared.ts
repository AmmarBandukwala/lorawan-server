import type { DeviceFilter, GroupFilter, View } from '../types'

/**
 * The device list's search and filter controls.
 *
 * The Overview and the Devices view carry the same control strip, and the state
 * is deliberately shared rather than local to each view: filtering from the
 * Overview and then opening Devices should not silently reset a query someone
 * had just typed.
 */
export type DeviceListControls = {
  query: string
  setQuery: (value: string) => void
  filter: DeviceFilter
  setFilter: (value: DeviceFilter) => void
  group: GroupFilter
  setGroup: (value: GroupFilter) => void
  /** Every group label in use, so the dropdown offers real groups only. */
  deviceGroups: string[]
}

/** Where a view can send the operator. */
export type Navigate = (view: View) => void
