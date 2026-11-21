import {
  ArrowDown, Radio, TriangleAlert,
} from 'lucide-react'
import type { NetworkEvent } from '../types'

/**
 * One row of the activity feed.
 *
 * The icon carries the severity rather than the colour alone, because the colour
 * pairing (green/amber/grey) is hard to tell apart at a glance in a dense list.
 */
export function ActivityItem({ event }: { event: NetworkEvent }) {
  return <div className={`activity-item`}><span className={`activity-icon activity-${event.type}`}>{event.type === 'success' ? <ArrowDown size={14} /> : event.type === 'warning' ? <TriangleAlert size={14} /> : <Radio size={14} />}</span><span className="activity-copy"><b>{event.title}</b><small>{event.detail}</small></span><time>{event.timeAgo}</time></div>
}
