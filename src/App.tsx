import { useMemo, useState } from 'react'
import {
  Activity, ArrowLeftRight, Bell, Bot, Boxes, Braces, Menu, Radio, RadioTower, RefreshCw,
  Settings2, Sparkles,
} from 'lucide-react'
import { PageHeadingAction, SettingsForm } from './features/network/components'
import { Agents } from './features/network/panels/Agents'
import { Assistant } from './features/network/panels/Assistant'
import { DeviceModels } from './features/network/panels/DeviceModels'
import { Integrations } from './features/network/panels/Integrations'
import { RegisterDeviceDialog } from './features/network/panels/RegisterDeviceDialog'
import { gatewaySummary } from './features/network/control/status'
import { UNASSIGNED_GROUP } from './features/network/constants'
import type { ConnectionState, Device, DeviceFilter, GroupFilter, View } from './features/network/types'
import { OverviewView } from './features/network/views/OverviewView'
import { DevicesView } from './features/network/views/DevicesView'
import { GatewaysView } from './features/network/views/GatewaysView'
import { EventsView } from './features/network/views/EventsView'
import { useNetworkSnapshot } from './features/network/hooks/useNetworkSnapshot'
import { useRegions } from './features/network/hooks/useRegions'
import { useFacilitySettings } from './features/network/hooks/useFacilitySettings'
import './App.css'

const navigation: { name: View; icon: typeof Activity }[] = [
  { name: 'Overview', icon: Activity }, { name: 'Devices', icon: Boxes },
  { name: 'Gateways', icon: RadioTower }, { name: 'Models', icon: Braces },
  { name: 'Integrations', icon: ArrowLeftRight },
  // Agents sits with Integrations rather than at the end because it is the same
  // kind of thing — a way out of the network to something the operator runs — and
  // it is the one entry whose page describes a service this console cannot control.
  //
  // Assistant sits beside it because it is the same capability from the other end:
  // the same read-only tools, reached from this page instead of from an agent the
  // operator configured elsewhere. Keeping them adjacent is what makes that
  // relationship visible rather than something to discover.
  { name: 'Assistant', icon: Sparkles },
  { name: 'Agents', icon: Bot },
  { name: 'Events', icon: Radio },
]

function App() {
  const [view, setView] = useState<View>('Overview')

  // The data layer lives in hooks, so what is left here is view state: which
  // page, which filter, which device is open. useNetworkSnapshot owns the poll
  // and which request supersedes which, useRegions the band list, and
  // useFacilitySettings the save round trip with its own error and confirmation.
  const {
    snapshot, connection, loading, status,
    settingsDraft, setSettingsDraft, refresh, patch,
  } = useNetworkSnapshot()
  const { devices, overview, gateways, events, settings } = snapshot
  const regions = useRegions()
  const settingsSave = useFacilitySettings({
    draft: settingsDraft, connection, setDraft: setSettingsDraft, patch,
  })

  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<DeviceFilter>('all')
  // 'all' means no group filter; any other value is a group label to match.
  const [group, setGroup] = useState<GroupFilter>('all')
  const [showAdd, setShowAdd] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)

  // The device whose live session is open. Not part of the snapshot: the session
  // is per-device state the list endpoint does not carry.
  const [selected, setSelected] = useState<Device | null>(null)

  // Devices grouped by the label an operator gave them. Grouping is a filter
  // rather than a tree because a facility's organisation changes — a line is
  // repurposed, a wing is added — and a device should move between groups by
  // editing one field rather than being re-provisioned.
  const deviceGroups = useMemo(() => {
    const names = new Set(devices.map((device) => device.group).filter((name) => name && name !== UNASSIGNED_GROUP))
    return [...names].sort()
  }, [devices])

  const filteredDevices = useMemo(() => devices.filter((device) => {
    const matchesQuery = `${device.name} ${device.eui} ${device.group}`.toLowerCase().includes(query.toLowerCase())
    const matchesState = filter === 'all' || device.state === filter
    const matchesGroup = group === 'all' ||
      (group === UNASSIGNED_GROUP ? !device.group || device.group === UNASSIGNED_GROUP : device.group === group)
    return matchesQuery && matchesState && matchesGroup
  }), [devices, filter, group, query])

  // Inventory rows the network server has never heard from. Kept visible because
  // "I registered three gateways and the list shows one" needs answering, but
  // separated from the live ones so an unused row does not read as a broken one.
  const unheardGateways = useMemo(() => gateways.filter(
    (record) => !(status?.gateways ?? []).some(
      (health) => health.gateway_eui.toUpperCase() === record.id.toUpperCase()),
  ), [gateways, status])

  /**
   * Opens one device's session panel, from any page.
   *
   * The panel only exists on the Devices page, so this has to switch page as
   * well as select. Without the switch, a row clicked on the Overview would look
   * clickable and do nothing.
   */
  function openDevice(device: Device) {
    setSelected(device)
    setView('Devices')
  }

  // The search box and the two dropdowns are shared by the Overview and Devices
  // pages, so their state lives here rather than in either page. That is what
  // lets you type a search on one page and still see it after moving to the
  // other. `deviceGroups` and `groups` are the same list under two names because
  // the two components name the prop differently.
  const deviceControls = {
    query, setQuery, filter, setFilter, group, setGroup, deviceGroups, groups: deviceGroups,
  }

  // How many gateways the Overview should show. This is worked out rather than
  // read straight from either source, because the saved list and the live list
  // measure different things: see gatewaySummary.
  const gatewayTotals = useMemo(
    () => gatewaySummary(status, gateways.map((record) => record.id)),
    [gateways, status],
  )

  /**
   * Runs after the network server accepts a newly registered device.
   *
   * The device is added to the list straight away so the page updates at once
   * instead of waiting for the next poll. The poll then checks that claim: if the
   * device is not really there, it replaces the row just added rather than
   * leaving a device on screen that can never join.
   */
  function deviceRegistered(device: Device) {
    patch({ devices: [device, ...snapshot.devices] })
    setShowAdd(false)
    setView('Devices')
    void refresh()
  }

  // Which requests failed, named. "API unavailable" reads the same whether the
  // database is down or one URL is misspelled, so the banner lists the actual
  // endpoints and an operator can do something about it.
  const failedLabel = snapshot.failed.length > 0
    ? `Unavailable: ${snapshot.failed.join(', ')}`
    : 'Live data unavailable'

  // The words for each connection state, in the three places they appear.
  // Written as lookups rather than nested conditionals because ConnectionState
  // has exactly these four values, so TypeScript checks that every case is
  // covered — a new state would be a compile error here instead of a silent
  // wrong label.
  const connectionTitle: Record<ConnectionState, string> = {
    connected: 'Database connected',
    loading: 'Connecting',
    degraded: 'Partially connected',
    offline: 'Database unavailable',
  }
  const connectionSubtitle: Record<ConnectionState, string> = {
    connected: 'PostgreSQL API',
    loading: 'Loading facility data',
    degraded: failedLabel,
    offline: failedLabel,
  }
  const footerStatus: Record<ConnectionState, string> = {
    connected: 'Inventory API connected',
    loading: 'Connecting to API',
    degraded: failedLabel,
    offline: 'API disconnected',
  }

  // One line of explanation under each page title. Pages not listed here get the
  // generic sentence below.
  const pageDescription: Partial<Record<View, string>> = {
    Overview: 'A clear view of what is happening across your facility.',
    Settings: 'Facility profile and operator display settings.',
    Models: 'Sensor types, and how their payloads are turned into readings.',
    Integrations: 'Push each decoded reading to your own systems as it arrives.',
    // Named rather than left to the generic sentence, because the generic one would
    // claim this page is "connected to your local network server" — and the whole
    // point of this page is that the service it describes is a third process the
    // console can neither configure nor vouch for.
    Agents: 'Let an agent read the network over the Model Context Protocol.',
    // Worded as a reader rather than an oracle, because that is the honest claim: it
    // looks things up through the same read-only tools the Agents page describes, and
    // a sentence promising it "knows your network" is the one this feature has to be
    // careful not to make.
    Assistant: 'Ask why something is happening. It looks the answer up, and can only read.',
  }

  const title = view === 'Overview' ? 'Network overview' : view
  const description = pageDescription[view]
    ?? `${view} connected to your local network server.`

  // Shown in the page heading, formatted the way a local reader expects:
  // "FRIDAY 17 NOVEMBER 2026 · 14:05".
  const localTimestamp = new Date()
    .toLocaleString('en-GB', { dateStyle: 'full', timeStyle: 'short' })
    .replace(' at ', ' · ')
    .toUpperCase()

  const facilityName = settings.configured
    ? settings.facilityName
    : 'Facility not configured'

  // "Ada Lovelace" becomes "AL" for the round avatar. At most two letters; an
  // empty name gives an empty string, which the JSX shows as a dash.
  const operatorInitials = settings.operatorName
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0])
    .join('')
    .toUpperCase()

  return (
    <div className="app-shell">
      {/* The sidebar is off-screen on a phone until the menu button opens it,
          which is what the sidebar-open class does. */}
      <aside className={`sidebar ${menuOpen ? 'sidebar-open' : ''}`}>
        <div className="brand">
          <div className="brand-mark"><RadioTower size={19} strokeWidth={2.2} /></div>
          <div>
            <strong>FIELDNODE</strong>
            <span>NETWORK SERVER</span>
          </div>
        </div>

        <div className="site-select">
          <span className="site-dot" />
          <span>
            <b>{facilityName}</b>
            <small>{settings.configured ? settings.region : 'Region not configured'}</small>
          </span>
        </div>

        <div className="nav-label">NETWORK</div>
        <nav aria-label="Main navigation">
          {navigation.map(({ name, icon: Icon }) => (
            <button
              key={name}
              className={`nav-item ${view === name ? 'active' : ''}`}
              onClick={() => { setView(name); setMenuOpen(false) }}
            >
              <Icon size={17} />
              <span>{name}</span>
              {/* Only the Devices entry shows a count, because it is the list
                  someone needs to know the size of at a glance. */}
              {name === 'Devices' && <span className="nav-count">{devices.length}</span>}
            </button>
          ))}
        </nav>

        <div className="sidebar-bottom">
          <div className="server-card">
            {/* The dot turns red when the database cannot be reached, so the
                colour and the words beside it always agree. */}
            <span className={`pulse-dot ${connection === 'offline' || connection === 'degraded' ? 'disconnected' : ''}`} />
            <div>
              <b>{connectionTitle[connection]}</b>
              <small>{connectionSubtitle[connection]}</small>
            </div>
          </div>

          <button
            className={`nav-item settings-item ${view === 'Settings' ? 'active' : ''}`}
            onClick={() => { setView('Settings'); setMenuOpen(false) }}
          >
            <Settings2 size={17} />
            <span>Settings</span>
          </button>

          <div className="user-row">
            <div className="avatar">{operatorInitials || '—'}</div>
            <div>
              <b>{settings.operatorName || 'Operator not configured'}</b>
              <small>{settings.operatorRole || 'No role configured'}</small>
            </div>
          </div>
        </div>
      </aside>

      <main className="main-area">
        <header className="topbar">
          {/* Only visible on a phone, where the sidebar is hidden. */}
          <button className="icon-button mobile-menu" aria-label="Open navigation" onClick={() => setMenuOpen(!menuOpen)}>
            <Menu size={19} />
          </button>
          <div className="breadcrumbs">
            <span>{facilityName}</span>
            <span className="crumb-divider">/</span>
            <b>{title}</b>
          </div>
          <div className="topbar-actions">
            {/* Reminds the operator that nothing here leaves the building. */}
            <div className="local-badge"><span className="local-indicator" />LOCAL NETWORK</div>
            <button
              className="icon-button alert-button"
              aria-label="View network events"
              onClick={() => setView('Events')}
            >
              <Bell size={18} />
            </button>
          </div>
        </header>

        <div className="page-content">
          <div className="page-heading">
            <div>
              <div className="eyebrow">{localTimestamp} <span>·</span> LOCAL</div>
              <h1>{title}</h1>
              <p>{description}</p>
            </div>
            <div className="heading-actions">
              {/* Reads every endpoint again. The icon spins while that is happening. */}
              <button className="button button-secondary" onClick={() => void refresh()}>
                <RefreshCw size={15} className={loading ? 'spinning' : ''} />
                Refresh
              </button>
              {/* Only pages that can add something show the button, and only when
                  the database can actually be written to. */}
              <PageHeadingAction
                view={view}
                canProvision={connection === 'connected'}
                onAddDevice={() => setShowAdd(true)}
              />
            </div>
          </div>

          {/* One banner, for whichever of these three situations applies. Each
              says what is wrong and what the operator is looking at as a result. */}
          {connection !== 'connected' && connection !== 'degraded' && (
            <div className="demo-notice">
              <span className="notice-dot" />
              {connection === 'loading'
                ? 'Connecting to the database API…'
                : 'Database API unavailable · no local sample data is shown'}
            </div>
          )}
          {connection === 'degraded' && (
            <div className="demo-notice">
              <span className="notice-dot" />
              {`Partially connected · ${failedLabel} · showing the last values received`}
            </div>
          )}
          {connection === 'connected' && !settings.configured && (
            <div className="demo-notice">
              <span className="notice-dot" />
              Facility profile is not configured. Add its details in Settings.
            </div>
          )}

          {/* Only one page is on screen at a time: exactly one of these
              conditions is true, because `view` holds a single value. */}
          {view === 'Overview' && <OverviewView
            overview={overview}
            gatewayTotals={gatewayTotals}
            status={status}
            gateways={gateways}
            events={events}
            filteredDevices={filteredDevices}
            {...deviceControls}
            view={view}
            setView={setView}
            onSelectDevice={openDevice}
          />}

          {view === 'Devices' && <DevicesView
            filteredDevices={filteredDevices}
            selected={selected}
            {...deviceControls}
            view={view}
            setView={setView}
            setSelected={setSelected}
            refresh={() => void refresh()}
          />}
          {view === 'Gateways' && <GatewaysView status={status} gateways={gateways} unheardGateways={unheardGateways} />}
          {view === 'Models' && <DeviceModels decoding={status?.decoding} />}
          {view === 'Integrations' && <Integrations />}
          {view === 'Agents' && <Agents />}
          {view === 'Assistant' && <Assistant />}
          {view === 'Events' && <EventsView events={events} />}
          {view === 'Settings' && <SettingsForm
            draft={settingsDraft}
            setDraft={setSettingsDraft}
            onSubmit={settingsSave.save}
            saving={settingsSave.saving}
            error={settingsSave.error}
            message={settingsSave.message}
            /* Saving cannot work while the database is unreachable, so the form
               is disabled rather than failing when Save is pressed. */
            disabled={connection === 'offline' || connection === 'loading'}
            regions={regions.regions}
            /* True when the server could not be asked for its bands, so the form
               warns that this list might not match what it supports. */
            regionsAreFallback={regions.isFallback}
          />}

          <footer className="page-footer">
            <span>FIELDNODE NETWORK SERVER</span>
            <span>
              <span className={`status-indicator ${connection === 'connected' ? 'green' : ''}`} />
              {footerStatus[connection]}
            </span>
          </footer>
        </div>
      </main>

      {showAdd && (
        <RegisterDeviceDialog
          onClose={() => setShowAdd(false)}
          onRegistered={deviceRegistered}
          unreachable={connection === 'offline' || connection === 'loading'}
        />
      )}
    </div>
  )
}

export default App
