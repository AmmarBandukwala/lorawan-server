/**
 * The Events page: the network's own log, newest first.
 */
import { ActivityItem } from '../components'
import type { NetworkEvent } from '../types'

export function EventsView({ events }: { events: NetworkEvent[] }) {
  return (
    <section className="panel event-view">
      <div className="panel-heading">
        <div><h2>Network events</h2><p>Recent activity from your local network</p></div>
        <span className="panel-caption">Most recent</span>
      </div>
      <div className="activity-list event-list">
        {events.map((event) => <ActivityItem key={event.id} event={event} />)}
        {events.length === 0 && <div className="empty-result">No network events recorded.</div>}
      </div>
    </section>
  )
}
