import { createElement as h } from 'react'
import { renderToString } from 'react-dom/server'
import App from '../src/App'
import { FALLBACK_REGIONS } from '../src/features/network/constants'
import { ActivityItem, DeviceHeader, DeviceTable, GatewayCard, PageHeadingAction, SettingsForm } from '../src/features/network/components'
import { DeviceConsole } from '../src/features/network/panels/DeviceConsole'
import { DeviceSettings } from '../src/features/network/panels/DeviceSettings'
import { DeviceModels } from '../src/features/network/panels/DeviceModels'
import { GatewayConnectPanel, LiveGatewayCard } from '../src/features/network/panels/Gateways'
import { DecodeHealthPanel, NetworkServerPanel } from '../src/features/network/panels/NetworkHealth'
import { attributeMap, attributeRows, describeDeviceEdit, diffDeviceEdit, locationFields, locationProblem } from '../src/features/network/control/device'
import { modelIdProblem } from '../src/features/network/control/models'
import { decodeHealth, formatUptime, gatewaySummary, recentlyRestarted } from '../src/features/network/control/status'
import { formatDecoded } from '../src/features/network/control/traffic'
import type { NetworkStatus, ProvisionedDevice } from '../src/features/network/control/types'
import { RegisterDeviceDialog } from '../src/features/network/panels/RegisterDeviceDialog'
import { EventsView } from '../src/features/network/views/EventsView'
import { GatewaysView } from '../src/features/network/views/GatewaysView'
import { OverviewView } from '../src/features/network/views/OverviewView'
import { DevicesView } from '../src/features/network/views/DevicesView'
import { DEVICE_FILTERS, endpointFor, UNASSIGNED_GROUP, viewOffersDeviceRegistration } from '../src/features/network/constants'
import { deviceFormError, deviceFormRequest, normalizeHex } from '../src/features/network/validation'
import { CommandRejected, failureMessage, isNotFound, isServerUnavailable } from '../src/features/network/control/http'
import type { GatewayHealth } from '../src/features/network/control/types'
import type { Device, FacilitySettings, GatewayRecord, NetworkEvent, Overview, View } from '../src/features/network/types'
import { emptyOverview } from '../src/features/network/types'

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
    group: 'all', setGroup: noop, groups: ['Cooling', 'Roof'],
  }))
  checks.push([`DeviceHeader renders filter=${value}`, html.includes(`>${label}</option>`)])
}
check('DeviceHeader marks the active filter', renderToString(h(DeviceHeader, {
  view: 'Devices', setView: noop, query: 'tank', setQuery: noop, filter: 'Needs attention', setFilter: noop,
  group: 'all', setGroup: noop, groups: ['Cooling'],
})), 'selected=""') // React marks the chosen option; presence proves the value bound

// The group dropdown is built from the groups that exist, so it has to show both
// what was passed in and the unassigned bucket — devices nobody has organised are
// the ones someone filtering by group most needs to find.
const headerHtml = renderToString(h(DeviceHeader, {
  view: 'Devices', setView: noop, query: '', setQuery: noop, filter: 'all', setFilter: noop,
  group: 'Roof', setGroup: noop, groups: ['Cooling', 'Roof'],
}))
check('DeviceHeader offers the groups it was given', headerHtml, '>Cooling</option>')
check('DeviceHeader offers the unassigned bucket', headerHtml, `>${UNASSIGNED_GROUP}</option>`)
assert('DeviceHeader marks the chosen group', headerHtml.includes('value="Roof" selected'))

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
  attributes: { line: '3', purpose: 'cold store' },
  location: { latitude: 51.5074, longitude: -0.1278, altitude: 12 },
}
const untouchedForm = {
  name: 'Tank 3', group: 'Cooling', enabled: true,
  disableAdr: true, supportsClassC: false, maxDataRate: '3',
  attributes: attributeRows(storedDevice.attributes),
  modelId: '',
  ...locationFields(storedDevice.location),
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

// Device context: attributes and position. These are the fields most likely to be
// mistyped and the least likely to be checked by anything else, so the pure logic
// is asserted directly.
assert('attribute rows round-trip into a map',
  JSON.stringify(attributeMap(attributeRows(storedDevice.attributes)).map) ===
  JSON.stringify({ line: '3', purpose: 'cold store' }))
assert('attribute keys are lower-cased and trimmed',
  attributeMap([{ key: ' Line ', value: '3' }]).map?.line === '3')
assert('a blank row is not stored', Object.keys(attributeMap([{ key: '', value: '' }]).map ?? {}).length === 0)
assert('a key with no value is refused', attributeMap([{ key: 'line', value: '  ' }]).problem !== undefined)
assert('a key with spaces is refused', attributeMap([{ key: 'rooftop antenna', value: '1' }]).problem !== undefined)
assert('two rows that normalise to one key are refused rather than merged',
  attributeMap([{ key: 'Line', value: '3' }, { key: 'line', value: '4' }]).problem !== undefined)

assert('an unchanged attribute set produces no edit',
  diffDeviceEdit(storedDevice, untouchedForm).attributes === undefined)
assert('editing one value sends the whole map',
  diffDeviceEdit(storedDevice, {
    ...untouchedForm, attributes: [{ key: 'line', value: '4' }, { key: 'purpose', value: 'cold store' }],
  }).attributes?.line === '4')
assert('removing every attribute is an explicit clearing, not silence',
  diffDeviceEdit(storedDevice, { ...untouchedForm, attributes: [] }).attributes !== undefined &&
  Object.keys(diffDeviceEdit(storedDevice, { ...untouchedForm, attributes: [] }).attributes ?? {}).length === 0)
assert('an unusable attribute row produces no attribute edit',
  diffDeviceEdit(storedDevice, { ...untouchedForm, attributes: [{ key: 'bad key', value: 'x' }] }).attributes === undefined)

// Position. The three cases the form has to distinguish are a change, a clearing
// and a half-typed entry that must never reach the network.
assert('an unchanged position produces no edit', diffDeviceEdit(storedDevice, untouchedForm).location === undefined)
assert('moving the device sends a position',
  diffDeviceEdit(storedDevice, { ...untouchedForm, latitude: '51.51' }).location?.latitude === 51.51)
assert('clearing all three fields on a placed device removes the position',
  diffDeviceEdit(storedDevice, { ...untouchedForm, latitude: '', longitude: '', altitude: '' }).location === null)
assert('a device with no position and empty fields sends nothing',
  diffDeviceEdit({ ...storedDevice, location: undefined }, {
    ...untouchedForm, latitude: '', longitude: '', altitude: '',
  }).location === undefined)
assert('a latitude without a longitude is not sent',
  diffDeviceEdit(storedDevice, { ...untouchedForm, longitude: '' }).location === undefined)
assert('a latitude without a longitude is explained',
  locationProblem({ ...untouchedForm, longitude: '' }).includes('both'))
assert('an out-of-range latitude is refused',
  locationProblem({ ...untouchedForm, latitude: '91' }).includes('-90 and 90'))
assert('a valid position reports no problem', locationProblem(untouchedForm) === '')
// An absent altitude must not render as 0, which is a real height.
assert('a position without an altitude leaves the box blank',
  locationFields({ latitude: 1, longitude: 2 }).altitude === '')
assert('an altitude of zero is shown as zero, not blank',
  locationFields({ latitude: 1, longitude: 2, altitude: 0 }).altitude === '0')

assert('clearing attributes is worded as clearing',
  describeDeviceEdit({ attributes: {} }).includes('attributes cleared'))
assert('a new position is described with its coordinates',
  describeDeviceEdit({ location: { latitude: 51.5, longitude: -0.12 } }).includes('51.50000'))
assert('removing a position is worded as removing',
  describeDeviceEdit({ location: null }).includes('position removed'))

// The panels themselves. Effects do not run under renderToString, so these
// assert the initial render rather than fetched data.
const settingsHtml = renderToString(h(DeviceSettings, {
  device, onChanged: noop, onDeleted: noop,
}))
check('DeviceSettings renders its loading state first', settingsHtml, 'Reading the device settings')
assert('DeviceSettings withholds the save button until it has the values',
  !settingsHtml.includes('Save 1 change'))

// The gateway connect panel: the endpoint is the point of it, so both transports
// must appear with their ports, and the UDP one has to say it is unauthenticated
// rather than let someone assume the port is safe to expose.
const connectHtml = renderToString(h(GatewayConnectPanel, {
  listeners: [
    { transport: 'semtech-udp', bind_addr: ':6858', port: 6858, host_wildcard: true, secured: false },
    { transport: 'basic-station', bind_addr: ':30001', port: 30001, host_wildcard: false, secured: true },
  ],
}))
check('GatewayConnectPanel shows the UDP port', connectHtml, '6858')
check('GatewayConnectPanel shows the station endpoint', connectHtml, '30001/updates')
check('GatewayConnectPanel names both transports', connectHtml, 'Basics Station')
check('GatewayConnectPanel warns that UDP is unauthenticated', connectHtml, 'claim a gateway EUI')
check('GatewayConnectPanel says a gateway registers itself', connectHtml, 'nothing to register first')
// A server that binds every interface cannot know which address the gateway
// reaches it on, and the panel has to admit it substituted one.
check('GatewayConnectPanel flags a substituted host', connectHtml, 'loaded from')
// A station endpoint that authenticates must not inherit the UDP warning.
assert('a secured endpoint does not warn about claiming gateways',
  !connectHtml.split('Basics Station')[1]?.includes('claim a gateway EUI'))
// With no listeners at all the panel must not invent an endpoint.
check('GatewayConnectPanel says so when nothing is reporting',
  renderToString(h(GatewayConnectPanel, { listeners: [] })), 'not reported')

const liveHtml = renderToString(h(LiveGatewayCard, {
  region: 'EU868',
  health: {
    gateway_eui: 'AA55555555555555', transport: 'semtech-udp', protocol: 'semtech-udp v2',
    address: '192.168.1.40:44211', online: true, last_seen: '2026-11-17T10:00:00Z', packets: 120,
    downlinks_sent: 7, downlink_errors: 2, downlinks_confirmed: 5, downlinks_unconfirmed: 2,
    last_tx_ack_error: 'TOO_LATE', rx_received: 90, rx_ok: 88, tx_received: 7,
  },
}))
check('LiveGatewayCard shows the gateway EUI', liveHtml, 'AA55555555555555')
check('LiveGatewayCard reports a refused downlink', liveHtml, 'TOO_LATE')
check('LiveGatewayCard shows the transport', liveHtml, 'UDP forwarder')

// The console starts in its loading state — the session read happens in an
// effect, which renderToString does not run — and the settings panel is offered
// regardless, so a device that has not joined yet is still editable.
const consoleHtml = renderToString(h(DeviceConsole, {
  device, onClose: noop, onChanged: noop, onDeleted: noop,
}))
check('DeviceConsole opens in its loading state', consoleHtml, 'Reading the session')
check('DeviceConsole offers settings before any session exists', consoleHtml, 'Settings')
check('DeviceConsole shows the device it is about', consoleHtml, 'AABBCCDDEEFF0011')
// The readings panel reads stored uplinks in an effect, so the first render says
// it is reading rather than claiming the device has never reported.
check('DeviceConsole starts the readings read rather than guessing', consoleHtml, 'Reading stored uplinks')

// Device models: the vendor-formatter editor and the helpers around it.
const modelsHtml = renderToString(h(DeviceModels))
check('DeviceModels renders its heading', modelsHtml, 'Device models')
check('DeviceModels offers a new model', modelsHtml, 'New model')
assert('DeviceModels does not declare an empty fleet before the read',
  !modelsHtml.includes('No device models yet'))

assert('an empty model id asks for one', modelIdProblem('') !== '')
assert('a one-character model id is refused', modelIdProblem('p') !== '')
assert('an upper-case model id is refused', modelIdProblem('Probe') !== '')
assert('a normal model id is accepted', modelIdProblem('cold-chain-probe-v2') === '')

// A formatter's object becomes lines an operator can read. Nested and array
// readings must be flattened rather than collapsed into "[object Object]", and a
// null field must survive as a field.
const flat = formatDecoded({ temperature: 21.5, channels: [{ rh: 40 }], alarm: null })
assert('a scalar reading keeps its field', flat.some((l) => l.key === 'temperature' && l.value === '21.5'))
assert('an array reading is flattened with indexed keys',
  flat.some((l) => l.key === 'channels[0].rh' && l.value === '40'))
assert('a null field is shown rather than dropped', flat.some((l) => l.key === 'alarm'))
assert('nothing decoded means no lines', formatDecoded(undefined).length === 0)

// The model association on a device edit, both directions: clearing is the case
// a naive diff loses, and a model that cannot be detached leaves a device being
// decoded by a formatter that no longer describes it.
const storedWithModel: ProvisionedDevice = { ...storedDevice, model_id: 'cold-chain-probe-v2' }
assert('a changed model shows up as an edit',
  diffDeviceEdit(storedWithModel, { ...untouchedForm, modelId: 'other-probe' }).modelId === 'other-probe')
assert('clearing a model shows up as an edit',
  diffDeviceEdit(storedWithModel, { ...untouchedForm, modelId: '' }).modelId === '')
assert('an unchanged model produces no edit',
  Object.keys(diffDeviceEdit(storedWithModel, { ...untouchedForm, modelId: 'cold-chain-probe-v2' })).length === 0)
assert('a device with no model and no choice produces no edit',
  Object.keys(diffDeviceEdit(storedDevice, untouchedForm)).length === 0)
assert('the save message names the model', describeDeviceEdit({ modelId: 'probe-2' }).includes('probe-2'))
assert('the save message says what clearing costs',
  describeDeviceEdit({ modelId: '' }).includes('no longer be decoded'))

// ---------------------------------------------------------------------------
// Network-server health: the values the status endpoint reports and the console
// used to discard.
// ---------------------------------------------------------------------------

// Every required field of GatewayHealth, spelled out. This fixture used to omit
// the counters the gateway cards read, which the compiler never caught because
// the smoke harness was not in any tsconfig — the card crashed at render time
// instead, taking the whole view with it.
const health = (gateway_eui: string, online: boolean): GatewayHealth => ({
  gateway_eui, transport: 'semtech-udp', protocol: '2', address: '10.0.0.5:44321',
  online, last_seen: '2026-11-17T10:00:00Z', packets: 41,
  downlinks_sent: 3, downlink_errors: 0, downlinks_confirmed: 3, downlinks_unconfirmed: 0,
  rx_received: 41, rx_ok: 40, tx_received: 3,
})
const liveStatus: NetworkStatus = {
  uptime_seconds: 3720, region: 'EU868', net_id: '000013', downlink_power_dbm: 14,
  gateways_total: 2, gateways_online: 1,
  gateways: [health('AA55555555555555', true), health('BB55555555555555', false)],
  transports: ['semtech-udp'],
}

// Uptime is read as "is this the long-running process or the one that just came
// back", so the unit has to match the magnitude.
assert('uptime over a day is read in days', formatUptime(90000) === '1d 1h')
assert('uptime in hours keeps minutes', formatUptime(3720) === '1h 2m')
assert('uptime under an hour keeps seconds', formatUptime(95) === '1m 35s')
assert('a sub-minute uptime is still shown', formatUptime(7) === '7s')
assert('an absent uptime says unknown rather than a number', formatUptime(-1) === 'Unknown')
assert('a server up 14 seconds looks like a restart', recentlyRestarted(14))
assert('a server up an hour does not', !recentlyRestarted(3600))

// Gateway counts: the inventory and the live list are different populations, so
// neither number alone is "how many gateways are online".
const twoHeard = gatewaySummary(liveStatus, ['AA55555555555555', 'CC55555555555555'])
assert('online comes from what the server hears', twoHeard.online === 1)
// 2 the server has heard of, plus the one inventory row it never saw.
assert('the total is the union, not the larger of the two', twoHeard.total === 3)
assert('a gateway heard from is not counted twice',
  gatewaySummary(liveStatus, ['aa55555555555555']).total === 2)
const blindSummary = gatewaySummary(null, ['AA55555555555555', 'BB55555555555555'])
assert('without the server the count is labelled as inventory', blindSummary.source === 'inventory')
assert('and it claims nobody is online', blindSummary.online === 0 && blindSummary.total === 2)

const serverHtml = renderToString(h(NetworkServerPanel, { status: liveStatus, inventoryIds: [] }))
check('NetworkServerPanel shows the NetID to write into a device', serverHtml, '000013')
check('NetworkServerPanel shows the band', serverHtml, 'EU868')
check('NetworkServerPanel shows how long the server has been up', serverHtml, 'up 1h 2m')
check('NetworkServerPanel names the transports it listens for', serverHtml, 'UDP forwarder')
assert('NetworkServerPanel renders nothing when the server is unreachable',
  renderToString(h(NetworkServerPanel, { status: null, inventoryIds: ['x'] })) === '')

// Decode health. The two failure shapes have to read differently: a formatter
// that will not compile is a fleet-wide fault affecting every frame from those
// devices, and a frame the formatter refused is one bad frame.
const brokenDecoding = {
  enabled: true, frames_decoded: 90, frames_failed: 4,
  compiled_formatters: 3, broken_formatters: 2,
  broken_models: [
    { model_id: 'aqi-pro', reason: 'syntax error: unexpected token' },
    { model_id: 'meter-3', reason: 'script is empty' },
  ],
}
assert('no decoder reported means no panel', decodeHealth(undefined) === null)
const brokenHealth = decodeHealth(brokenDecoding)
assert('broken formatters are reported', brokenHealth !== null && brokenHealth.broken.length === 2)
assert('the warning names the models to go and fix',
  brokenHealth !== null && brokenHealth.problem.includes('aqi-pro') && brokenHealth.problem.includes('meter-3'))
assert('the warning carries the compiler message',
  brokenHealth !== null && brokenHealth.problem.includes('unexpected token'))
assert('the warning says the devices still look healthy',
  brokenHealth !== null && brokenHealth.problem.includes('look healthy'))

const noisyDecoding = { ...brokenDecoding, broken_formatters: 0, broken_models: [] }
const noisyHealth = decodeHealth(noisyDecoding)
assert('a rejected frame is not reported as a broken formatter',
  noisyHealth !== null && !noisyHealth.problem.includes('will not run'))
assert('a rejected frame still warns', noisyHealth !== null && noisyHealth.problem.includes('rejected'))

const cleanDecoding = {
  enabled: true, frames_decoded: 90, frames_failed: 0,
  compiled_formatters: 3, broken_formatters: 0,
}
assert('a healthy fleet has nothing to warn about',
  decodeHealth(cleanDecoding)?.problem === '')

const decodeHtml = renderToString(h(DecodeHealthPanel, { decoding: brokenDecoding }))
check('DecodeHealthPanel names the failing model', decodeHtml, 'aqi-pro')
check('DecodeHealthPanel shows why it fails', decodeHtml, 'unexpected token')
check('DecodeHealthPanel says decoding is running', decodeHtml, 'formatters are running')
assert('DecodeHealthPanel renders nothing when there is no decoder',
  renderToString(h(DecodeHealthPanel, { decoding: undefined })) === '')
// A clean report must not read like an alarm, and must not overstate what the
// counters cover.
const cleanHtml = renderToString(h(DecodeHealthPanel, { decoding: cleanDecoding }))
assert('a clean decode report shows no alert', !cleanHtml.includes('role="alert"'))
check('the counters say what they cover', cleanHtml, 'Only models that have sent a')

// ---------------------------------------------------------------------------
// Per-view heading action.
//
// "Add device" used to render on every view except Settings and Models, so the
// Gateways page offered a device form while its own first panel explains that
// gateways are not registered here, and Events offered it for no reason at all.
// It then stayed on the Overview, whose device panel is a five-row preview: the
// form opened for a device you could not see in context, when the inventory,
// filters and session console all live on the Devices page.
// ---------------------------------------------------------------------------

const deviceViews: View[] = ['Devices']
const otherViews: View[] = ['Overview', 'Gateways', 'Events', 'Models', 'Settings']
for (const view of deviceViews) {
  assert(`the heading offers device registration on ${view}`, viewOffersDeviceRegistration(view))
}
for (const view of otherViews) {
  assert(`the heading offers no device registration on ${view}`, !viewOffersDeviceRegistration(view))
}

assert('the Gateways heading renders no primary action at all',
  renderToString(h(PageHeadingAction, {
    view: 'Gateways', canProvision: true, onAddDevice: noop,
  })) === '')
assert('the Events heading renders no primary action at all',
  renderToString(h(PageHeadingAction, {
    view: 'Events', canProvision: true, onAddDevice: noop,
  })) === '')
assert('the Overview heading renders no primary action at all',
  renderToString(h(PageHeadingAction, {
    view: 'Overview', canProvision: true, onAddDevice: noop,
  })) === '')
const devicesHeading = renderToString(h(PageHeadingAction, {
  view: 'Devices', canProvision: true, onAddDevice: noop,
}))
check('the Devices heading still offers Add device', devicesHeading, 'Add device')
// Disabled rather than hidden: the action stays discoverable, and the connection
// notice above the heading is what explains why it cannot be used yet.
const offlineHeading = renderToString(h(PageHeadingAction, {
  view: 'Devices', canProvision: false, onAddDevice: noop,
}))
assert('the action is disabled rather than hidden while the API is unreachable',
  offlineHeading.includes('Add device') && offlineHeading.includes('disabled'))
// The Overview is what App renders first, so this checks the wiring end to end
// rather than only the component in isolation.
assert('the Overview page renders no Add device button', !shell.includes('Add device'))
assert('the Overview still offers its route to the page that owns provisioning',
  shell.includes('View all'))

// The address a gateway is pointed at. One function builds it so the card and
// the clipboard can never disagree.
assert('a station endpoint carries the scheme and the path',
  endpointFor('basic-station', '10.0.0.4', '30001') === 'wss://10.0.0.4:30001/updates')
assert('a forwarder endpoint is a bare host and port',
  endpointFor('semtech-udp', '10.0.0.4', '6858') === '10.0.0.4:6858')
assert('an unknown port is left off rather than rendered as :0',
  endpointFor('semtech-udp', '10.0.0.4', 0) === '10.0.0.4')

const copyPanelHtml = renderToString(h(GatewayConnectPanel, {
  listeners: [{
    transport: 'semtech-udp', bind_addr: '0.0.0.0', port: 6858,
    host_wildcard: true, secured: false,
  }, {
    transport: 'basic-station', bind_addr: '0.0.0.0', port: 30001,
    host_wildcard: true, secured: true,
  }],
}))
check('the connect panel offers to copy the forwarder endpoint', copyPanelHtml, '6858')
check('the connect panel offers to copy the station endpoint', copyPanelHtml, 'wss://')
assert('each endpoint has its own copy button, so nothing has to be guessed',
  copyPanelHtml.split('copy-endpoint').length - 1 === 2)
check('a copy button names the endpoint it copies', copyPanelHtml, 'Copy the UDP forwarder endpoint')

// The rejection helpers. These decide what an operator reads when a command
// fails, and which failures are blamed on the device rather than on us.
const rejected = new CommandRejected(502, 'gateway_unreachable', 'The network server could not be reached.')
assert('a rejection shows the network server’s own reason',
  failureMessage(rejected, 'fallback') === 'The network server could not be reached.')
assert('a rejection with no detail falls back to its code, not to a generic string',
  failureMessage(new CommandRejected(409, 'already_queued', ''), 'fallback') === 'already_queued')
assert('a fetch that never got a response keeps its own message',
  failureMessage(new Error('Failed to fetch'), 'fallback') === 'Failed to fetch')
assert('something that is not an error still produces readable text',
  failureMessage('boom', 'The request failed.') === 'The request failed.')
assert('404 reads as “nothing here”, not as a broken server',
  isNotFound(new CommandRejected(404, 'no_session', '')) && !isNotFound(rejected))
assert('502 and up read as our server being unavailable',
  isServerUnavailable(rejected) && isServerUnavailable(new CommandRejected(504, 'timeout', '')) &&
  !isServerUnavailable(new CommandRejected(404, 'no_session', '')))
assert('a non-rejection is neither, so a network error is not mislabelled',
  !isNotFound(new Error('Failed to fetch')) && !isServerUnavailable(new Error('Failed to fetch')))

// The views, rendered on their own. App renders these too, but only in its
// initial loading state under renderToString — so these are what prove the
// extraction kept the content rather than just the file names.
const overview: Overview = {
  ...emptyOverview, deviceCount: 4, onlineDevices: 2, attentionDevices: 1, offlineDevices: 1,
  gatewayCount: 1, onlineGateways: 1, uplinks24h: 128, queuedDownlinks: 2,
  sentDownlinks24h: 9, lastDownlink: '4 minutes ago', hourlyUplinks: [1, 0, 3, 7, 2],
}
const controls = {
  query: '', setQuery: noop, filter: 'all' as const, setFilter: noop,
  group: 'all' as const, setGroup: noop, deviceGroups: ['Cooling'],
}

const overviewHtml = renderToString(h(OverviewView, {
  overview, gatewayTotals: gatewaySummary(liveStatus, ['gw1', 'gw-typed-but-dead']),
  status: liveStatus, gateways: [gateway], events: [event], filteredDevices: [device],
  view: 'Overview', setView: noop, onSelectDevice: noop, ...controls,
}))
check('the Overview renders its metric grid', overviewHtml, 'Registered devices')
check('the Overview counts the devices it was given', overviewHtml, '>4<')
check('the Overview names the traffic window rather than a bare number', overviewHtml, 'last 24 hours')
check('the Overview shows the gateway health panel', overviewHtml, 'Gateway health')
check('the Overview keeps the network server’s own identity panel', overviewHtml, 'NetID')
// The gap this closes: the preview rows used to look like the clickable ones on
// the Devices view and do nothing.
check('the Overview preview rows are the selectable kind', overviewHtml, 'row-selectable')

const devicesHtml = renderToString(h(DevicesView, {
  filteredDevices: [device], selected: null, setSelected: noop, refresh: noop,
  view: 'Devices', setView: noop, ...controls,
}))
check('the Devices view renders the table', devicesHtml, 'DEVICE')
check('the Devices view renders the row it was given', devicesHtml, 'Tank 3')
assert('an unselected device does not open a session panel', !devicesHtml.includes('Reading the session'))

const gatewaysHtml = renderToString(h(GatewaysView, {
  status: liveStatus, gateways: [gateway],
  unheardGateways: [{ ...gateway, id: 'gw-dead', name: 'GW-DEAD' }],
}))
check('the Gateways view leads with how to connect one', gatewaysHtml, 'Connect a gateway')
// A gateway the server has heard from but nobody named is still shown, by the
// only identifier there is.
check('the Gateways view shows a live gateway nobody named', gatewaysHtml, 'AA55555555555555')
// And when an inventory row matches it, the card upgrades to the operator's name
// plus the database's aggregates.
const matchedHtml = renderToString(h(GatewaysView, {
  status: liveStatus,
  gateways: [{ ...gateway, id: 'AA55555555555555' }],
  unheardGateways: [],
}))
check('a live gateway with an inventory row is shown by its name', matchedHtml, 'GW-1')
assert('a matched gateway leaves nothing in the unheard section',
  !matchedHtml.includes('Registered, never heard from'))
// The two populations must stay visibly separate; merging them is what makes
// "I registered three and see one" unanswerable.
check('the Gateways view separates inventory rows that never spoke', gatewaysHtml, 'Registered, never heard from')
check('the Gateways view names the unheard row rather than hiding it', gatewaysHtml, 'GW-DEAD')

const eventsHtml = renderToString(h(EventsView, { events: [event] }))
check('the Events view renders the feed', eventsHtml, 'Device joined')
check('the Events view says what the feed is', eventsHtml, 'Network events')
assert('an empty event list says so rather than rendering a blank panel',
  renderToString(h(EventsView, { events: [] })).includes('No network events recorded.'))

let failed = 0
for (const [name, ok] of checks) {
  if (!ok) failed++
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}`)
}
console.log(failed === 0 ? `\nall ${checks.length} checks passed` : `\n${failed} of ${checks.length} failed`)
process.exit(failed === 0 ? 0 : 1)
