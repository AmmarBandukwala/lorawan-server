/**
 * The Devices page.
 *
 * It shows the whole device list, filtered by the search box and the two
 * dropdowns. Clicking a row opens that device's session panel on the right.
 *
 * The layout has two shapes, and the CSS class on the outer <div> is what
 * switches between them:
 *   - no device picked  -> the list uses the full page width
 *   - a device picked   -> the list shrinks and the panel takes a fixed column
 */
import { DeviceHeader, DeviceTable } from '../components'
import { DeviceConsole } from '../panels/DeviceConsole'
import type { Device, View } from '../types'
import type { DeviceListControls, Navigate } from './shared'

export function DevicesView({
  filteredDevices, selected, setSelected, refresh, view, setView,
  query, setQuery, filter, setFilter, group, setGroup, deviceGroups,
}: {
  /** The devices left after the search text and dropdowns are applied. */
  filteredDevices: Device[]
  /** The device whose panel is open, or null when no panel is open. */
  selected: Device | null
  /** Opens a device's panel. Pass null to close it. */
  setSelected: (device: Device | null) => void
  /** Reads the data again, so a change you just made shows up. */
  refresh: () => void
  view: View
  setView: Navigate
} & DeviceListControls) {
  /** Called after the panel changes or deletes the device it was showing. */
  function closeAndRefresh() {
    setSelected(null)
    void refresh()
  }

  return (
    <div className={selected ? 'device-view with-console' : 'device-view'}>
      <section className="panel device-panel full-device-panel">
        <DeviceHeader
          view={view}
          setView={setView}
          query={query}
          setQuery={setQuery}
          filter={filter}
          setFilter={setFilter}
          group={group}
          setGroup={setGroup}
          groups={deviceGroups}
        />
        <DeviceTable devices={filteredDevices} onSelect={setSelected} />
      </section>

      {selected && (
        <DeviceConsole
          device={selected}
          onClose={() => setSelected(null)}
          onChanged={() => void refresh()}
          onDeleted={closeAndRefresh}
        />
      )}
    </div>
  )
}

