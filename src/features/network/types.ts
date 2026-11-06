export type DeviceState = 'Online' | 'Needs attention' | 'Offline'
export type Device = { id: string; name: string; eui: string; group: string; gateway: string; lastSeen: string; battery: number; state: DeviceState; rssi: number }
export type GatewayState = 'Online' | 'Offline'
export type GatewayRecord = { id: string; name: string; region: string; lastSeen: string; state: GatewayState; uplinks24h: number; averageRssi: number | null; averageSnr: number | null }
export type NetworkEvent = { id: number; type: 'success' | 'warning' | 'info'; title: string; detail: string; timeAgo: string }
export type Overview = { deviceCount: number; onlineDevices: number; attentionDevices: number; offlineDevices: number; gatewayCount: number; onlineGateways: number; uplinks24h: number; queuedDownlinks: number; sentDownlinks24h: number; lastDownlink: string; hourlyUplinks: number[] }
export type FacilitySettings = { configured: boolean; facilityName: string; region: string; operatorName: string; operatorRole: string }
export type ConnectionState = 'loading' | 'connected' | 'offline'
export type View = 'Overview' | 'Devices' | 'Gateways' | 'Events' | 'Settings'

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
