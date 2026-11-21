/**
 * The bands this deployment can be configured for.
 *
 * Fetched once rather than on every poll: the list changes only when the network
 * server is rebuilt, and the settings form is a rare visit compared with the
 * dashboard's refresh.
 */
import { useEffect, useState } from 'react'

import { fetchRegions } from '../control/status'
import type { RegionList } from '../control/types'
import { FALLBACK_REGIONS } from '../constants'

export type AvailableRegions = {
  regions: readonly string[]
  /**
   * Whether the list is the console's own guess rather than what the running
   * server enforces.
   *
   * This is part of the result rather than folded away because an operator
   * choosing a regulatory band deserves to know which of the two happened. A
   * failure is deliberately not an error state: the form still has to be
   * fillable while the network server is restarting.
   */
  isFallback: boolean
}

export function useRegions(): AvailableRegions {
  const [list, setList] = useState<RegionList | null>(null)

  useEffect(() => {
    let active = true
    fetchRegions()
      .then((fetched) => { if (active) setList(fetched) })
      .catch(() => { if (active) setList(null) })
    return () => { active = false }
  }, [])

  return {
    regions: list?.regions ?? FALLBACK_REGIONS,
    isFallback: list === null || list.source === 'fallback',
  }
}
