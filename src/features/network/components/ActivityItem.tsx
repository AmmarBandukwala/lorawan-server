import {
  ArrowDown, Radio, TriangleAlert,
} from 'lucide-react'
import type { NetworkEvent } from '../types'

/** The icon for one feed row, chosen by how serious the event is. */
function eventIcon(type: NetworkEvent['type']) {
  if (type === 'success') return <ArrowDown size={14} />
  if (type === 'warning') return <TriangleAlert size={14} />
  return <Radio size={14} />
}

/**
 * One row of the activity feed.
 *
 * The icon carries the severity rather than the colour alone, because the
 * green/amber/grey pairing is hard to tell apart at a glance in a dense list.
 */
export function ActivityItem({ event }: { event: NetworkEvent }) {
  return (
    <div className="activity-item">
      <span className={`activity-icon activity-${event.type}`}>{eventIcon(event.type)}</span>
      <span className="activity-copy"><b>{event.title}</b><small>{event.detail}</small></span>
      <time>{event.timeAgo}</time>
    </div>
  )
}
