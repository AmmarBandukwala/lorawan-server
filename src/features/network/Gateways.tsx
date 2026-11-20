import { ArrowUpRight, CircleCheck, Info, RadioTower, ShieldAlert } from 'lucide-react'
import { transportLabel } from './constants'
import type { GatewayHealth, GatewayListener } from './control'

/**
 * How to point a gateway at this server.
 *
 * This is the panel an operator needs on their first visit and that this console
 * did not have: a gateway is not added by filling in a form here. It announces
 * itself the moment it can reach an open endpoint, so the thing always missing is
 * on the other side — the address and port to type into the forwarder or the
 * station. A "register gateway" form would only create a record of a gateway that
 * still cannot connect, and then the console would disagree with the radio.
 *
 * The endpoints come from the network server rather than being assumed, because
 * NS_LBS_ADDR=off genuinely removes one: telling someone to configure a station
 * against a port nobody listens for is worse than saying nothing.
 */
export function GatewayConnectPanel({ listeners, transports }: {
  listeners?: GatewayListener[]
  transports?: string[]
}) {
  // An older network server reports the transport names without addresses. The
  // panel still earns its place then — "which protocols are open" is half the
  // answer — so the rows render without a port rather than disappearing.
  const rows: { transport: string; port?: string; wildcard: boolean; secured: boolean }[] =
    listeners && listeners.length > 0
      ? listeners.map((l) => ({
        transport: l.transport,
        port: l.port > 0 ? String(l.port) : undefined,
        wildcard: l.host_wildcard,
        secured: l.secured,
      }))
      : (transports ?? []).map((transport) => ({ transport, wildcard: true, secured: false }))

  // The host a gateway should be pointed at. When the server bound every
  // interface, the address this page was loaded from is the best available
  // guess at one the gateway can also reach — the same machine in a single-host
  // deployment, and in a larger one the operator knows better than we do, which
  // is why the substitution is stated rather than hidden.
  const host = typeof window === 'undefined' ? 'this-server' : window.location.hostname

  if (rows.length === 0) {
    return (
      <section className="panel connect-panel">
        <div className="panel-heading"><div><h2>Connect a gateway</h2>
          <p>No gateway endpoint is reporting itself</p></div></div>
        <p className="console-empty-note">
          The network server has not reported which gateway protocols it listens for, so
          there is nothing reliable to tell you to configure. Check that it is running and
          that its status endpoint is reachable.
        </p>
      </section>
    )
  }

  return (
    <section className="panel connect-panel">
      <div className="panel-heading">
        <div>
          <h2>Connect a gateway</h2>
          <p>A gateway appears here by itself once it can reach one of these endpoints — there is nothing to register first.</p>
        </div>
      </div>

      <div className="connect-grid">
        {rows.map((row) => (
          <article className="connect-card" key={row.transport}>
            <div className="connect-card-top">
              <div className="gateway-icon"><RadioTower size={17} /></div>
              <b>{transportLabel(row.transport)}</b>
            </div>

            {row.transport === 'basic-station'
              ? <Endpoint
                value={`wss://${host}${row.port ? `:${row.port}` : ''}/updates`}
                note="The station’s LNS endpoint. Use wss:// when a TLS proxy sits in front of this server, ws:// when nothing does." />
              : <Endpoint
                value={`${host}${row.port ? `:${row.port}` : ''}`}
                note="The packet forwarder’s server_path address and port — in global_conf.json for the classic forwarder, in the LNS server settings for an sx1302 one." />}

            {row.wildcard && <p className="connect-note"><Info size={13} />
              The server listens on every interface, so the host above is the one this page was loaded from. Use the address your gateway can actually reach.</p>}
            {!row.secured && <p className="connect-warning"><ShieldAlert size={13} />
              {row.transport === 'basic-station'
                ? 'No station token is configured, so anything that can reach this port can claim a gateway.'
                : 'This protocol carries no credential, so anything that can reach this port can claim a gateway EUI and inject uplinks. Keep it on a trusted network.'}</p>}
          </article>
        ))}
      </div>

      <ol className="connect-steps">
        <li><b>Configure the gateway</b><span>Point it at the endpoint above and restart its forwarder or station.</span></li>
        <li><b>Wait for it to check in</b><span>A forwarder sends a PULL_DATA every 30 seconds; a station holds a connection open. Either way it appears below within a minute.</span></li>
        <li><b>Check the band matches</b><span>The server stamps each gateway with the band it was configured for. A gateway aimed at a server running another band joins nothing.</span></li>
      </ol>
    </section>
  )
}

function Endpoint({ value, note }: { value: string; note: string }) {
  return (
    <div className="connect-endpoint">
      <code>{value}</code>
      <span>{note}</span>
    </div>
  )
}

/**
 * A gateway the network server has heard from.
 *
 * This is the primary gateway list, because the network server is what knows a
 * gateway exists — it is the one receiving the packets. An inventory row for a
 * gateway that has never transmitted is a label with nothing behind it, while a
 * gateway that transmits without an inventory row is a working gateway, so when
 * the two disagree the radio is the one that is right.
 */
export function LiveGatewayCard({ health, region }: { health: GatewayHealth; region: string }) {
  const refused = health.downlink_errors > 0
  // Scheduled but never radiated. The gap between these two counters is the only
  // sign that a downlink left the server and never reached the air.
  const unconfirmed = health.downlinks_sent - health.downlinks_confirmed
  return (
    <article className="panel gateway-card">
      <div className="gateway-card-top">
        <div className="gateway-icon large"><RadioTower size={20} /></div>
        <span className={`state-pill ${health.online ? 'state-online' : 'state-offline'}`}>
          <i />{health.online ? 'Online' : 'Silent'}
        </span>
      </div>
      <h2>{health.model || 'Gateway'} <small className="gateway-eui">{health.gateway_eui}</small></h2>
      <p className="gateway-address">
        {transportLabel(health.transport)} <span>·</span> {health.address || 'address unknown'}
        {health.firmware && <span> <span>·</span> fw {health.firmware}</span>}
      </p>

      <div className="gateway-stats">
        <div><span>Packets seen</span><b>{health.packets.toLocaleString()}</b></div>
        <div><span>RX packets OK</span><b>{health.rx_ok.toLocaleString()} / {health.rx_received.toLocaleString()}</b></div>
        <div><span>Downlinks sent</span><b>{health.downlinks_sent.toLocaleString()}</b></div>
        <div><span>Confirmed on air</span><b className={unconfirmed > 0 ? 'text-warning' : undefined}>{health.downlinks_confirmed.toLocaleString()}</b></div>
      </div>

      {refused && <p className="form-error" role="alert">Last refusal: {health.last_tx_ack_error || 'the concentrator did not say why'}. Downlinks scheduled for this gateway are not reaching devices.</p>}
      {!health.online && <p className="connect-note"><Info size={13} /> Last heard {formatSeen(health.last_seen)}. A forwarder that stops sending PULL_DATA goes quiet rather than reporting a problem.</p>}

      <div className="gateway-card-footer"><CircleCheck size={15} /> {region} · {health.protocol || 'protocol unknown'}</div>
    </article>
  )
}

/**
 * A last-seen timestamp as a local time, or a phrase when there is none.
 *
 * An unparsable or zero timestamp reports "never" rather than throwing: this
 * renders inside a card that is otherwise useful, and one bad field should not
 * take the gateway's whole row with it.
 */
function formatSeen(timestamp: string): string {
  if (!timestamp) return 'never'
  const at = new Date(timestamp)
  if (Number.isNaN(at.getTime())) return timestamp
  return at.toLocaleString('en-GB')
}

/**
 * A gateway that has an inventory row and no radio traffic to go with it.
 *
 * Listed separately rather than as a card, because showing it among the live
 * gateways with a zeroed-out stat grid reads as "this gateway is broken" when the
 * truth is usually "this gateway has never been pointed at the server". The
 * button scrolls to the endpoint that would fix it.
 */
export function UnheardGatewayRow({ name, id, onOpen }: {
  name: string
  id: string
  onOpen: () => void
}) {
  return (
    <div className="unheard-gateway">
      <RadioTower size={15} />
      <span><b>{name}</b> <small>{id} · no packets seen yet</small></span>
      <button className="text-action" onClick={onOpen}>How to connect it <ArrowUpRight size={13} /></button>
    </div>
  )
}
