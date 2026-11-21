/**
 * The Gateways page.
 *
 * Two populations are shown apart on purpose. The network server's presence list
 * is the truth about which radios are working; the database's inventory is what
 * someone typed in. Merging them into one list is what makes "I registered three
 * gateways and the page shows one" unanswerable, so the unheard rows get their
 * own section rather than a misleading row.
 */
import { GatewayCard } from '../components'
import { DutyCyclePanel } from '../panels/DeviceConsole'
import { GatewayConnectPanel, LiveGatewayCard, UnheardGatewayRow } from '../panels/Gateways'
import type { NetworkStatus } from '../control/types'
import type { GatewayRecord } from '../types'

export function GatewaysView({ status, gateways, unheardGateways }: {
  status: NetworkStatus | null
  gateways: GatewayRecord[]
  /** Inventory rows with no packets behind them. */
  unheardGateways: GatewayRecord[]
}) {
  // The network server's own list, which is the only record of radios that have
  // actually been heard from.
  const liveGateways = status?.gateways ?? []

  return <div className="gateway-view">
    {/* First on the page because it is what someone with a gateway in
        their hands needs, and it was the one thing this view did not
        have: gateways are not registered here, they arrive, so the
        useful prompt is the endpoint to point them at. */}
    <GatewayConnectPanel listeners={status?.listeners} transports={status?.transports} />

    <section className="gateway-grid-full">
      {/* Live presence leads. The network server is what hears the
          packets, so a gateway it has heard from exists regardless of
          whether anyone typed it into an inventory. */}
      {liveGateways.map((health) => {
        // EUIs reach us from two places and are not cased consistently, so the
        // match is on the value rather than on its spelling.
        const record = gateways.find((gateway) =>
          gateway.id.toUpperCase() === health.gateway_eui.toUpperCase())
        // An inventory row adds the database's own aggregates, so the
        // richer card is used when both views have something to say.
        return record
          ? <GatewayCard key={health.gateway_eui} gateway={record} health={health} />
          : <LiveGatewayCard key={health.gateway_eui} health={health} region={status?.region ?? 'band unknown'} />
      })}

      {liveGateways.length === 0 && (
        <div className="panel empty-panel">
          {status
            ? 'No gateway has contacted this server yet. Configure one with the endpoint above — it appears here within about 30 seconds of its first packet.'
            : 'The network server is not reachable, so gateway presence is unknown. This is not the same as having no gateways.'}
        </div>
      )}
    </section>

    {unheardGateways.length > 0 && (
      <section className="panel unheard-panel">
        <div className="panel-heading">
          <div>
            <h2>Registered, never heard from</h2>
            <p>Inventory rows with no packets behind them</p>
          </div>
        </div>
        {unheardGateways.map((gateway) => (
          <UnheardGatewayRow key={gateway.id} name={gateway.name} id={gateway.id}
            onOpen={() => window.scrollTo({ top: 0, behavior: 'smooth' })} />
        ))}
      </section>
    )}

    {status?.duty_cycle && (
      <DutyCyclePanel usage={status.duty_cycle.usage} window={status.duty_cycle.window}
        denials={status.duty_cycle.denials} />
    )}

    {status && status.integrations && (
      <article className="panel integration-panel">
        <div className="panel-heading">
          <div>
            <h2>Integrations</h2>
            <p>{status.integrations.sinks.join(', ') || 'No sinks configured'}</p>
          </div>
        </div>
        <div className="gateway-stats">
          <div><span>Delivered</span><b>{status.integrations.published.toLocaleString()}</b></div>
          <div><span>Failed</span><b>{status.integrations.delivery_failures.toLocaleString()}</b></div>
          <div><span>Dropped</span><b>{status.integrations.dropped.toLocaleString()}</b></div>
          <div><span>In queue</span><b>{status.integrations.queued.toLocaleString()}</b></div>
        </div>
        {/* Dropped events are silent data loss: the radio path is fine and the
            application simply never learns about some uplinks. */}
        {status.integrations.dropped > 0 && (
          <p className="form-error" role="alert">Events are being dropped: the queue is full, so a receiver is too slow or unreachable.</p>
        )}
      </article>
    )}

    {!status && (
      <div className="panel integration-panel"><b>No health report from the network server.</b><span> Gateway presence, duty-cycle occupancy and integration counters need the network server reachable.</span></div>
    )}
  </div>
}
