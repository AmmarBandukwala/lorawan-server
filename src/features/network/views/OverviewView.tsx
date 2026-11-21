/**
 * The Overview page.
 *
 * It answers "is my network working" in one screen: how many devices are
 * talking, how many are not, what the traffic looked like yesterday, and which
 * radio is carrying it. Everything here is read-only — the actions live on the
 * Devices view, because acting on one device needs that device in front of you.
 */
import { ArrowDown, ArrowUpRight, Boxes, Check, RadioTower, TriangleAlert } from 'lucide-react'

import { ActivityItem, DeviceHeader, DeviceTable, Gateway } from '../components'
import { NetworkServerPanel } from '../panels/NetworkHealth'
import { type GatewaySummary } from '../control/status'
import type { NetworkStatus } from '../control/types'
import type { Device, GatewayRecord, NetworkEvent, Overview, View } from '../types'
import type { DeviceListControls, Navigate } from './shared'

/**
 * Height of one chart bar as a CSS percentage.
 *
 * Bars are scaled against the busiest hour so the chart shows the shape of the
 * day rather than absolute counts. A non-zero floor keeps an hour with a single
 * uplink visible instead of rendering as an invisible 1% sliver.
 */
function barHeight(count: number, busiestHour: number): string {
  if (!busiestHour) return '0%'
  return `${Math.max(3, count / busiestHour * 100)}%`
}

export function OverviewView({
  overview, gatewayTotals, status, gateways, events, filteredDevices,
  view, setView, onSelectDevice,
  query, setQuery, filter, setFilter, group, setGroup, deviceGroups,
}: {
  overview: Overview
  /** The reconciled inventory-plus-presence count, not either source alone. */
  gatewayTotals: GatewaySummary
  status: NetworkStatus | null
  gateways: GatewayRecord[]
  events: NetworkEvent[]
  filteredDevices: Device[]
  view: View
  setView: Navigate
  /** Opening a preview row opens its session, which lives on the Devices view. */
  onSelectDevice: (device: Device) => void
} & DeviceListControls) {
  const onlineCount = overview.onlineDevices
  const attentionCount = overview.attentionDevices
  const busiestHour = Math.max(...overview.hourlyUplinks, 0)

  // With no devices there is nothing to take a percentage of, and dividing by
  // zero would put NaN into the bar's width style. The bar keeps the exact
  // fraction while the label rounds: a 33.333% wide bar reading "33%".
  const availabilityWidth = overview.deviceCount ? onlineCount / overview.deviceCount * 100 : 0
  const availabilityLabel = overview.deviceCount ? Math.round(onlineCount / overview.deviceCount * 100) : 0

  // Three different situations, three different sentences. Saying "0 online"
  // when nothing is registered would read as an outage rather than as an empty
  // deployment, and presence we cannot see must not be reported as presence.
  const gatewayFootnote = gatewayTotals.total === 0
    ? 'No gateways known'
    : gatewayTotals.source === 'network-server'
      ? `${gatewayTotals.online} reporting to the network server`
      : `${gatewayTotals.total} registered, presence unknown`

  return <>
    <section className="metric-grid" aria-label="Network summary">
      <article className="metric-card">
        <div className="metric-top"><span>Registered devices</span><Boxes size={16} /></div>
        <div className="metric-value">{overview.deviceCount}<span className="metric-unit"> devices</span></div>
        <div className="metric-foot">Persisted device inventory</div>
      </article>

      <article className="metric-card">
        <div className="metric-top">
          <span>Online now</span>
          <span className={`status-indicator ${onlineCount ? 'green' : ''}`} />
        </div>
        <div className="metric-value">{onlineCount}<span className="metric-unit"> / {overview.deviceCount}</span></div>
        <div className="metric-foot">
          <span className="availability-bar"><i style={{ width: `${availabilityWidth}%` }} /></span>
          <span>{availabilityLabel}% availability</span>
        </div>
      </article>

      <article className="metric-card">
        <div className="metric-top"><span>Needs attention</span><TriangleAlert size={16} className="warning-icon" /></div>
        <div className="metric-value">{attentionCount + overview.offlineDevices}<span className="metric-unit"> devices</span></div>
        <div className="metric-foot">
          <span className="metric-warning">{attentionCount} low battery</span>
          <span>·</span>
          <span>{overview.offlineDevices} offline</span>
        </div>
      </article>

      <article className="metric-card gateway-metric">
        <div className="metric-top"><span>Gateways</span><RadioTower size={16} /></div>
        <div className="metric-value">{gatewayTotals.online}<span className="metric-unit"> / {gatewayTotals.total}</span></div>
        <div className="metric-foot">
          <span className={`status-indicator ${gatewayTotals.online ? 'green' : ''}`} />{gatewayFootnote}
        </div>
      </article>
    </section>

    <section className="middle-grid">
      <article className="panel traffic-panel">
        <div className="panel-heading">
          <div><h2>Message traffic</h2><p>Uplinks received in the last 24 hours</p></div>
          <span className="panel-caption">Last 24 hours</span>
        </div>
        <div className="traffic-summary"><strong>{overview.uplinks24h.toLocaleString()}</strong><small>received uplinks</small></div>
        <div className="chart-wrap">
          <div className="chart">
            <div className="chart-grid"><i /><i /><i /><i /><i /></div>
            <div className="chart-bars" role="img" aria-label={`${overview.uplinks24h} uplinks received in the last 24 hours`}>
              {overview.hourlyUplinks.map((count, index) => (
                <i key={index} title={`${count} uplinks`} style={{ height: barHeight(count, busiestHour) }} />
              ))}
            </div>
            <div className="chart-x-labels">
              <span>24h ago</span><span>18h</span><span>12h</span><span>6h</span><span>Now</span>
            </div>
          </div>
        </div>
        <div className="chart-legend"><span><i className="legend-dot" />Received uplinks</span></div>
      </article>

      <article className="panel gateway-panel">
        <div className="panel-heading">
          <div>
            <h2>Gateway health</h2>
            <p>{gatewayTotals.online} online · {gatewayTotals.total} known{gatewayTotals.source === 'network-server' ? '' : ' (presence unknown)'}</p>
          </div>
          <button className="text-action" onClick={() => setView('Gateways')}>View all <ArrowUpRight size={14} /></button>
        </div>
        <div className="gateway-list">
          {gateways.slice(0, 3).map((gateway) => <Gateway key={gateway.id} gateway={gateway} />)}
          {gateways.length === 0 && <div className="empty-result">No gateways registered yet.</div>}
        </div>
        <div className="gateway-footer">
          <span>Latest reception</span><b>{gateways[0]?.lastSeen ?? 'No radio traffic'}</b>
        </div>
      </article>
    </section>

    {/* Identity and liveness of the process itself. It sits here rather
        than in Settings because these are read-only facts an operator
        needs while looking at traffic — the NetID to write into a device,
        and whether this server has been up long enough to trust its
        counters. */}
    <NetworkServerPanel status={status} inventoryIds={gateways.map((record) => record.id)} />

    <section className="panel device-panel">
      <DeviceHeader view={view} setView={setView} query={query} setQuery={setQuery}
        filter={filter} setFilter={setFilter} group={group} setGroup={setGroup} groups={deviceGroups} />
      <DeviceTable devices={filteredDevices.slice(0, 5)} onSelect={onSelectDevice} />
    </section>

    <section className="bottom-grid">
      <article className="panel activity-panel">
        <div className="panel-heading">
          <div><h2>Recent activity</h2><p>Latest network events</p></div>
          <button className="text-action" onClick={() => setView('Events')}>All events <ArrowUpRight size={14} /></button>
        </div>
        <div className="activity-list">
          {events.slice(0, 3).map((event) => <ActivityItem key={event.id} event={event} />)}
          {events.length === 0 && <div className="empty-result">No network events recorded.</div>}
        </div>
      </article>

      <article className="panel downlink-panel">
        <div className="panel-heading">
          <div><h2>Downlink queue</h2><p>Scheduled device messages</p></div>
          <ArrowDown size={16} className="muted-icon" />
        </div>
        <div className="queue-empty">
          <div className="queue-icon"><Check size={18} /></div>
          <b>{overview.queuedDownlinks === 0 ? 'Queue is clear' : `${overview.queuedDownlinks} messages queued`}</b>
          <span>{overview.queuedDownlinks === 0 ? 'No pending downlinks' : 'Waiting for an eligible receive window'}</span>
        </div>
        <div className="queue-footer">
          <span>Last sent</span><b>{overview.lastDownlink} <span>·</span> {overview.sentDownlinks24h} in 24h</b>
        </div>
      </article>
    </section>
  </>
}
