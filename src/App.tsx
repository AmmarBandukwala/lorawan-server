import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity, ArrowDown, ArrowUpRight, Bell, Boxes, Braces, Check, ChevronDown,
  Menu, Radio, RadioTower, RefreshCw, Settings2, TriangleAlert,
} from 'lucide-react'
import { emptySnapshot, loadNetworkSnapshot, sameSettings, saveFacilitySettings, sectionKeys, type NetworkSnapshot } from './features/network/api'
import { ActivityItem, DeviceHeader, DeviceTable, Gateway, GatewayCard, PageHeadingAction, SettingsForm } from './features/network/components'
import { DeviceConsole, DutyCyclePanel } from './features/network/DeviceConsole'
import { GatewayConnectPanel, LiveGatewayCard, UnheardGatewayRow } from './features/network/Gateways'
import { DeviceModels } from './features/network/DeviceModels'
import { NetworkServerPanel } from './features/network/NetworkHealth'
import { fetchRegions, fetchStatus, gatewaySummary, type NetworkStatus, type RegionList } from './features/network/control'
import { RegisterDeviceDialog } from './features/network/RegisterDeviceDialog'
import { FALLBACK_REGIONS, REFRESH_INTERVAL_MS, UNASSIGNED_GROUP } from './features/network/constants'
import { emptySettings, type ConnectionState, type Device, type DeviceFilter, type FacilitySettings, type GroupFilter, type View } from './features/network/types'
import './App.css'

const navigation: { name: View; icon: typeof Activity }[] = [
  { name: 'Overview', icon: Activity }, { name: 'Devices', icon: Boxes },
  { name: 'Gateways', icon: RadioTower }, { name: 'Models', icon: Braces },
  { name: 'Events', icon: Radio },
]

function App() {
  const [view, setView] = useState<View>('Overview')
  // One snapshot instead of five states. The panels are refreshed as a unit, and
  // holding them separately is what let the mount effect and the manual refresh
  // grow two different sets of setters that could drift apart.
  const [snapshot, setSnapshot] = useState<NetworkSnapshot>(emptySnapshot)
  const { devices, overview, gateways, events, settings } = snapshot
  const [settingsDraft, setSettingsDraft] = useState<FacilitySettings>(emptySettings)
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<DeviceFilter>('all')
  // 'all' means no group filter; any other value is a group label to match.
  const [group, setGroup] = useState<GroupFilter>('all')
  const [showAdd, setShowAdd] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const [connection, setConnection] = useState<ConnectionState>('loading')
  const [loading, setLoading] = useState(false)
  const [settingsError, setSettingsError] = useState('')
  const [settingsMessage, setSettingsMessage] = useState('')
  const [settingsSaving, setSettingsSaving] = useState(false)

  // The device whose live session is open, and the network server's own health
  // report. Neither comes from the snapshot: the session is per-device state the
  // list endpoint does not carry, and the health report is process state — which
  // gateways are still sending PULL_DATA, how many webhook deliveries were
  // dropped — that no table holds.
  const [selected, setSelected] = useState<Device | null>(null)
  const [status, setStatus] = useState<NetworkStatus | null>(null)

  // The bands the settings form offers. Fetched once rather than on every poll:
  // the list changes only when the network server is rebuilt, and the form is a
  // rare visit compared with the dashboard's fifteen-second refresh.
  const [regions, setRegions] = useState<RegionList | null>(null)

  // The newest snapshot, for merging a refresh into it. It is written only
  // where the snapshot changes — in the refresh and in patchSnapshot — so it
  // never needs syncing from state, and reading it does not depend on when
  // React commits.
  const snapshotRef = useRef<NetworkSnapshot>(emptySnapshot)
  const abortRef = useRef<AbortController | null>(null)

  const refreshNetworkSnapshot = useCallback(async () => {
    // A refresh started while an earlier one is still in flight makes that
    // result stale; cancelling it stops the panels flickering backwards.
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    try {
      const previous = snapshotRef.current
      const next = await loadNetworkSnapshot(previous, controller.signal)
      if (controller.signal.aborted) return
      snapshotRef.current = next
      setSnapshot(next)
      // Adopt the fetched settings only while the draft is untouched. A
      // background refresh that overwrote the draft would silently discard what
      // the operator is typing.
      setSettingsDraft((draft) => (sameSettings(draft, previous.settings) ? next.settings : draft))
      setConnection(next.failed.length === 0 ? 'connected'
        : next.failed.length === sectionKeys.length ? 'offline' : 'degraded')

      // Best-effort and deliberately outside the snapshot: the health report
      // comes from the network server, and one that is not answering must not
      // degrade the panels that did load. The badge in the gateways view says
      // when it is missing.
      const health = await fetchStatus().catch(() => null)
      if (!controller.signal.aborted) setStatus(health)
    } catch {
      // Per-section failures are reported in the snapshot rather than thrown, so
      // reaching here means the refresh was superseded or the page went away.
      if (!controller.signal.aborted) setConnection('offline')
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [])

  // Asks the network server which bands it implements. A failure is not an
  // error state: the form falls back to the console's own list and says so,
  // because an operator should still be able to finish a facility profile while
  // the network server is restarting.
  useEffect(() => {
    let active = true
    fetchRegions()
      .then((list) => { if (active) setRegions(list) })
      .catch(() => { if (active) setRegions(null) })
    return () => { active = false }
  }, [])

  // Keeps the ref and the state in step. A refresh merges into snapshotRef, so a
  // local optimistic update that touched only the state would be quietly undone
  // by the next poll.
  function patchSnapshot(patch: Partial<NetworkSnapshot>) {
    const next = { ...snapshotRef.current, ...patch }
    snapshotRef.current = next
    setSnapshot(next)
  }

  useEffect(() => {
    // Deferred by a task rather than called inline so the effect body does not
    // set state synchronously; the first paint still shows the loading state.
    const first = setTimeout(() => void refreshNetworkSnapshot(), 0)
    const timer = setInterval(() => {
      // A hidden tab does not need fresh data, and a dashboard left open for a
      // week should not quietly turn into a load generator.
      if (!document.hidden) void refreshNetworkSnapshot()
    }, REFRESH_INTERVAL_MS)
    return () => {
      clearTimeout(first)
      clearInterval(timer)
      abortRef.current?.abort()
    }
  }, [refreshNetworkSnapshot])

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
    patchSnapshot({ devices: [device, ...snapshotRef.current.devices] })
    setShowAdd(false)
    setView('Devices')
    void refreshNetworkSnapshot()
  }

  async function saveSettings(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSettingsError('')
    setSettingsMessage('')
    if (connection === 'offline' || connection === 'loading') {
      setSettingsError('Connect to the database API before saving facility settings.')
      return
    }
    setSettingsSaving(true)
    try {
      const saved = await saveFacilitySettings(settingsDraft)
      patchSnapshot({ settings: saved })
      setSettingsDraft(saved)
      setSettingsMessage('Facility settings saved.')
    } catch (error) {
      setSettingsError(error instanceof Error ? error.message : 'Settings could not be saved.')
    } finally {
      setSettingsSaving(false)
    }
  }

  const onlineCount = overview.onlineDevices
  // Naming what failed is the difference between a banner an operator reads and
  // one they learn to ignore: "API unavailable" reads the same whether the
  // database is down or a single route has a typo in it.
  const failedLabel = snapshot.failed.length > 0
    ? `Unavailable: ${snapshot.failed.join(', ')}`
    : 'Live data unavailable'
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
          <div className="server-card"><span className={`pulse-dot ${connection === 'offline' || connection === 'degraded' ? 'disconnected' : ''}`} /><div><b>{connection === 'connected' ? 'Database connected' : connection === 'loading' ? 'Connecting' : connection === 'degraded' ? 'Partially connected' : 'Database unavailable'}</b><small>{connection === 'connected' ? 'PostgreSQL API' : connection === 'loading' ? 'Loading facility data' : failedLabel}</small></div><ChevronDown size={14} /></div>
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
            <div><div className="eyebrow">{localTimestamp} <span>·</span> LOCAL</div><h1>{title}</h1><p>{view === 'Overview' ? 'A clear view of what is happening across your facility.' : view === 'Settings' ? 'Facility profile and operator display settings.' : view === 'Models' ? 'Sensor types, and how their payloads are turned into readings.' : `${view} connected to your local network server.`}</p></div>
            <div className="heading-actions"><button className="button button-secondary" onClick={() => void refreshNetworkSnapshot()}><RefreshCw size={15} className={loading ? 'spinning' : ''} />Refresh</button><PageHeadingAction view={view} canProvision={connection === 'connected'} onAddDevice={() => setShowAdd(true)} /></div>
          </div>

          {connection !== 'connected' && connection !== 'degraded' && <div className="demo-notice"><span className="notice-dot" />{connection === 'loading' ? 'Connecting to the database API…' : 'Database API unavailable · no local sample data is shown'}</div>}
          {connection === 'degraded' && <div className="demo-notice"><span className="notice-dot" />{`Partially connected · ${failedLabel} · showing the last values received`}</div>}
          {connection === 'connected' && !settings.configured && <div className="demo-notice"><span className="notice-dot" />Facility profile is not configured. Add its details in Settings.</div>}

          {view === 'Overview' && <>
            <section className="metric-grid" aria-label="Network summary">
              <article className="metric-card"><div className="metric-top"><span>Registered devices</span><Boxes size={16} /></div><div className="metric-value">{overview.deviceCount}<span className="metric-unit"> devices</span></div><div className="metric-foot">Persisted device inventory</div></article>
              <article className="metric-card"><div className="metric-top"><span>Online now</span><span className={`status-indicator ${onlineCount ? 'green' : ''}`} /></div><div className="metric-value">{onlineCount}<span className="metric-unit"> / {overview.deviceCount}</span></div><div className="metric-foot"><span className="availability-bar"><i style={{ width: `${overview.deviceCount ? onlineCount / overview.deviceCount * 100 : 0}%` }} /></span><span>{overview.deviceCount ? Math.round(onlineCount / overview.deviceCount * 100) : 0}% availability</span></div></article>
              <article className="metric-card"><div className="metric-top"><span>Needs attention</span><TriangleAlert size={16} className="warning-icon" /></div><div className="metric-value">{attentionCount + overview.offlineDevices}<span className="metric-unit"> devices</span></div><div className="metric-foot"><span className="metric-warning">{attentionCount} low battery</span><span>·</span><span>{overview.offlineDevices} offline</span></div></article>
              <article className="metric-card gateway-metric"><div className="metric-top"><span>Gateways</span><RadioTower size={16} /></div><div className="metric-value">{gatewayTotals.online}<span className="metric-unit"> / {gatewayTotals.total}</span></div><div className="metric-foot"><span className={`status-indicator ${gatewayTotals.online ? 'green' : ''}`} />{gatewayTotals.total === 0 ? 'No gateways known' : gatewayTotals.source === 'network-server' ? `${gatewayTotals.online} reporting to the network server` : `${gatewayTotals.total} registered, presence unknown`}</div></article>
            </section>

            <section className="middle-grid">
              <article className="panel traffic-panel"><div className="panel-heading"><div><h2>Message traffic</h2><p>Uplinks received in the last 24 hours</p></div><span className="select-button">Last 24 hours</span></div><div className="traffic-summary"><strong>{overview.uplinks24h.toLocaleString()}</strong><small>received uplinks</small></div><div className="chart-wrap"><div className="chart"><div className="chart-grid"><i /><i /><i /><i /><i /></div><div className="chart-bars" role="img" aria-label={`${overview.uplinks24h} uplinks received in the last 24 hours`}>{overview.hourlyUplinks.map((count, index) => <i key={index} title={`${count} uplinks`} style={{ height: maxHourlyTraffic ? `${Math.max(3, count / maxHourlyTraffic * 100)}%` : '0%' }} />)}</div><div className="chart-x-labels"><span>24h ago</span><span>18h</span><span>12h</span><span>6h</span><span>Now</span></div></div></div><div className="chart-legend"><span><i className="legend-dot" />Received uplinks</span></div></article>

              <article className="panel gateway-panel"><div className="panel-heading"><div><h2>Gateway health</h2><p>{gatewayTotals.online} online · {gatewayTotals.total} known{gatewayTotals.source === 'network-server' ? '' : ' (presence unknown)'}</p></div><button className="text-action" onClick={() => setView('Gateways')}>View all <ArrowUpRight size={14} /></button></div><div className="gateway-list">{gateways.slice(0, 3).map((gateway) => <Gateway key={gateway.id} gateway={gateway} />)}{gateways.length === 0 && <div className="empty-result">No gateways registered yet.</div>}</div><div className="gateway-footer"><span>Latest reception</span><b>{gateways[0]?.lastSeen ?? 'No radio traffic'}</b></div></article>
            </section>

            {/* Identity and liveness of the process itself. It sits here rather
                than in Settings because these are read-only facts an operator
                needs while looking at traffic — the NetID to write into a device,
                and whether this server has been up long enough to trust its
                counters. */}
            <NetworkServerPanel status={status} inventoryIds={gateways.map((record) => record.id)} />

            <section className="panel device-panel"><DeviceHeader view={view} setView={setView} query={query} setQuery={setQuery} filter={filter} setFilter={setFilter} group={group} setGroup={setGroup} groups={deviceGroups} /><DeviceTable devices={filteredDevices.slice(0, 5)} /></section>

            <section className="bottom-grid"><article className="panel activity-panel"><div className="panel-heading"><div><h2>Recent activity</h2><p>Latest network events</p></div><button className="text-action" onClick={() => setView('Events')}>All events <ArrowUpRight size={14} /></button></div><div className="activity-list">{events.slice(0, 3).map((event) => <ActivityItem key={event.id} event={event} />)}{events.length === 0 && <div className="empty-result">No network events recorded.</div>}</div></article><article className="panel downlink-panel"><div className="panel-heading"><div><h2>Downlink queue</h2><p>Scheduled device messages</p></div><ArrowDown size={16} className="muted-icon" /></div><div className="queue-empty"><div className="queue-icon"><Check size={18} /></div><b>{overview.queuedDownlinks === 0 ? 'Queue is clear' : `${overview.queuedDownlinks} messages queued`}</b><span>{overview.queuedDownlinks === 0 ? 'No pending downlinks' : 'Waiting for an eligible receive window'}</span></div><div className="queue-footer"><span>Last sent</span><b>{overview.lastDownlink} <span>·</span> {overview.sentDownlinks24h} in 24h</b></div></article></section>
          </>}

          {view === 'Devices' && <div className="device-view"><section className="panel device-panel full-device-panel"><DeviceHeader view={view} setView={setView} query={query} setQuery={setQuery} filter={filter} setFilter={setFilter} group={group} setGroup={setGroup} groups={deviceGroups} /><DeviceTable devices={filteredDevices} onSelect={setSelected} /></section>{selected && <DeviceConsole device={selected} onClose={() => setSelected(null)} onChanged={() => void refreshNetworkSnapshot()} onDeleted={() => { setSelected(null); void refreshNetworkSnapshot() }} />}</div>}
          {view === 'Gateways' && <div className="gateway-view">
            {/* First on the page because it is what someone with a gateway in
                their hands needs, and it was the one thing this view did not
                have: gateways are not registered here, they arrive, so the
                useful prompt is the endpoint to point them at. */}
            <GatewayConnectPanel listeners={status?.listeners} transports={status?.transports} />

            <section className="gateway-grid-full">
              {/* Live presence leads. The network server is what hears the
                  packets, so a gateway it has heard from exists regardless of
                  whether anyone typed it into an inventory. */}
              {(status?.gateways ?? []).map((health) => {
                const record = gateways.find((g) => g.id.toUpperCase() === health.gateway_eui.toUpperCase())
                // An inventory row adds the database's own aggregates, so the
                // richer card is used when both views have something to say.
                return record
                  ? <GatewayCard key={health.gateway_eui} gateway={record} health={health} />
                  : <LiveGatewayCard key={health.gateway_eui} health={health} region={status?.region ?? 'band unknown'} />
              })}
              {(status?.gateways ?? []).length === 0 && <div className="panel empty-panel">
                {status
                  ? 'No gateway has contacted this server yet. Configure one with the endpoint above — it appears here within about 30 seconds of its first packet.'
                  : 'The network server is not reachable, so gateway presence is unknown. This is not the same as having no gateways.'}
              </div>}
            </section>

            {unheardGateways.length > 0 && <section className="panel unheard-panel">
              <div className="panel-heading"><div><h2>Registered, never heard from</h2>
                <p>Inventory rows with no packets behind them</p></div></div>
              {unheardGateways.map((gateway) => <UnheardGatewayRow key={gateway.id} name={gateway.name} id={gateway.id} onOpen={() => window.scrollTo({ top: 0, behavior: 'smooth' })} />)}
            </section>}

            {status?.duty_cycle && <DutyCyclePanel usage={status.duty_cycle.usage} window={status.duty_cycle.window} denials={status.duty_cycle.denials} />}
            {status && status.integrations && <article className="panel integration-panel"><div className="panel-heading"><div><h2>Integrations</h2><p>{status.integrations.sinks.join(', ') || 'No sinks configured'}</p></div></div><div className="gateway-stats"><div><span>Delivered</span><b>{status.integrations.published.toLocaleString()}</b></div><div><span>Failed</span><b>{status.integrations.delivery_failures.toLocaleString()}</b></div><div><span>Dropped</span><b>{status.integrations.dropped.toLocaleString()}</b></div><div><span>In queue</span><b>{status.integrations.queued.toLocaleString()}</b></div></div>{status.integrations.dropped > 0 && <p className="form-error" role="alert">Events are being dropped: the queue is full, so a receiver is too slow or unreachable.</p>}</article>}
            {!status && <div className="panel integration-panel"><b>No health report from the network server.</b><span> Gateway presence, duty-cycle occupancy and integration counters need the network server reachable.</span></div>}
          </div>}
          {view === 'Models' && <DeviceModels decoding={status?.decoding} />}
          {view === 'Events' && <section className="panel event-view"><div className="panel-heading"><div><h2>Network events</h2><p>Recent activity from your local network</p></div><span className="select-button">Most recent</span></div><div className="activity-list event-list">{events.map((event) => <ActivityItem key={event.id} event={event} />)}{events.length === 0 && <div className="empty-result">No network events recorded.</div>}</div></section>}
          {view === 'Settings' && <SettingsForm draft={settingsDraft} setDraft={setSettingsDraft} onSubmit={saveSettings} saving={settingsSaving} error={settingsError} message={settingsMessage} disabled={connection === 'offline' || connection === 'loading'} regions={regions?.regions ?? FALLBACK_REGIONS} regionsAreFallback={regions === null || regions.source === 'fallback'} />}

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
