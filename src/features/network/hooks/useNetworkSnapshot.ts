/**
 * The dashboard's data layer: what the network is, kept fresh.
 *
 * This is the polling loop that used to live inline in App. It is a hook because
 * it is stateful behaviour rather than rendering, and because App's job is to
 * decide what a view looks like — not when to refetch, which request supersedes
 * which, or whether a background refresh is allowed to overwrite what the
 * operator is typing.
 */
import { useCallback, useEffect, useRef, useState } from 'react'

import { emptySnapshot, loadNetworkSnapshot, sameSettings, sectionKeys, type NetworkSnapshot } from '../snapshot'
import { fetchStatus } from '../control/status'
import type { NetworkStatus } from '../control/types'
import { REFRESH_INTERVAL_MS } from '../constants'
import { emptySettings, type ConnectionState, type FacilitySettings } from '../types'

export type NetworkData = {
  snapshot: NetworkSnapshot
  /** Which endpoints answered, and therefore which badge the shell shows. */
  connection: ConnectionState
  /** A refresh is in flight; the manual refresh button spins on this. */
  loading: boolean
  /**
   * The network server's own health report, deliberately outside the snapshot:
   * it is process state no table holds, and one that is not answering must not
   * degrade the panels that did load.
   */
  status: NetworkStatus | null
  settingsDraft: FacilitySettings
  setSettingsDraft: (settings: FacilitySettings) => void
  refresh: () => Promise<void>
  /**
   * Merge a local change into the snapshot without waiting for the next poll.
   *
   * Always go through this rather than setting snapshot state directly: a poll
   * merges into the ref, so an optimistic update that touched only the state
   * would be quietly undone by the next refresh.
   */
  patch: (patch: Partial<NetworkSnapshot>) => void
}

/**
 * Polls every section of the snapshot on an interval.
 *
 * The settings draft lives here even though it is presentation state, because
 * its adoption rule is coupled to the refresh: a background poll may overwrite
 * the draft only while the draft still matches the settings it was seeded from.
 * Splitting the two would mean sharing a ref between hooks to express one rule.
 */
export function useNetworkSnapshot(intervalMs = REFRESH_INTERVAL_MS): NetworkData {
  const [snapshot, setSnapshot] = useState<NetworkSnapshot>(emptySnapshot)
  const [settingsDraft, setSettingsDraftState] = useState<FacilitySettings>(emptySettings)
  const [connection, setConnection] = useState<ConnectionState>('loading')
  const [loading, setLoading] = useState(false)
  const [status, setStatus] = useState<NetworkStatus | null>(null)

  // The newest snapshot, for merging a refresh into it. Written only where the
  // snapshot changes — in the refresh and in patch — so it never needs syncing
  // from state, and reading it does not depend on when React commits.
  const snapshotRef = useRef<NetworkSnapshot>(emptySnapshot)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
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
      setSettingsDraftState((draft) => (sameSettings(draft, previous.settings) ? next.settings : draft))
      setConnection(next.failed.length === 0 ? 'connected'
        : next.failed.length === sectionKeys.length ? 'offline' : 'degraded')

      // Best-effort and deliberately outside the snapshot. The badge in the
      // gateways view says when it is missing.
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

  const setSettingsDraft = useCallback((settings: FacilitySettings) => setSettingsDraftState(settings), [])

  const patch = useCallback((next: Partial<NetworkSnapshot>) => {
    const merged = { ...snapshotRef.current, ...next }
    snapshotRef.current = merged
    setSnapshot(merged)
  }, [])

  useEffect(() => {
    // Deferred by a task rather than called inline so the effect body does not
    // set state synchronously; the first paint still shows the loading state.
    const first = setTimeout(() => void refresh(), 0)
    const timer = setInterval(() => {
      // A hidden tab does not need fresh data, and a dashboard left open for a
      // week should not quietly turn into a load generator.
      if (!document.hidden) void refresh()
    }, intervalMs)
    return () => {
      clearTimeout(first)
      clearInterval(timer)
      abortRef.current?.abort()
    }
  }, [refresh, intervalMs])

  return { snapshot, connection, loading, status, settingsDraft, setSettingsDraft, refresh, patch }
}
