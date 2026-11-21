import {
  ArrowUpRight, ChevronDown, Clock3, Radio, Search,
} from 'lucide-react'
import { DEVICE_FILTERS, LOW_BATTERY_PERCENT, UNASSIGNED_GROUP } from '../constants'
import type { Device, DeviceFilter, GroupFilter, View } from '../types'

export function DeviceHeader({ view, setView, query, setQuery, filter, setFilter, group, setGroup, groups }: {
  view: View
  setView: (view: View) => void
  query: string
  setQuery: (query: string) => void
  filter: DeviceFilter
  setFilter: (filter: DeviceFilter) => void
  group: GroupFilter
  setGroup: (group: GroupFilter) => void
  groups: string[]
}) {
  return <div className="device-header"><div className="panel-heading"><div><h2>{view === 'Devices' ? 'Device inventory' : 'Devices'}</h2><p>Most recently active end devices</p></div>{view !== 'Devices' && <button className="text-action" onClick={() => setView('Devices')}>View all <ArrowUpRight size={14} /></button>}</div><div className="table-tools"><label className="search-field"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search devices" /></label><select aria-label="Filter devices by status" value={filter} onChange={(event) => setFilter(event.target.value as DeviceFilter)}>{DEVICE_FILTERS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}</select><select aria-label="Filter devices by group" value={group} onChange={(event) => setGroup(event.target.value)}>
  <option value="all">All groups</option>
  {groups.map((name) => <option key={name} value={name}>{name}</option>)}
  <option value={UNASSIGNED_GROUP}>{UNASSIGNED_GROUP}</option>
</select></div></div>
}

/**
 * The device table.
 *
 * `onSelect` makes a row open the live session panel. It is optional so the
 * compact overview table stays a plain list: opening a session from a five-row
 * preview is more surprise than help, and the full view is where an operator
 * goes looking for one device.

 */
export function DeviceTable({ devices, onSelect }: { devices: Device[]; onSelect?: (device: Device) => void }) {
  return <div className="table-scroll"><table className="device-table"><thead><tr><th>DEVICE</th><th>GROUP</th><th>LAST SEEN</th><th>BATTERY</th><th>GATEWAY</th><th>STATUS</th><th><span className="sr-only">Actions</span></th></tr></thead><tbody>{devices.map((device) => <tr key={device.id} className={onSelect ? 'row-selectable' : undefined} onClick={onSelect ? () => onSelect(device) : undefined}><td><div className="device-name"><span className="device-symbol"><Radio size={15} /></span><span><b>{device.name}</b><small>{device.eui}</small></span></div></td><td>{device.group}</td><td><span className="seen-cell"><Clock3 size={13} />{device.lastSeen}</span></td><td><span className={`battery ${device.battery < LOW_BATTERY_PERCENT ? 'battery-low' : ''}`}><i><em style={{ width: `${device.battery}%` }} /></i>{device.battery ? `${device.battery}%` : '—'}</span></td><td>{device.gateway}</td><td><span className={`state-pill ${device.state === 'Online' ? 'state-online' : device.state === 'Offline' ? 'state-offline' : 'state-warning'}`}><i />{device.state}</span></td><td>{onSelect ? <button className="row-menu" aria-label={`Open the session of ${device.name}`} onClick={(event) => { event.stopPropagation(); onSelect(device) }}><ChevronDown size={15} /></button> : <button className="row-menu" aria-label={`More actions for ${device.name}`}><ChevronDown size={15} /></button>}</td></tr>)}</tbody></table>{devices.length === 0 && <div className="empty-result">No devices match this search.</div>}</div>
}
