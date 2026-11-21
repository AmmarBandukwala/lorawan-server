/**
 * The Devices page: the whole inventory, filtered, with one device's live
 * session openable from the row.
 */
import { DeviceHeader, DeviceTable } from '../components'
import { DeviceConsole } from '../DeviceConsole'
import type { Device, View } from '../types'
import type { DeviceListControls, Navigate } from './shared'

export function DevicesView({
  filteredDevices, selected, setSelected, refresh, view, setView,
  query, setQuery, filter, setFilter, group, setGroup, deviceGroups,
}: {
  filteredDevices: Device[]
  /** The row whose session panel is open; null shows the table alone. */
  selected: Device | null
  /** Open one row's session, or close it with null. */
  setSelected: (device: Device | null) => void
  /** Re-read the snapshot, so a command that moved the session shows it. */
  refresh: () => void
  view: View
  setView: Navigate
} & DeviceListControls) {
  return <div className="device-view"><section className="panel device-panel full-device-panel"><DeviceHeader view={view} setView={setView} query={query} setQuery={setQuery} filter={filter} setFilter={setFilter} group={group} setGroup={setGroup} groups={deviceGroups} /><DeviceTable devices={filteredDevices} onSelect={setSelected} /></section>{selected && <DeviceConsole device={selected} onClose={() => setSelected(null)} onChanged={() => void refresh()} onDeleted={() => { setSelected(null); void refresh() }} />}</div>
}
