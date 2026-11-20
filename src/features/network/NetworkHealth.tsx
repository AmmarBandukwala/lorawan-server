import { Cpu, RadioTower, Timer } from 'lucide-react'
import {
  decodeHealth,
  formatUptime,
  gatewaySummary,
  recentlyRestarted,
  type GatewaySummary,
  type NetworkStatus,
} from './control'
import { transportLabel } from './constants'

/**
 * Network-server identity and liveness.
 *
 * Every value here is already in the status response and was being discarded.
 * The NetID is the one that costs an operator the most time: provisioning an
 * OTAA device means writing this network's NetID into it, and until now the only
 * way to read it back was to query the server by hand.
 *
 * Uptime earns its place by being the cheapest crash-loop detector there is. A
 * device list that has not changed next to "up 14s" says the container is
 * restarting, which nothing else in a browser reveals.
 */
export function NetworkServerPanel({ status, inventoryIds }: {
  status: NetworkStatus | null
  inventoryIds: string[]
}) {
  if (!status) return null
  const summary = gatewaySummary(status, inventoryIds)
  const restarted = recentlyRestarted(status.uptime_seconds)

  return (
    <article className="panel server-panel">
      <div className="panel-heading">
        <div>
          <h2>Network server</h2>
          <p>What this network is configured as, from the process that runs it</p>
        </div>
        <span className={`uptime-badge ${restarted ? 'recent' : ''}`}>
          <Timer size={12} />{` up ${formatUptime(status.uptime_seconds)}`}
        </span>
      </div>

      <div className="gateway-stats">
        <div><span>Band</span><b>{status.region}</b></div>
        <div><span>NetID</span><b title="Write this into an OTAA device so it accepts this network's join-accept">{status.net_id}</b></div>
        <div><span>Downlink power</span><b>{status.downlink_power_dbm} dBm</b></div>
        <div>
          <span>Gateways {summary.source === 'network-server' ? 'online' : 'registered'}</span>
          <b>{summary.online} / {summary.total}</b>
        </div>
      </div>

      {restarted && (
        <p className="form-error" role="alert">
          This network server started less than two minutes ago. If the number stays
          small across refreshes, the process is restarting rather than freshly deployed.
        </p>
      )}
      {summary.source === 'inventory' && (
        <p className="panel-note">
          Presence is unknown: the network server is not reporting, so this count comes
          from the inventory and says nothing about radio health.
        </p>
      )}
      {status.transports && status.transports.length > 0 && (
        <p className="panel-note">
          <RadioTower size={11} /> Listening for{' '}
          {status.transports.map((t) => transportLabel(t)).join(' and ')}
        </p>
      )}
    </article>
  )
}

/**
 * Payload-formatter health, on the page where the scripts are edited.
 *
 * A formatter that will not compile is the quietest failure this system has: the
 * devices join, they report on time, their signal looks fine, and every frame is
 * stored as bytes nobody can read. The per-device console does show a frame's
 * decode error, but nothing did until now said "these models are broken" — and
 * an operator with two hundred devices on one of them is not going to find it by
 * opening two hundred consoles.
 */
export function DecodeHealthPanel({ decoding }: { decoding: NetworkStatus['decoding'] }) {
  const health = decodeHealth(decoding)
  if (!health) return null

  return (
    <article className="panel decode-panel">
      <div className="panel-heading">
        <div>
          <h2>Decoding</h2>
          <p>
            {health.enabled
              ? 'Payload formatters are running on incoming frames'
              : 'Payload decoding is switched off; frames are stored as received'}
          </p>
        </div>
        <span className="decode-counters">
          <Cpu size={13} /> {health.framesDecoded.toLocaleString()} decoded
          {health.framesFailed > 0 && <em> · {health.framesFailed.toLocaleString()} failed</em>}
        </span>
      </div>

      {health.problem && <p className="form-error" role="alert">{health.problem}</p>}

      {health.broken.length > 0 && (
        <div className="broken-list">
          {health.broken.map((b) => (
            <div className="broken-row" key={b.model_id}>
              <code>{b.model_id}</code>
              <span>{b.reason}</span>
            </div>
          ))}
        </div>
      )}

      {health.broken.length === 0 && health.framesFailed === 0 && (
        <p className="panel-note">
          {health.compiled.toLocaleString()} formatter{health.compiled === 1 ? '' : 's'} compiled
          {' '}and nothing rejected since the server started. Only models that have sent a
          frame since then appear in these counts.
        </p>
      )}
    </article>
  )
}

export type { GatewaySummary }
