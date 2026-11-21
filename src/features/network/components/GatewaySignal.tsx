import {
  CircleCheck, RadioTower,
} from 'lucide-react'
import { SIGNAL_QUALITY, transportLabel } from '../constants'
import type { GatewayHealth } from '../control/types'
import type { GatewayRecord } from '../types'

/**
 * Signal quality bands for one gateway, worked out from its average SNR.
 *
 * A missing SNR means no reception has been recorded yet. That is not the same
 * thing as a weak signal, so it reports zero bars instead of the lowest band.
 */
function signalQuality(averageSnr: number | null): { quality: string; bars: number } {
  if (averageSnr === null) return { quality: 'No data', bars: 0 }
  if (averageSnr >= SIGNAL_QUALITY.excellentAbove) return { quality: 'Excellent', bars: 4 }
  if (averageSnr >= SIGNAL_QUALITY.goodAbove) return { quality: 'Good', bars: 3 }
  return { quality: 'Weak', bars: 1 }
}

/** The compact gateway row shown on the overview. */
export function Gateway({ gateway }: { gateway: GatewayRecord }) {
  const { quality, bars } = signalQuality(gateway.averageSnr)
  const onlineClass = gateway.state === 'Online' ? 'state-online' : 'state-offline'

  return (
    <div className="gateway-row">
      <div className="gateway-icon"><RadioTower size={17} /></div>
      <div className="gateway-info">
        <b>{gateway.name}</b>
        <small>{gateway.region} <span>·</span> {gateway.uplinks24h.toLocaleString()} uplinks / 24h</small>
      </div>
      <div className="gateway-signal">
        <span className={`state-pill ${onlineClass}`}><i />{gateway.state}</span>
        <div className="signal-bars" aria-label={`${quality} signal`}>
          {[1, 2, 3, 4].map((bar) => <i key={bar} className={bar <= bars ? 'filled' : ''} />)}
        </div>
      </div>
    </div>
  )
}

/**
 * One gateway in the full view.
 *
 * `health` is the network server's live report for this gateway's EUI. It is
 * kept apart from the inventory record because the two answer different
 * questions: the record says what has been stored about a gateway, while the
 * health report says whether it is still sending PULL_DATA, how many downlinks
 * it refused, and — through `last_tx_ack_error` — why. A gateway that accepts
 * every uplink and refuses every downlink looks perfectly healthy in the
 * database.
 */
export function GatewayCard({ gateway, health }: { gateway: GatewayRecord; health?: GatewayHealth }) {
  // A gateway that refuses downlinks is the failure mode operators notice last:
  // uplinks keep arriving, so nothing on the surface looks broken.
  const refusingDownlinks = health && health.downlink_errors > 0
  // Fewer frames confirmed on air than scheduled means the concentrator dropped
  // some, usually because the slot had already passed (TOO_LATE).
  const notAllTransmitted = health
    ? health.downlinks_sent > health.downlinks_confirmed
    : false
  // The live report decides the badge, because it knows whether PULL_DATA is
  // still arriving. "Silent" rather than "Offline": a gateway we know about but
  // have not heard from recently is a different problem from one never seen.
  const isOnline = health ? health.online : gateway.state === 'Online'
  const stateLabel = health ? (health.online ? 'Online' : 'Silent') : gateway.state
  // Model and firmware are optional, so each carries its own " · value" prefix
  // and disappears entirely when the gateway did not report it.
  const modelSuffix = health?.model ? <span> <span>·</span> {health.model}</span> : null
  const firmwareSuffix = health?.firmware ? <span> <span>·</span> fw {health.firmware}</span> : null
  return (
    <article className="panel gateway-card">
      <div className="gateway-card-top">
        <div className="gateway-icon large"><RadioTower size={20} /></div>
        <span className={`state-pill ${isOnline ? 'state-online' : 'state-offline'}`}><i />{stateLabel}</span>
      </div>

      <h2>{gateway.name}</h2>

      <p className="gateway-address">{gateway.id} <span>·</span> {health ? health.address : gateway.region}{modelSuffix}{firmwareSuffix}</p>

      <div className="gateway-stats">
        <div><span>Uplinks · 24h</span><b>{gateway.uplinks24h.toLocaleString()}</b></div>
        <div><span>Average RSSI</span><b>{gateway.averageRssi === null ? 'No data' : `${gateway.averageRssi.toFixed(0)} dBm`}</b></div>
        <div><span>Last reception</span><b>{gateway.lastSeen}</b></div>
        <div><span>Average SNR</span><b>{gateway.averageSnr === null ? 'No data' : `${gateway.averageSnr.toFixed(1)} dB`}</b></div>
      </div>

      {health && (
        <div className="gateway-health">
          <div><span>Packets seen</span><b>{health.packets.toLocaleString()}</b></div>
          <div><span>Downlinks sent</span><b>{health.downlinks_sent.toLocaleString()}</b></div>
          <div><span>Confirmed on air</span><b className={notAllTransmitted ? 'text-warning' : undefined}>{health.downlinks_confirmed.toLocaleString()} / {health.downlinks_sent.toLocaleString()}</b></div>
          <div><span>Downlinks refused</span><b className={refusingDownlinks ? 'text-warning' : undefined}>{health.downlink_errors.toLocaleString()}</b></div>
          <div><span>RX packets OK</span><b>{health.rx_ok.toLocaleString()} / {health.rx_received.toLocaleString()}</b></div>
        </div>
      )}

      {refusingDownlinks && (
        <p className="form-error" role="alert">Last refusal: {health.last_tx_ack_error || 'the concentrator did not say why'}. Downlinks scheduled for this gateway are not reaching devices.</p>
      )}

      <div className="gateway-card-footer"><CircleCheck size={15} /> {health ? `${transportLabel(health.transport)} · ${health.protocol ? `protocol ${health.protocol}` : 'protocol unknown'} · seen ${health.last_seen}` : 'Persisted gateway record'}</div>
    </article>
  )
}
