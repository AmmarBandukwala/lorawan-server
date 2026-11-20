export type DeviceState = 'Online' | 'Needs attention' | 'Offline'
export type Device = { id: string; name: string; eui: string; group: string; gateway: string; lastSeen: string; battery: number; state: DeviceState; rssi: number }
export type GatewayState = 'Online' | 'Offline'
export type GatewayRecord = { id: string; name: string; region: string; lastSeen: string; state: GatewayState; uplinks24h: number; averageRssi: number | null; averageSnr: number | null }
export type NetworkEvent = { id: number; type: 'success' | 'warning' | 'info'; title: string; detail: string; timeAgo: string }
export type Overview = { deviceCount: number; onlineDevices: number; attentionDevices: number; offlineDevices: number; gatewayCount: number; onlineGateways: number; uplinks24h: number; queuedDownlinks: number; sentDownlinks24h: number; lastDownlink: string; hourlyUplinks: number[] }
export type FacilitySettings = { configured: boolean; facilityName: string; region: string; operatorName: string; operatorRole: string }
/**
 * `degraded` means the API answered some endpoints and not others. It is
 * distinct from `offline` because the right operator action differs: a partial
 * failure usually names a single broken route, while `offline` means nothing is
 * reachable at all.
 */
export type ConnectionState = 'loading' | 'connected' | 'degraded' | 'offline'

/**
 * The device-status filter. `all` means no filter; every other value is a device
 * state to match exactly. Labels live in DEVICE_FILTERS so the wording can
 * change without touching filtering.
 */
export type DeviceFilter = 'all' | DeviceState

/**
 * The group filter. Unlike the status filter this one cannot be a closed set:
 * groups are whatever labels operators have typed, so the value is either 'all'
 * or an arbitrary string, and the dropdown is built from the devices themselves.
 */
export type GroupFilter = 'all' | string
export type View = 'Overview' | 'Devices' | 'Gateways' | 'Models' | 'Events' | 'Settings'

export const emptyOverview: Overview = {
  deviceCount: 0,
  onlineDevices: 0,
  attentionDevices: 0,
  offlineDevices: 0,
  gatewayCount: 0,
  onlineGateways: 0,
  uplinks24h: 0,
  queuedDownlinks: 0,
  sentDownlinks24h: 0,
  lastDownlink: 'Never',
  hourlyUplinks: [],
}

export const emptySettings: FacilitySettings = {
  configured: false,
  facilityName: '',
  region: '',
  operatorName: '',
  operatorRole: '',
}
