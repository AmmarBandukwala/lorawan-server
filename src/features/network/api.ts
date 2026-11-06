import type { Device, FacilitySettings, GatewayRecord, NetworkEvent, Overview } from './types'

export type NetworkSnapshot = {
  devices: Device[]
  overview: Overview
  gateways: GatewayRecord[]
  events: NetworkEvent[]
  settings: FacilitySettings
}

export async function loadNetworkSnapshot(): Promise<NetworkSnapshot> {
  const paths = ['/api/v1/devices', '/api/v1/overview', '/api/v1/gateways', '/api/v1/events', '/api/v1/settings']
  const responses = await Promise.all(paths.map((path) => fetch(path)))
  if (responses.some((response) => !response.ok)) throw new Error('Network API unavailable')

  const [devices, overview, gateways, events, settings] = await Promise.all([
    responses[0].json() as Promise<Device[]>,
    responses[1].json() as Promise<Overview>,
    responses[2].json() as Promise<GatewayRecord[]>,
    responses[3].json() as Promise<NetworkEvent[]>,
    responses[4].json() as Promise<FacilitySettings>,
  ])
  return { devices, overview, gateways, events, settings }
}

export async function registerDevice(name: string, devEui: string): Promise<Device> {
  const response = await fetch('/api/v1/devices', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, devEui, group: 'Unassigned' }),
  })
  if (response.status === 409) throw new Error('A device with this DevEUI is already registered.')
  if (!response.ok) throw new Error('Device could not be saved. Check the name and DevEUI.')
  return response.json() as Promise<Device>
}

export async function saveFacilitySettings(settings: FacilitySettings): Promise<FacilitySettings> {
  const response = await fetch('/api/v1/settings', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(settings),
  })
  if (!response.ok) throw new Error('Settings could not be saved. Check the values and try again.')
  return response.json() as Promise<FacilitySettings>
}
