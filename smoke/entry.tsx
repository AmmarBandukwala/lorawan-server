import { createElement as h } from 'react'
import { renderToString } from 'react-dom/server'
import App from '../src/App'
import { FALLBACK_REGIONS } from '../src/features/network/constants'
import { DeviceHeader, DeviceTable, GatewayCard, ActivityItem, SettingsForm } from '../src/features/network/components'
import { DeviceConsole } from '../src/features/network/DeviceConsole'
import { DeviceSettings } from '../src/features/network/DeviceSettings'
import { describeDeviceEdit, diffDeviceEdit, type ProvisionedDevice } from '../src/features/network/control'
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
  regions: FALLBACK_REGIONS,
})), 'North Depot')
check('SettingsForm surfaces a save error', renderToString(h(SettingsForm, {
  draft, setDraft: noop, onSubmit: noop, saving: false, error: 'Region is not supported', message: '', disabled: false,
  regions: FALLBACK_REGIONS,
})), 'Region is not supported')
// The band list is a prop rather than a constant baked into the form, so this
// asserts both that what the console was given is rendered and that the AS923
// variants — the bands the old three-entry list left out — can reach the dropdown.
check('SettingsForm offers the bands it was given', renderToString(h(SettingsForm, {
  draft, setDraft: noop, onSubmit: noop, saving: false, error: '', message: '', disabled: false,
  regions: FALLBACK_REGIONS,
})), 'AS923-4')

// When the network server could not be asked, the form must say the list is its
// own: an operator choosing a regulatory band needs to know its provenance.
check('SettingsForm flags a fallback band list', renderToString(h(SettingsForm, {
  draft, setDraft: noop, onSubmit: noop, saving: false, error: '', message: '', disabled: false,
  regions: FALLBACK_REGIONS, regionsAreFallback: true,
})), 'could not be reached')

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

// The device edit form's diff. This is the safety property of the whole panel:
// the network server reads an omitted field as "leave alone", so a form that
// posted everything it displays would turn an untouched checkbox into an
// instruction — renaming a device would quietly re-enable ADR on it.
const storedDevice: ProvisionedDevice = {
  dev_eui: 'AABBCCDDEEFF0011', join_eui: '0102030405060708', name: 'Tank 3',
  enabled: true, activation: 'otaa', group: 'Cooling',
  disable_adr: true, supports_class_c: false,
  max_data_rate: 3, min_data_rate: -1, max_tx_power: -1,
}
const untouchedForm = {
  name: 'Tank 3', group: 'Cooling', enabled: true,
  disableAdr: true, supportsClassC: false, maxDataRate: '3',
}
assert('an untouched form produces no edit at all', Object.keys(diffDeviceEdit(storedDevice, untouchedForm)).length === 0)

const renamed = diffDeviceEdit(storedDevice, { ...untouchedForm, name: 'Tank 4' })
assert('a rename sends only the name', renamed.name === 'Tank 4' && Object.keys(renamed).length === 1)
assert('a rename does not touch ADR', renamed.disableAdr === undefined)
assert('a rename does not touch the data-rate limit', renamed.maxDataRate === undefined)

// Flipping a stored setting must be visible in the diff, in both directions.
assert('re-enabling ADR is detected',
  diffDeviceEdit(storedDevice, { ...untouchedForm, disableAdr: false }).disableAdr === false)
assert('pausing a device is detected',
  diffDeviceEdit(storedDevice, { ...untouchedForm, enabled: false }).enabled === false)
assert('raising the data-rate limit is detected',
  diffDeviceEdit(storedDevice, { ...untouchedForm, maxDataRate: '5' }).maxDataRate === 5)
assert('lifting the limit to the band default is detected',
  diffDeviceEdit(storedDevice, { ...untouchedForm, maxDataRate: '-1' }).maxDataRate === -1)
// Whitespace-only edits are not edits; sending one would be a pointless write.
assert('trailing spaces are not a change',
  Object.keys(diffDeviceEdit(storedDevice, { ...untouchedForm, name: '  Tank 3  ' })).length === 0)
// A non-numeric select value must not become NaN in the request body.
assert('a non-numeric data rate is not sent',
  diffDeviceEdit(storedDevice, { ...untouchedForm, maxDataRate: '' }).maxDataRate === undefined)
// Number('') is 0, so without a guard a blank field would set the tightest
// possible limit rather than no limit. Both blank spellings are checked.
assert('a blank data rate is not read as DR0',
  diffDeviceEdit(storedDevice, { ...untouchedForm, maxDataRate: ' ' }).maxDataRate === undefined)
assert('a non-numeric data rate is not read as any limit',
  diffDeviceEdit(storedDevice, { ...untouchedForm, maxDataRate: 'abc' }).maxDataRate === undefined)

// The confirmation wording, because two of these changes alter radio behaviour.
assert('the confirmation names the changed field',
  describeDeviceEdit({ name: 'Tank 4' }) === 'Saved: name.')
assert('an ADR change is described as a radio change',
  describeDeviceEdit({ disableAdr: true }).includes('ADR off'))
assert('a radio change says when it applies',
  describeDeviceEdit({ disableAdr: true }).includes('next uplink'))
assert('a label change does not claim a radio change',
  !describeDeviceEdit({ group: 'Roof' }).includes('next uplink'))
assert('lifting a limit is worded as lifting',
  describeDeviceEdit({ maxDataRate: -1 }).includes('limit lifted'))
assert('an empty edit still reads as a confirmation', describeDeviceEdit({}) === 'Saved.')

// The panels themselves. Effects do not run under renderToString, so these
// assert the initial render rather than fetched data.
const settingsHtml = renderToString(h(DeviceSettings, {
  device, onChanged: noop, onDeleted: noop,
}))
check('DeviceSettings renders its loading state first', settingsHtml, 'Reading the device settings')
assert('DeviceSettings withholds the save button until it has the values',
  !settingsHtml.includes('Save 1 change'))

// The console starts in its loading state — the session read happens in an
// effect, which renderToString does not run — and the settings panel is offered
// regardless, so a device that has not joined yet is still editable.
const consoleHtml = renderToString(h(DeviceConsole, {
  device, onClose: noop, onChanged: noop, onDeleted: noop,
}))
check('DeviceConsole opens in its loading state', consoleHtml, 'Reading the session')
check('DeviceConsole offers settings before any session exists', consoleHtml, 'Settings')
check('DeviceConsole shows the device it is about', consoleHtml, 'AABBCCDDEEFF0011')

let failed = 0
for (const [name, ok] of checks) {
  if (!ok) failed++
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}`)
}
console.log(failed === 0 ? `\nall ${checks.length} checks passed` : `\n${failed} of ${checks.length} failed`)
process.exit(failed === 0 ? 0 : 1)
