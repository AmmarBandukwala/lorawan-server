import { useEffect, useMemo, useState } from 'react'
import {
  Activity, ArrowDown, ArrowUpRight, Bell, Boxes, Check, ChevronDown,
  Menu, Plus, Radio, RadioTower, RefreshCw, Settings2, TriangleAlert, X,
} from 'lucide-react'
import { loadNetworkSnapshot, registerDevice, saveFacilitySettings } from './features/network/api'
import { ActivityItem, DeviceHeader, DeviceTable, Gateway, GatewayCard, SettingsForm } from './features/network/components'
import { emptyOverview, emptySettings, type ConnectionState, type Device, type FacilitySettings, type GatewayRecord, type NetworkEvent, type Overview, type View } from './features/network/types'
import './App.css'

const navigation: { name: View; icon: typeof Activity }[] = [
  { name: 'Overview', icon: Activity }, { name: 'Devices', icon: Boxes },
  { name: 'Gateways', icon: RadioTower }, { name: 'Events', icon: Radio },
]

function App() {
  const [view, setView] = useState<View>('Overview')
  const [devices, setDevices] = useState<Device[]>([])
  const [overview, setOverview] = useState<Overview>(emptyOverview)
  const [gateways, setGateways] = useState<GatewayRecord[]>([])
  const [events, setEvents] = useState<NetworkEvent[]>([])
  const [settings, setSettings] = useState<FacilitySettings>(emptySettings)
  const [settingsDraft, setSettingsDraft] = useState<FacilitySettings>(emptySettings)
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState('All devices')
  const [showAdd, setShowAdd] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const [connection, setConnection] = useState<ConnectionState>('loading')
  const [loading, setLoading] = useState(false)
  const [newName, setNewName] = useState('')
  const [newEui, setNewEui] = useState('')
  const [registrationError, setRegistrationError] = useState('')
  const [settingsError, setSettingsError] = useState('')
  const [settingsMessage, setSettingsMessage] = useState('')
  const [settingsSaving, setSettingsSaving] = useState(false)

  async function refreshNetworkSnapshot() {
    setLoading(true)
    try {
      const snapshot = await loadNetworkSnapshot()
      setDevices(snapshot.devices)
      setOverview(snapshot.overview)
      setGateways(snapshot.gateways)
      setEvents(snapshot.events)
      setSettings(snapshot.settings)
      setSettingsDraft(snapshot.settings)
      setConnection('connected')
    } catch {
      setDevices([])
      setOverview(emptyOverview)
      setGateways([])
      setEvents([])
      setConnection('offline')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    let active = true
    void loadNetworkSnapshot()
      .then((snapshot) => {
        if (active) {
          setDevices(snapshot.devices)
          setOverview(snapshot.overview)
          setGateways(snapshot.gateways)
          setEvents(snapshot.events)
          setSettings(snapshot.settings)
          setSettingsDraft(snapshot.settings)
          setConnection('connected')
        }
      })
      .catch(() => {
        if (active) setConnection('offline')
      })
    return () => { active = false }
  }, [])

  const filteredDevices = useMemo(() => devices.filter((device) => {
    const matchesQuery = `${device.name} ${device.eui} ${device.group}`.toLowerCase().includes(query.toLowerCase())
    const matchesState = filter === 'All devices' || device.state === filter
    return matchesQuery && matchesState
  }), [devices, filter, query])

  async function addDevice(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (connection !== 'connected') {
      setRegistrationError('Connect to the database API before registering a device.')
      return
    }
    const formattedEui = newEui.replace(/[^a-fA-F0-9]/g, '').toUpperCase()
    if (formattedEui.length !== 16) {
      setRegistrationError('DevEUI must contain 16 hexadecimal characters.')
      return
    }
    try {
      const saved = await registerDevice(newName, formattedEui)
      setDevices((current) => [saved, ...current])
    } catch (error) {
      setRegistrationError(error instanceof Error ? error.message : 'Device could not be saved.')
      return
    }
    setNewName('')
    setNewEui('')
    setRegistrationError('')
    setShowAdd(false)
    setView('Devices')
    void refreshNetworkSnapshot()
  }

  async function saveSettings(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSettingsError('')
    setSettingsMessage('')
    if (connection !== 'connected') {
      setSettingsError('Connect to the database API before saving facility settings.')
      return
    }
    setSettingsSaving(true)
    try {
      const saved = await saveFacilitySettings(settingsDraft)
      setSettings(saved)
      setSettingsDraft(saved)
      setSettingsMessage('Facility settings saved.')
    } catch (error) {
      setSettingsError(error instanceof Error ? error.message : 'Settings could not be saved.')
    } finally {
      setSettingsSaving(false)
    }
  }

  const onlineCount = overview.onlineDevices
  const attentionCount = overview.attentionDevices
  const maxHourlyTraffic = Math.max(...overview.hourlyUplinks, 0)
  const title = view === 'Overview' ? 'Network overview' : view
  const localTimestamp = new Date().toLocaleString('en-GB', { dateStyle: 'full', timeStyle: 'short' }).replace(' at ', ' · ').toUpperCase()
  const facilityName = settings.configured ? settings.facilityName : 'Facility not configured'
  const operatorInitials = settings.operatorName.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]).join('').toUpperCase()

  return (
    <div className="app-shell">
      <aside className={`sidebar ${menuOpen ? 'sidebar-open' : ''}`}>
        <div className="brand"><div className="brand-mark"><RadioTower size={19} strokeWidth={2.2} /></div><div><strong>FIELDNODE</strong><span>NETWORK SERVER</span></div></div>
        <div className="site-select"><span className="site-dot" /><span><b>{facilityName}</b><small>{settings.configured ? settings.region : 'Region not configured'}</small></span><ChevronDown size={15} /></div>
        <div className="nav-label">NETWORK</div>
        <nav aria-label="Main navigation">
          {navigation.map(({ name, icon: Icon }) => <button key={name} className={`nav-item ${view === name ? 'active' : ''}`} onClick={() => { setView(name); setMenuOpen(false) }}><Icon size={17} /><span>{name}</span>{name === 'Devices' && <span className="nav-count">{devices.length}</span>}</button>)}
        </nav>
        <div className="sidebar-bottom">
          <div className="server-card"><span className={`pulse-dot ${connection === 'offline' ? 'disconnected' : ''}`} /><div><b>{connection === 'connected' ? 'Database connected' : connection === 'loading' ? 'Connecting' : 'Database unavailable'}</b><small>{connection === 'connected' ? 'PostgreSQL API' : connection === 'loading' ? 'Loading facility data' : 'Live data unavailable'}</small></div><ChevronDown size={14} /></div>
          <button className={`nav-item settings-item ${view === 'Settings' ? 'active' : ''}`} onClick={() => { setView('Settings'); setMenuOpen(false) }}><Settings2 size={17} /><span>Settings</span></button>
          <div className="user-row"><div className="avatar">{operatorInitials || '—'}</div><div><b>{settings.operatorName || 'Operator not configured'}</b><small>{settings.operatorRole || 'No role configured'}</small></div><ChevronDown size={15} /></div>
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
            <div><div className="eyebrow">{localTimestamp} <span>·</span> LOCAL</div><h1>{title}</h1><p>{view === 'Overview' ? 'A clear view of what is happening across your facility.' : view === 'Settings' ? 'Facility profile and operator display settings.' : `${view} connected to your local network server.`}</p></div>
            <div className="heading-actions"><button className="button button-secondary" onClick={() => void refreshNetworkSnapshot()}><RefreshCw size={15} className={loading ? 'spinning' : ''} />Refresh</button>{view !== 'Settings' && <button className="button button-primary" onClick={() => setShowAdd(true)} disabled={connection !== 'connected'}><Plus size={16} />Add device</button>}</div>
          </div>

          {connection !== 'connected' && <div className="demo-notice"><span className="notice-dot" />{connection === 'loading' ? 'Connecting to the database API…' : 'Database API unavailable · no local sample data is shown'}</div>}
          {connection === 'connected' && !settings.configured && <div className="demo-notice"><span className="notice-dot" />Facility profile is not configured. Add its details in Settings.</div>}

          {view === 'Overview' && <>
            <section className="metric-grid" aria-label="Network summary">
              <article className="metric-card"><div className="metric-top"><span>Registered devices</span><Boxes size={16} /></div><div className="metric-value">{overview.deviceCount}<span className="metric-unit"> devices</span></div><div className="metric-foot">Persisted device inventory</div></article>
              <article className="metric-card"><div className="metric-top"><span>Online now</span><span className={`status-indicator ${onlineCount ? 'green' : ''}`} /></div><div className="metric-value">{onlineCount}<span className="metric-unit"> / {overview.deviceCount}</span></div><div className="metric-foot"><span className="availability-bar"><i style={{ width: `${overview.deviceCount ? onlineCount / overview.deviceCount * 100 : 0}%` }} /></span><span>{overview.deviceCount ? Math.round(onlineCount / overview.deviceCount * 100) : 0}% availability</span></div></article>
              <article className="metric-card"><div className="metric-top"><span>Needs attention</span><TriangleAlert size={16} className="warning-icon" /></div><div className="metric-value">{attentionCount + overview.offlineDevices}<span className="metric-unit"> devices</span></div><div className="metric-foot"><span className="metric-warning">{attentionCount} low battery</span><span>·</span><span>{overview.offlineDevices} offline</span></div></article>
              <article className="metric-card gateway-metric"><div className="metric-top"><span>Gateways</span><RadioTower size={16} /></div><div className="metric-value">{overview.onlineGateways}<span className="metric-unit"> / {overview.gatewayCount}</span></div><div className="metric-foot"><span className={`status-indicator ${overview.onlineGateways ? 'green' : ''}`} />{overview.gatewayCount ? `${overview.onlineGateways} reporting recently` : 'No gateways registered'}</div></article>
            </section>

            <section className="middle-grid">
              <article className="panel traffic-panel"><div className="panel-heading"><div><h2>Message traffic</h2><p>Uplinks received in the last 24 hours</p></div><span className="select-button">Last 24 hours</span></div><div className="traffic-summary"><strong>{overview.uplinks24h.toLocaleString()}</strong><small>received uplinks</small></div><div className="chart-wrap"><div className="chart"><div className="chart-grid"><i /><i /><i /><i /><i /></div><div className="chart-bars" role="img" aria-label={`${overview.uplinks24h} uplinks received in the last 24 hours`}>{overview.hourlyUplinks.map((count, index) => <i key={index} title={`${count} uplinks`} style={{ height: maxHourlyTraffic ? `${Math.max(3, count / maxHourlyTraffic * 100)}%` : '0%' }} />)}</div><div className="chart-x-labels"><span>24h ago</span><span>18h</span><span>12h</span><span>6h</span><span>Now</span></div></div></div><div className="chart-legend"><span><i className="legend-dot" />Received uplinks</span></div></article>

              <article className="panel gateway-panel"><div className="panel-heading"><div><h2>Gateway health</h2><p>{overview.onlineGateways} online · {overview.gatewayCount} registered</p></div><button className="text-action" onClick={() => setView('Gateways')}>View all <ArrowUpRight size={14} /></button></div><div className="gateway-list">{gateways.slice(0, 3).map((gateway) => <Gateway key={gateway.id} gateway={gateway} />)}{gateways.length === 0 && <div className="empty-result">No gateways registered yet.</div>}</div><div className="gateway-footer"><span>Latest reception</span><b>{gateways[0]?.lastSeen ?? 'No radio traffic'}</b></div></article>
            </section>

            <section className="panel device-panel"><DeviceHeader view={view} setView={setView} query={query} setQuery={setQuery} filter={filter} setFilter={setFilter} /><DeviceTable devices={filteredDevices.slice(0, 5)} /></section>

            <section className="bottom-grid"><article className="panel activity-panel"><div className="panel-heading"><div><h2>Recent activity</h2><p>Latest network events</p></div><button className="text-action" onClick={() => setView('Events')}>All events <ArrowUpRight size={14} /></button></div><div className="activity-list">{events.slice(0, 3).map((event) => <ActivityItem key={event.id} event={event} />)}{events.length === 0 && <div className="empty-result">No network events recorded.</div>}</div></article><article className="panel downlink-panel"><div className="panel-heading"><div><h2>Downlink queue</h2><p>Scheduled device messages</p></div><ArrowDown size={16} className="muted-icon" /></div><div className="queue-empty"><div className="queue-icon"><Check size={18} /></div><b>{overview.queuedDownlinks === 0 ? 'Queue is clear' : `${overview.queuedDownlinks} messages queued`}</b><span>{overview.queuedDownlinks === 0 ? 'No pending downlinks' : 'Waiting for an eligible receive window'}</span></div><div className="queue-footer"><span>Last sent</span><b>{overview.lastDownlink} <span>·</span> {overview.sentDownlinks24h} in 24h</b></div></article></section>
          </>}

          {view === 'Devices' && <section className="panel device-panel full-device-panel"><DeviceHeader view={view} setView={setView} query={query} setQuery={setQuery} filter={filter} setFilter={setFilter} /><DeviceTable devices={filteredDevices} /></section>}
          {view === 'Gateways' && <section className="gateway-grid-full">{gateways.map((gateway) => <GatewayCard key={gateway.id} gateway={gateway} />)}{gateways.length === 0 && <div className="panel empty-panel">No gateways registered. Gateway registration and Basic Station connectivity are not implemented yet.</div>}</section>}
          {view === 'Events' && <section className="panel event-view"><div className="panel-heading"><div><h2>Network events</h2><p>Recent activity from your local network</p></div><span className="select-button">Most recent</span></div><div className="activity-list event-list">{events.map((event) => <ActivityItem key={event.id} event={event} />)}{events.length === 0 && <div className="empty-result">No network events recorded.</div>}</div></section>}
          {view === 'Settings' && <SettingsForm draft={settingsDraft} setDraft={setSettingsDraft} onSubmit={saveSettings} saving={settingsSaving} error={settingsError} message={settingsMessage} disabled={connection !== 'connected'} />}

          <footer className="page-footer"><span>FIELDNODE NETWORK SERVER</span><span><span className={`status-indicator ${connection === 'connected' ? 'green' : ''}`} />{connection === 'connected' ? 'Inventory API connected' : connection === 'loading' ? 'Connecting to API' : 'API disconnected'}</span></footer>
        </div>
      </main>

      {showAdd && <div className="modal-backdrop" onClick={() => setShowAdd(false)}><section className="modal" role="dialog" aria-modal="true" aria-labelledby="modal-title" onClick={(event) => event.stopPropagation()}><div className="modal-heading"><div><span className="modal-icon"><Plus size={18} /></span><div><h2 id="modal-title">Register a device</h2><p>Add an end device to your inventory</p></div></div><button className="icon-button" aria-label="Close dialog" onClick={() => setShowAdd(false)}><X size={18} /></button></div><form onSubmit={addDevice}><label>Device name<input autoFocus required value={newName} onChange={(event) => setNewName(event.target.value)} placeholder="Enter device name" /></label><label>DevEUI <span>16 hexadecimal characters</span><input required minLength={16} maxLength={23} value={newEui} onChange={(event) => setNewEui(event.target.value)} placeholder="Enter DevEUI" /></label>{registrationError && <p className="form-error" role="alert">{registrationError}</p>}<div className="form-note"><TriangleAlert size={15} />This only adds an inventory record. Activation credentials and join settings must be configured before a device can join.</div><div className="modal-actions"><button type="button" className="button button-secondary" onClick={() => setShowAdd(false)}>Cancel</button><button type="submit" className="button button-primary"><Plus size={15} />Register device</button></div></form></section></div>}
    </div>
  )
}

export default App
