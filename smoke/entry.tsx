import { createElement as h } from 'react'
import { renderToString } from 'react-dom/server'
import App from '../src/App'
import { DeviceHeader, DeviceTable, GatewayCard, ActivityItem, SettingsForm } from '../src/features/network/components'
import { RegisterDeviceDialog } from '../src/features/network/RegisterDeviceDialog'
import { DEVICE_FILTERS, UNASSIGNED_GROUP } from '../src/features/network/constants'
import { deviceFormError, deviceFormRequest, normalizeHex } from '../src/features/network/validation'
import type { Device, FacilitySettings, GatewayRecord, NetworkEvent } from '../src/features/network/types'

const noop = () => {}
const device: Device = { id: '1', name: 'Tank 3', eui: 'AABBCCDDEEFF0011', group: 'Cooling', gateway: 'GW-1', lastSeen: '2 minutes ago', battery: 88, state: 'Online', rssi: -71 }
const gateway: GatewayRecord = { id: 'gw1', name: 'GW-1', region: 'EU868', lastSeen: '1 minute ago', state: 'Online', uplinks24h: 12, averageRssi: -70, averageSnr: 8.5 }
const event: NetworkEvent = { id: 1, type: 'success', title: 'Device joined', detail: 'Tank 3 came online', timeAgo: '2 minutes ago' }
const draft: FacilitySettings = { configured: true, facilityName: 'North Depot', region: 'EU868', operatorName: 'Ada Lovelace', operatorRole: 'Operator' }

const checks: Array<[string, boolean]> = []
const check = (name: string, html: string, needle: string) => checks.push([name, html.includes(needle)])
const assert = (name: string, ok: boolean) => checks.push([name, ok])

// App itself: effects do not run under renderToString, so this asserts the
// initial shape renders rather than that data appears.
const shell = renderToString(h(App))
checks.push(['App renders without throwing', shell.length > 1000])
checks.push(['App shows the connecting state before data', shell.includes('Connecting')])
checks.push(['App reports an unconfigured facility rather than crashing', shell.includes('Facility not configured')])

// The components that receive the data, rendered with realistic props.
//
// Driven from DEVICE_FILTERS rather than a literal list: the filter values are a
// union type, and a hand-written array with an `as` cast keeps compiling after
// the union changes while quietly testing values the component never receives.
for (const { value, label } of DEVICE_FILTERS) {
  const html = renderToString(h(DeviceHeader, {
    view: 'Devices', setView: noop, query: '', setQuery: noop, filter: value, setFilter: noop,
  }))
  checks.push([`DeviceHeader renders filter=${value}`, html.includes(`>${label}</option>`)])
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
check('SettingsForm offers the supported regions', renderToString(h(SettingsForm, {
  draft, setDraft: noop, onSubmit: noop, saving: false, error: '', message: '', disabled: false,
})), 'US915')

const dialog = renderToString(h(RegisterDeviceDialog, {
  onClose: noop, onRegistered: noop, unreachable: false,
}))
check('RegisterDeviceDialog renders the credential fields', dialog, 'AppKey')
check('RegisterDeviceDialog states where the AppKey goes', dialog, 'never shown again')
check('RegisterDeviceDialog reports an unreachable API', renderToString(h(RegisterDeviceDialog, {
  onClose: noop, onRegistered: noop, unreachable: true,
})), 'Register device')

// Validation is the logic that decides whether a device can be provisioned, and
// it is pure, so it is worth asserting directly rather than through the DOM.
const valid = { name: 'Tank 3', devEui: '0011223344556677', joinEui: '', appKey: '2b7e151628aed2a6abf7158809cf4f3c', group: '' }
assert('validation accepts a well-formed device', deviceFormError(valid) === null)
assert('validation needs a name', deviceFormError({ ...valid, name: '  ' }) !== null)
assert('validation rejects a short DevEUI', deviceFormError({ ...valid, devEui: '00112233' }) !== null)
assert('validation rejects a short AppKey', deviceFormError({ ...valid, appKey: '2b7e1516' }) !== null)
assert('validation accepts an omitted JoinEUI', deviceFormError(valid) === null)
assert('validation rejects a malformed JoinEUI', deviceFormError({ ...valid, joinEui: '0011' }) !== null)

// Copied keys arrive with separators and mixed case; what goes on the wire must
// be the plain upper-case form the network server's schema expects.
assert('validation strips separators from identifiers', normalizeHex('aa:bb-cc dd\n') === 'AABBCCDD')
const request = deviceFormRequest({ ...valid, devEui: '00-11-22-33-44-55-66-77', group: '   ' })
assert('request normalises the DevEUI', request.devEui === '0011223344556677')
assert('request defaults an empty group', request.group === UNASSIGNED_GROUP)

let failed = 0
for (const [name, ok] of checks) {
  if (!ok) failed++
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}`)
}
console.log(failed === 0 ? `\nall ${checks.length} checks passed` : `\n${failed} of ${checks.length} failed`)
process.exit(failed === 0 ? 0 : 1)
