import { useMemo, useState } from 'react'
import {
  Activity, ArrowLeftRight, Bell, Boxes, Braces, Menu, Radio, RadioTower, RefreshCw, Settings2,
} from 'lucide-react'
import { PageHeadingAction, SettingsForm } from './features/network/components'
import { DeviceModels } from './features/network/panels/DeviceModels'
import { Integrations } from './features/network/panels/Integrations'
import { RegisterDeviceDialog } from './features/network/panels/RegisterDeviceDialog'
import { gatewaySummary } from './features/network/control/status'
import { UNASSIGNED_GROUP } from './features/network/constants'
import type { Device, DeviceFilter, GroupFilter, View } from './features/network/types'
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
  { name: 'Integrations', icon: ArrowLeftRight }, { name: 'Events', icon: Radio },
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

  // The gateway count the Overview shows. It is reconciled rather than taken
  // from either source directly, because the inventory and the live presence
  // list disagree by construction — see gatewaySummary.
  /**
   * Open one device's live session from anywhere.
   *
   * The session panel lives on the Devices view, so a row chosen on the Overview
   * has to move there as well as select — otherwise the row looks interactive and
   * does nothing, which is what the preview used to do.
   */
  function openDevice(device: Device) {
    setSelected(device)
    setView('Devices')
  }

  // One object rather than six props repeated on two views: the strip's state
  // lives here precisely so it survives moving between Overview and Devices.
  const deviceControls = {
    query, setQuery, filter, setFilter, group, setGroup, deviceGroups, groups: deviceGroups,
  }

  const gatewayTotals = useMemo(
    () => gatewaySummary(status, gateways.map((record) => record.id)),
    [gateways, status],
  )

  /**
   * What the dialog does after the network server accepts a device.
   *
   * The new device is put into the snapshot immediately so the list reacts
   * without waiting for the next poll, and the refresh that follows is what
   * confirms it: if the device is not really there, the poll replaces the
   * optimistic row rather than leaving a device on screen that cannot join.
   */
  function deviceRegistered(device: Device) {
    patch({ devices: [device, ...snapshot.devices] })
    setShowAdd(false)
    setView('Devices')
    void refresh()
  }

  // Naming what failed is the difference between a banner an operator reads and
  // one they learn to ignore: "API unavailable" reads the same whether the
  // database is down or a single route has a typo in it.
  const failedLabel = snapshot.failed.length > 0
    ? `Unavailable: ${snapshot.failed.join(', ')}`
    : 'Live data unavailable'
  const title = view === 'Overview' ? 'Network overview' : view
  const localTimestamp = new Date().toLocaleString('en-GB', { dateStyle: 'full', timeStyle: 'short' }).replace(' at ', ' · ').toUpperCase()
  const facilityName = settings.configured ? settings.facilityName : 'Facility not configured'
  const operatorInitials = settings.operatorName.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]).join('').toUpperCase()

  return (
    <div className="app-shell">
      <aside className={`sidebar ${menuOpen ? 'sidebar-open' : ''}`}>
        <div className="brand"><div className="brand-mark"><RadioTower size={19} strokeWidth={2.2} /></div><div><strong>FIELDNODE</strong><span>NETWORK SERVER</span></div></div>
        <div className="site-select"><span className="site-dot" /><span><b>{facilityName}</b><small>{settings.configured ? settings.region : 'Region not configured'}</small></span></div>
        <div className="nav-label">NETWORK</div>
        <nav aria-label="Main navigation">
          {navigation.map(({ name, icon: Icon }) => <button key={name} className={`nav-item ${view === name ? 'active' : ''}`} onClick={() => { setView(name); setMenuOpen(false) }}><Icon size={17} /><span>{name}</span>{name === 'Devices' && <span className="nav-count">{devices.length}</span>}</button>)}
        </nav>
        <div className="sidebar-bottom">
          <div className="server-card"><span className={`pulse-dot ${connection === 'offline' || connection === 'degraded' ? 'disconnected' : ''}`} /><div><b>{connection === 'connected' ? 'Database connected' : connection === 'loading' ? 'Connecting' : connection === 'degraded' ? 'Partially connected' : 'Database unavailable'}</b><small>{connection === 'connected' ? 'PostgreSQL API' : connection === 'loading' ? 'Loading facility data' : failedLabel}</small></div></div>
          <button className={`nav-item settings-item ${view === 'Settings' ? 'active' : ''}`} onClick={() => { setView('Settings'); setMenuOpen(false) }}><Settings2 size={17} /><span>Settings</span></button>
          <div className="user-row"><div className="avatar">{operatorInitials || '—'}</div><div><b>{settings.operatorName || 'Operator not configured'}</b><small>{settings.operatorRole || 'No role configured'}</small></div></div>
        </div>
      </aside>

      <main className="main-area">
        <header className="topbar">
          <button className="icon-button mobile-menu" aria-label="Open navigation" onClick={() => setMenuOpen(!menuOpen)}><Menu size={19} /></button>
          <div className="breadcrumbs"><span>{facilityName}</span><span className="crumb-divider">/</span><b>{title}</b></div>
          <div className="topbar-actions"><div className="local-badge"><span className="local-indicator" />LOCAL NETWORK</div><button className="icon-button alert-button" aria-label="View network events" onClick={() => setView('Events')}><Bell size={18} /></button></div>
        </header>

        <div className="page-content">
          <div className="page-heading">
            <div><div className="eyebrow">{localTimestamp} <span>·</span> LOCAL</div><h1>{title}</h1><p>{view === 'Overview' ? 'A clear view of what is happening across your facility.' : view === 'Settings' ? 'Facility profile and operator display settings.' : view === 'Models' ? 'Sensor types, and how their payloads are turned into readings.' : view === 'Integrations' ? 'Push each decoded reading to your own systems as it arrives.' : `${view} connected to your local network server.`}</p></div>
            <div className="heading-actions"><button className="button button-secondary" onClick={() => void refresh()}><RefreshCw size={15} className={loading ? 'spinning' : ''} />Refresh</button><PageHeadingAction view={view} canProvision={connection === 'connected'} onAddDevice={() => setShowAdd(true)} /></div>
          </div>

          {connection !== 'connected' && connection !== 'degraded' && <div className="demo-notice"><span className="notice-dot" />{connection === 'loading' ? 'Connecting to the database API…' : 'Database API unavailable · no local sample data is shown'}</div>}
          {connection === 'degraded' && <div className="demo-notice"><span className="notice-dot" />{`Partially connected · ${failedLabel} · showing the last values received`}</div>}
          {connection === 'connected' && !settings.configured && <div className="demo-notice"><span className="notice-dot" />Facility profile is not configured. Add its details in Settings.</div>}

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
          {view === 'Events' && <EventsView events={events} />}
          {view === 'Settings' && <SettingsForm draft={settingsDraft} setDraft={setSettingsDraft} onSubmit={settingsSave.save} saving={settingsSave.saving} error={settingsSave.error} message={settingsSave.message} disabled={connection === 'offline' || connection === 'loading'} regions={regions.regions} regionsAreFallback={regions.isFallback} />}

          <footer className="page-footer"><span>FIELDNODE NETWORK SERVER</span><span><span className={`status-indicator ${connection === 'connected' ? 'green' : ''}`} />{connection === 'connected' ? 'Inventory API connected' : connection === 'loading' ? 'Connecting to API' : connection === 'degraded' ? failedLabel : 'API disconnected'}</span></footer>
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
