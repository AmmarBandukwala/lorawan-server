/**
 * The live session of one device: reading it, and the commands that reach a
 * device over the radio.
 *
 * These calls resolve when the network server has accepted the work, not when the
 * device has acted on it. A class-A device listens for two seconds after it
 * speaks and then goes quiet, so a queued frame leaves in whatever window the
 * device opens next — the UI must not claim more than "queued".
 */

import type { DeviceSession } from './types'
import { request } from './http'

export const fetchSession = (devEUI: string) =>
  request<DeviceSession>('GET', `/devices/${devEUI}/session`)

export const enqueueDownlink = (
  devEUI: string,
  input: { data: string; fPort?: number; confirmed?: boolean },
) =>
  request<{ dev_eui: string; queued: boolean; f_port: number; confirmed: boolean; explanation: string }>(
    'POST',
    `/devices/${devEUI}/downlink`,
    { data: input.data, f_port: input.fPort, confirmed: input.confirmed ?? false },
  )

export const cancelDownlink = (devEUI: string) =>
  request<{ dev_eui: string; cancelled: boolean }>('DELETE', `/devices/${devEUI}/downlink`)

/** Asks the device for a battery and link-margin report on its next uplink. */
export const requestDeviceStatus = (devEUI: string) =>
  request<{ dev_eui: string; scheduled: boolean }>('POST', `/devices/${devEUI}/devstatus`, {})

/**
 * What an operator can change about a device once it is on the network.
 *
 * Every field is optional and omitted rather than sent as null, because the
 * network server distinguishes "not mentioned" from "set to false". A form that
 * sent every field on every save would silently re-enable ADR on a device whose
 * name was the only thing edited.
 *
 * Credentials are not among them. The network server never returns keys, so a
 * console could not resupply them; editing works precisely because it does not
 * ask for what it cannot have.
 */

export function validatePayload(hex: string, maxBytes = 51): string {
  const cleaned = hex.replace(/\s+/g, '')
  if (cleaned === '') return 'Enter a payload to send.'
  if (!/^[0-9a-fA-F]+$/.test(cleaned)) return 'Use hexadecimal digits only (0-9, a-f).'
  if (cleaned.length % 2 !== 0) return 'Hex needs an even number of digits.'
  if (cleaned.length / 2 > maxBytes) return `At most ${maxBytes} bytes per downlink.`
  return ''
}

/**
 * Device models: what a kind of sensor is, and how its bytes are read.
 *
 * A model is the unit that carries a vendor's payload formatter, so this is the
 * part of the console an operator touches when integrating a new sensor type —
 * paste the vendor's JavaScript, test it against a frame from the datasheet,
 * save it, then point devices at it.
 *
 * The formatter is returned on reads. It is not a secret (it came from a vendor's
 * datasheet) and an operator who cannot see what a fleet is running cannot audit
 * it. The list omits scripts, because a fleet's worth of them is hundreds of
 * kilobytes and a picker only needs names.
 */
