import type { FormEvent } from 'react'
import {
  ArrowDown, ArrowUpRight, Check, ChevronDown, CircleCheck, Clock3, Radio,
  RadioTower, Search, Settings2, TriangleAlert,
} from 'lucide-react'
import { DEVICE_FILTERS, LOW_BATTERY_PERCENT, SIGNAL_QUALITY } from './constants'
import type { GatewayHealth } from './control'
import type { Device, DeviceFilter, FacilitySettings, GatewayRecord, NetworkEvent, View } from './types'

export function DeviceHeader({ view, setView, query, setQuery, filter, setFilter }: { view: View; setView: (view: View) => void; query: string; setQuery: (query: string) => void; filter: DeviceFilter; setFilter: (filter: DeviceFilter) => void }) {
  return <div className="device-header"><div className="panel-heading"><div><h2>{view === 'Devices' ? 'Device inventory' : 'Devices'}</h2><p>Most recently active end devices</p></div>{view !== 'Devices' && <button className="text-action" onClick={() => setView('Devices')}>View all <ArrowUpRight size={14} /></button>}</div><div className="table-tools"><label className="search-field"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search devices" /></label><select aria-label="Filter devices by status" value={filter} onChange={(event) => setFilter(event.target.value as DeviceFilter)}>{DEVICE_FILTERS.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}</select><button className="filter-button" aria-label="More filters"><Settings2 size={15} /></button></div></div>
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

/**
 * Signal quality bands for one gateway, from its average SNR.
 *
 * A null SNR means no reception has been recorded yet, which is not the same as
 * a weak signal, so it reports zero bars rather than the lowest band.
 */
function signalQuality(averageSnr: number | null): { quality: string; bars: number } {
  if (averageSnr === null) return { quality: 'No data', bars: 0 }
  if (averageSnr >= SIGNAL_QUALITY.excellentAbove) return { quality: 'Excellent', bars: 4 }
  if (averageSnr >= SIGNAL_QUALITY.goodAbove) return { quality: 'Good', bars: 3 }
  return { quality: 'Weak', bars: 1 }
}

export function Gateway({ gateway }: { gateway: GatewayRecord }) {
  const { quality, bars } = signalQuality(gateway.averageSnr)
  return <div className="gateway-row"><div className="gateway-icon"><RadioTower size={17} /></div><div className="gateway-info"><b>{gateway.name}</b><small>{gateway.region} <span>·</span> {gateway.uplinks24h.toLocaleString()} uplinks / 24h</small></div><div className="gateway-signal"><span className={`state-pill ${gateway.state === 'Online' ? 'state-online' : 'state-offline'}`}><i />{gateway.state}</span><div className="signal-bars" aria-label={`${quality} signal`}>{[1, 2, 3, 4].map((bar) => <i key={bar} className={bar <= bars ? 'filled' : ''} />)}</div></div></div>
}

/**
 * One gateway in the full view.
 *
 * `health` is the network server's live report for this EUI, kept separate from
 * the inventory record because they answer different questions. The record says
 * what has been stored about a gateway; the health report says whether it is
 * still sending PULL_DATA, how many downlinks it refused, and — through
 * `last_tx_ack_error` — why. A gateway that accepts every uplink and refuses
 * every downlink looks perfectly healthy in the database.
 */
export function GatewayCard({ gateway, health }: { gateway: GatewayRecord; health?: GatewayHealth }) {
  const refused = health && health.downlink_errors > 0
  return <article className="panel gateway-card"><div className="gateway-card-top"><div className="gateway-icon large"><RadioTower size={20} /></div><span className={`state-pill ${(health ? health.online : gateway.state === 'Online') ? 'state-online' : 'state-offline'}`}><i />{health ? (health.online ? 'Online' : 'Silent') : gateway.state}</span></div><h2>{gateway.name}</h2><p className="gateway-address">{gateway.id} <span>·</span> {health ? health.address : gateway.region}</p><div className="gateway-stats"><div><span>Uplinks · 24h</span><b>{gateway.uplinks24h.toLocaleString()}</b></div><div><span>Average RSSI</span><b>{gateway.averageRssi === null ? 'No data' : `${gateway.averageRssi.toFixed(0)} dBm`}</b></div><div><span>Last reception</span><b>{gateway.lastSeen}</b></div><div><span>Average SNR</span><b>{gateway.averageSnr === null ? 'No data' : `${gateway.averageSnr.toFixed(1)} dB`}</b></div></div>{health && <div className="gateway-health"><div><span>Packets seen</span><b>{health.packets.toLocaleString()}</b></div><div><span>Downlinks sent</span><b>{health.downlinks_sent.toLocaleString()}</b></div><div><span>Downlinks refused</span><b className={refused ? 'text-warning' : undefined}>{health.downlink_errors.toLocaleString()}</b></div><div><span>RX packets OK</span><b>{health.rx_ok.toLocaleString()} / {health.rx_received.toLocaleString()}</b></div></div>}{refused && <p className="form-error" role="alert">Last refusal: {health.last_tx_ack_error || 'the concentrator did not say why'}. Downlinks scheduled for this gateway are not reaching devices.</p>}<div className="gateway-card-footer"><CircleCheck size={15} /> {health ? `Protocol v${health.protocol_version} · seen ${health.last_seen}` : 'Persisted gateway record'}<button aria-label={`Gateway record for ${gateway.name}`}><ArrowUpRight size={15} /></button></div></article>
}

export function ActivityItem({ event }: { event: NetworkEvent }) {
  return <div className={`activity-item`}><span className={`activity-icon activity-${event.type}`}>{event.type === 'success' ? <ArrowDown size={14} /> : event.type === 'warning' ? <TriangleAlert size={14} /> : <Radio size={14} />}</span><span className="activity-copy"><b>{event.title}</b><small>{event.detail}</small></span><time>{event.timeAgo}</time></div>
}

/**
 * The facility profile form.
 *
 * `regions` is the band list the console was given, and `regionsAreFallback`
 * says whether the network server could not be reached to supply it. The band is
 * a regulatory setting, so the form says where its options came from rather than
 * presenting a guess with the same confidence as an answer.
 */
export function SettingsForm({ draft, setDraft, onSubmit, saving, error, message, disabled, regions, regionsAreFallback }: { draft: FacilitySettings; setDraft: (settings: FacilitySettings) => void; onSubmit: (event: FormEvent<HTMLFormElement>) => void; saving: boolean; error: string; message: string; disabled: boolean; regions: readonly string[]; regionsAreFallback?: boolean }) {
  return <section className="panel settings-view"><div className="panel-heading"><div><h2>Facility profile</h2><p>These values are stored in the local database.</p></div></div><form className="settings-form" onSubmit={onSubmit}><label>Facility name<input required maxLength={120} value={draft.facilityName} onChange={(event) => setDraft({ ...draft, facilityName: event.target.value })} /></label><label>LoRaWAN region<select required value={draft.region} onChange={(event) => setDraft({ ...draft, region: event.target.value })}><option value="">Select region</option>{regions.map((region) => <option key={region} value={region}>{region}</option>)}</select>{regionsAreFallback && <small className="field-hint">The network server could not be reached, so this list is the console’s own. The band the network enforces comes from its NS_REGION setting.</small>}</label><label>Operator display name<input required maxLength={120} value={draft.operatorName} onChange={(event) => setDraft({ ...draft, operatorName: event.target.value })} /></label><label>Operator role<input required maxLength={80} value={draft.operatorRole} onChange={(event) => setDraft({ ...draft, operatorRole: event.target.value })} /></label>{error && <p className="form-error" role="alert">{error}</p>}{message && <p className="form-success" role="status">{message}</p>}<div className="modal-actions"><button className="button button-primary" type="submit" disabled={disabled || saving}><Check size={15} />{saving ? 'Saving…' : 'Save settings'}</button></div></form></section>
}
