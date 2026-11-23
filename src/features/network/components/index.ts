/**
 * The console's presentational kit.
 *
 * Unlike control/, this folder re-exports from an index. The distinction is
 * deliberate: the control modules are different concerns, and a barrel there
 * would let one import statement quietly pull in the health report, the device
 * edit form and the codec API. These are all the same kind of thing — small
 * components that render data they are handed — so importing "the kit" says
 * something true, and every consumer of a design system ends up wanting most of
 * it anyway.
 */
export { ActivityItem } from './ActivityItem'
export { CopyValue } from './CopyValue'
export { DeviceHeader, DeviceTable } from './DeviceList'
export { Gateway, GatewayCard } from './GatewaySignal'
export { PageHeadingAction } from './PageHeadingAction'
export { SettingsForm } from './SettingsForm'
