import {
  Plus,
} from 'lucide-react'
import { viewOffersDeviceRegistration } from '../constants'
import type { View } from '../types'

/**
 * The page heading's primary action, which is per view rather than global.
 *
 * Renders nothing where there is nothing to do. An empty `.heading-actions`
 * leaves the refresh button alone on the right, which is the honest layout: the
 * heading offers what this page can actually do.
 */

export function PageHeadingAction({ view, canProvision, onAddDevice }: {
  view: View
  /** Provisioning needs the API reachable; disabled rather than hidden, so the
   *  action is still discoverable and its unavailability is explained by the
   *  connection notice above it. */
  canProvision: boolean
  onAddDevice: () => void
}) {
  if (!viewOffersDeviceRegistration(view)) return null
  return (
    <button className="button button-primary" onClick={onAddDevice} disabled={!canProvision}>
      <Plus size={16} />Add device
    </button>
  )
}
