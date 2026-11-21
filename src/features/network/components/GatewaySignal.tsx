import {
  ArrowUpRight, CircleCheck, RadioTower,
} from 'lucide-react'
import { SIGNAL_QUALITY, transportLabel } from '../constants'
import type { GatewayHealth } from '../control/types'
import type { GatewayRecord } from '../types'

function signalQuality(averageSnr: number | null): { quality: string; bars: number } {
  if (averageSnr === null) return { quality: 'No data', bars: 0 }
  if (averageSnr >= SIGNAL_QUALITY.excellentAbove) return { quality: 'Excellent', bars: 4 }
  if (averageSnr >= SIGNAL_QUALITY.goodAbove) return { quality: 'Good', bars: 3 }
  return { quality: 'Weak', bars: 1 }
}

/**
 * Signal quality bands for one gateway, from its average SNR.
 *
 * A null SNR means no reception has been recorded yet, which is not the same as
 * a weak signal, so it reports zero bars rather than the lowest band.
 */

export function Gateway({ gateway }: { gateway: GatewayRecord }) {
  const { quality, bars } = signalQuality(gateway.averageSnr)
  return <div className="gateway-row"><div className="gateway-icon"><RadioTower size={17} /></div><div className="gateway-info"><b>{gateway.name}</b><small>{gateway.region} <span>·</span> {gateway.uplinks24h.toLocaleString()} uplinks / 24h</small></div><div className="gateway-signal"><span className={`state-pill ${gateway.state === 'Online' ? 'state-online' : 'state-offline'}`}><i />{gateway.state}</span><div className="signal-bars" aria-label={`${quality} signal`}>{[1, 2, 3, 4].map((bar) => <i key={bar} className={bar <= bars ? 'filled' : ''} />)}</div></div></div>
}

/**
 * One gateway in the full view.
 *
 * `health` is the network server's live report for this EUI, kept separate from
 * the inventory record because they answer different questions. The record says
 * what has been stored about a gateway; the health report says whether it is
 * still sending PULL_DATA, how many downlinks it refused, and — through
 * `last_tx_ack_error` — why. A gateway that accepts every uplink and refuses
 * every downlink looks perfectly healthy in the database.

 */
export function GatewayCard({ gateway, health }: { gateway: GatewayRecord; health?: GatewayHealth }) {
  const refused = health && health.downlink_errors > 0
  return <article className="panel gateway-card"><div className="gateway-card-top"><div className="gateway-icon large"><RadioTower size={20} /></div><span className={`state-pill ${(health ? health.online : gateway.state === 'Online') ? 'state-online' : 'state-offline'}`}><i />{health ? (health.online ? 'Online' : 'Silent') : gateway.state}</span></div><h2>{gateway.name}</h2><p className="gateway-address">{gateway.id} <span>·</span> {health ? health.address : gateway.region}{health?.model ? <span> <span>·</span> {health.model}</span> : null}{health?.firmware ? <span> <span>·</span> fw {health.firmware}</span> : null}</p><div className="gateway-stats"><div><span>Uplinks · 24h</span><b>{gateway.uplinks24h.toLocaleString()}</b></div><div><span>Average RSSI</span><b>{gateway.averageRssi === null ? 'No data' : `${gateway.averageRssi.toFixed(0)} dBm`}</b></div><div><span>Last reception</span><b>{gateway.lastSeen}</b></div><div><span>Average SNR</span><b>{gateway.averageSnr === null ? 'No data' : `${gateway.averageSnr.toFixed(1)} dB`}</b></div></div>{health && <div className="gateway-health"><div><span>Packets seen</span><b>{health.packets.toLocaleString()}</b></div><div><span>Downlinks sent</span><b>{health.downlinks_sent.toLocaleString()}</b></div><div><span>Confirmed on air</span><b className={health.downlinks_sent > health.downlinks_confirmed ? 'text-warning' : undefined}>{health.downlinks_confirmed.toLocaleString()} / {health.downlinks_sent.toLocaleString()}</b></div><div><span>Downlinks refused</span><b className={refused ? 'text-warning' : undefined}>{health.downlink_errors.toLocaleString()}</b></div><div><span>RX packets OK</span><b>{health.rx_ok.toLocaleString()} / {health.rx_received.toLocaleString()}</b></div></div>}{refused && <p className="form-error" role="alert">Last refusal: {health.last_tx_ack_error || 'the concentrator did not say why'}. Downlinks scheduled for this gateway are not reaching devices.</p>}<div className="gateway-card-footer"><CircleCheck size={15} /> {health ? `${transportLabel(health.transport)} · ${health.protocol ? `protocol ${health.protocol}` : 'protocol unknown'} · seen ${health.last_seen}` : 'Persisted gateway record'}<button aria-label={`Gateway record for ${gateway.name}`}><ArrowUpRight size={15} /></button></div></article>
}
