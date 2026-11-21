import { APP_KEY_HEX_DIGITS, EUI_HEX_DIGITS, UNASSIGNED_GROUP } from './constants'
import type { NewDevice } from './snapshot'

/** What the registration form collects, before any checking. */
export type DeviceFormValues = {
  name: string
  devEui: string
  joinEui: string
  appKey: string
  group: string
}

/**
 * Strips separators and upper-cases an identifier.
 *
 * Operators copy keys out of PDFs and terminal output, which bring spaces,
 * colons and dashes with them. Accepting those here means the value sent matches
 * the pattern the network server's schema checks for, instead of being rejected
 * for whitespace nobody can see.
 */
export function normalizeHex(value: string): string {
  return value.replace(/[^a-fA-F0-9]/g, '').toUpperCase()
}

/**
 * Returns the first problem with a device form, phrased as what the operator
 * should do, or null when the form can be sent.
 *
 * This runs in the browser only to produce a message in the right field. The
 * network server validates again and is the authority — a form in the DOM is
 * trivially bypassed, and the credentials it collects are exactly the kind an
 * attacker would tamper with.
 */
export function deviceFormError(values: DeviceFormValues): string | null {
  const devEui = normalizeHex(values.devEui)
  const joinEui = normalizeHex(values.joinEui)
  const appKey = normalizeHex(values.appKey)

  if (!values.name.trim()) return 'Give the device a name you will recognise in the list.'
  if (devEui.length !== EUI_HEX_DIGITS) {
    return `DevEUI must contain ${EUI_HEX_DIGITS} hexadecimal characters.`
  }
  // Empty is a real choice here, not a missing value: it means the network
  // accepts whatever JoinEUI the device presents during join.
  if (joinEui.length !== 0 && joinEui.length !== EUI_HEX_DIGITS) {
    return `JoinEUI is optional, but must contain ${EUI_HEX_DIGITS} hexadecimal characters.`
  }
  if (appKey.length !== APP_KEY_HEX_DIGITS) {
    return `AppKey must contain ${APP_KEY_HEX_DIGITS} hexadecimal characters. It is printed on the device label.`
  }
  return null
}

/** Turns validated form values into the request body. */
export function deviceFormRequest(values: DeviceFormValues): NewDevice {
  return {
    name: values.name.trim(),
    devEui: normalizeHex(values.devEui),
    joinEui: normalizeHex(values.joinEui),
    appKey: normalizeHex(values.appKey),
    group: values.group.trim() || UNASSIGNED_GROUP,
  }
}
