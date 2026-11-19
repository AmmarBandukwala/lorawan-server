import { createElement as h } from 'react'
import { renderToString } from 'react-dom/server'
import App from '../src/App'
import { DeviceHeader, DeviceTable, GatewayCard, ActivityItem, SettingsForm } from '../src/features/network/components'
import type { Device, DeviceFilter, FacilitySettings, GatewayRecord, NetworkEvent } from '../src/features/network/types'

const noop = () => {}
const device: Device = { id: '1', name: 'Tank 3', eui: 'AABBCCDDEEFF0011', group: 'Cooling', gateway: 'GW-1', lastSeen: '2 minutes ago', battery: 88, state: 'Online', rssi: -71 }
const gateway: GatewayRecord = { id: 'gw1', name: 'GW-1', region: 'EU868', lastSeen: '1 minute ago', state: 'Online', uplinks24h: 12, averageRssi: -70, averageSnr: 8.5 }
const event: NetworkEvent = { id: 1, type: 'success', title: 'Device joined', detail: 'Tank 3 came online', timeAgo: '2 minutes ago' }
const draft: FacilitySettings = { configured: true, facilityName: 'North Depot', region: 'EU868', operatorName: 'Ada Lovelace', operatorRole: 'Operator' }

const checks: Array<[string, boolean]> = []
const check = (name: string, html: string, needle: string) => checks.push([name, html.includes(needle)])

// App itself: effects do not run under renderToString, so this asserts the
// initial shape renders rather than that data appears.
const shell = renderToString(h(App))
checks.push(['App renders without throwing', shell.length > 1000])
checks.push(['App shows the connecting state before data', shell.includes('Connecting')])
checks.push(['App reports an unconfigured facility rather than crashing', shell.includes('Facility not configured')])

// The components that receive the data, rendered with realistic props.
for (const filter of ['All devices', 'Online', 'Needs attention', 'Offline'] as DeviceFilter[]) {
  const html = renderToString(h(DeviceHeader, {
    view: 'Devices', setView: noop, query: '', setQuery: noop, filter, setFilter: noop,
  }))
  checks.push([`DeviceHeader renders filter=${filter}`, html.includes(`>${filter}</option>`)])
}
check('DeviceHeader marks the active filter', renderToString(h(DeviceHeader, {
  view: 'Devices', setView: noop, query: 'tank', setQuery: noop, filter: 'Needs attention', setFilter: noop,
})), 'selected=""') // React marks the chosen option; presence proves the value bound

check('DeviceTable renders a device row', renderToString(h(DeviceTable, { devices: [device] })), 'AABBCCDDEEFF0011')
check('DeviceTable renders its empty state', renderToString(h(DeviceTable, { devices: [] })), 'No devices')
check('GatewayCard renders radio health', renderToString(h(GatewayCard, { gateway })), 'GW-1')
check('ActivityItem renders an event', renderToString(h(ActivityItem, { event })), 'Device joined')
check('SettingsForm renders the draft', renderToString(h(SettingsForm, {
  draft, setDraft: noop, onSubmit: noop, saving: false, error: '', message: '', disabled: false,
})), 'North Depot')
check('SettingsForm surfaces a save error', renderToString(h(SettingsForm, {
  draft, setDraft: noop, onSubmit: noop, saving: false, error: 'Region is not supported', message: '', disabled: false,
})), 'Region is not supported')

let failed = 0
for (const [name, ok] of checks) {
  if (!ok) failed++
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}`)
}
console.log(failed === 0 ? `\nall ${checks.length} render checks passed` : `\n${failed} of ${checks.length} failed`)
process.exit(failed === 0 ? 0 : 1)
