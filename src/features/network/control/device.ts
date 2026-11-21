/**
 * A provisioned device: what the network server holds about it, and the edit
 * form's diffing against that record.
 *
 * The diff is the safety property of the whole form. The server treats an omitted
 * field as "leave alone", so a panel that posted every field it was showing would
 * turn an unrendered checkbox into an instruction — renaming a device would
 * silently re-enable ADR on it. Everything here exists to make the request carry
 * only what the operator actually changed.
 */

import type { AttributeRow, DeviceEdit, DeviceForm, DeviceLocation, ProvisionedDevice } from './types'
import { request } from './http'

export const fetchDevice = (devEUI: string) =>
  request<ProvisionedDevice>('GET', `/devices/${devEUI}`, undefined, {
    404: 'This device is not provisioned on the network.',
  })

export function attributeMap(rows: AttributeRow[]): { map?: Record<string, string>; problem?: string } {
  const map: Record<string, string> = {}
  for (const row of rows) {
    const key = row.key.trim().toLowerCase()
    const value = row.value.trim()
    // A completely blank row is someone who pressed "Add" and moved on, not a
    // tag they meant to store.
    if (key === '' && value === '') continue
    if (key === '') return { problem: 'An attribute needs a key, or remove the empty row.' }
    if (!/^[a-z0-9_-]+$/.test(key)) {
      return { problem: `“${key}” can only use letters, numbers, dash and underscore.` }
    }
    if (value === '') return { problem: `“${key}” has no value. Fill it in or remove the row.` }
    if (key in map) return { problem: `“${key}” is listed twice.` }
    map[key] = value
  }
  return { map }
}

/** Stored attributes as editor rows, in a stable order. */
export function attributeRows(map: Record<string, string> | undefined): AttributeRow[] {
  if (!map) return []
  return Object.keys(map).sort().map((key) => ({ key, value: map[key] }))
}

/**
 * Whether two attribute maps hold the same pairs.
 *
 * Object comparison would always report a change, because building the map from
 * the form produces a new object every render.
 */
export function sameAttributes(
  a: Record<string, string> | undefined,
  b: Record<string, string>,
): boolean {
  const existing = a ?? {}
  const existingKeys = Object.keys(existing)
  if (existingKeys.length !== Object.keys(b).length) return false
  return existingKeys.every((key) => existing[key] === b[key])
}

/** A position as form text, for loading a stored device into the editor. */
export function locationFields(location: DeviceLocation | undefined): {
  latitude: string
  longitude: string
  altitude: string
} {
  if (!location) return { latitude: '', longitude: '', altitude: '' }
  return {
    latitude: String(location.latitude),
    longitude: String(location.longitude),
    // An absent altitude must not render as "0": that is a real height, and
    // showing it would invite the operator to save a height nobody measured.
    altitude: location.altitude === undefined ? '' : String(location.altitude),
  }
}

/** Whether a position matches what is stored, altitude included. */
export function sameLocation(a: DeviceLocation, b: DeviceLocation): boolean {
  return a.latitude === b.latitude && a.longitude === b.longitude &&
    (a.altitude ?? null) === (b.altitude ?? null)
}

/**
 * The position the form is asking for, or undefined for "no change".
 *
 * Three cases have to be told apart, and the empty-field case is the one that
 * would otherwise go wrong: clearing all three boxes on a device that has a
 * position means "remove it", which is an explicit null rather than silence.
 * A half-typed position returns undefined so it cannot be sent — the caller
 * shows locationProblem instead.
 */
export function positionFromForm(
  form: DeviceForm,
  stored: DeviceLocation | undefined,
): DeviceLocation | null | undefined {
  const lat = form.latitude.trim()
  const long = form.longitude.trim()
  const alt = form.altitude.trim()

  if (lat === '' && long === '' && alt === '') {
    return stored ? null : undefined
  }
  if (lat === '' || long === '') return undefined

  const latitude = Number(lat)
  const longitude = Number(long)
  if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) return undefined

  const next: DeviceLocation = { latitude, longitude }
  if (alt !== '') {
    const altitude = Number(alt)
    if (!Number.isFinite(altitude)) return undefined
    next.altitude = altitude
  }
  if (stored && sameLocation(stored, next)) return undefined
  return next
}

/**
 * What is wrong with the position fields, in operator terms, or '' when they are
 * usable.
 *
 * Ranges are checked here because a latitude of 91 does not mean anything, and
 * finding that out after a round trip costs the operator the context of what they
 * were typing.
 */
export function locationProblem(form: DeviceForm): string {
  const lat = form.latitude.trim()
  const long = form.longitude.trim()
  const alt = form.altitude.trim()
  if (lat === '' && long === '' && alt === '') return ''
  if (lat === '' || long === '') return 'A position needs both a latitude and a longitude. Clear all three to remove it.'
  const latitude = Number(lat)
  const longitude = Number(long)
  if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) return 'Latitude and longitude must be numbers.'
  if (latitude < -90 || latitude > 90) return 'Latitude must be between -90 and 90.'
  if (longitude < -180 || longitude > 180) return 'Longitude must be between -180 and 180.'
  if (alt !== '') {
    const altitude = Number(alt)
    if (!Number.isFinite(altitude)) return 'Altitude must be a number of metres.'
    if (altitude < -1000 || altitude > 10000) return 'Altitude must be between -1000 and 10000 metres.'
  }
  return ''
}

/**
 * The difference between what the form shows and what is stored.
 *
 * This is the whole safety property of the edit form in one function. The network
 * server treats an omitted field as "leave alone", so a panel that posted every
 * field it was showing would turn an unrendered checkbox into an instruction —
 * renaming a device would silently re-enable ADR on it. Comparing against the
 * loaded record rather than the form's own defaults is what prevents that.
 *
 * Returns an empty object when nothing changed, which the caller uses to disable
 * the save button rather than issue a request that would be refused.
 */
export function diffDeviceEdit(stored: ProvisionedDevice, form: DeviceForm): DeviceEdit {
  const edit: DeviceEdit = {}
  if (form.name.trim() !== (stored.name ?? '')) edit.name = form.name.trim()
  if (form.group.trim() !== (stored.group ?? '')) edit.group = form.group.trim()
  if (form.enabled !== stored.enabled) edit.enabled = form.enabled
  if (form.disableAdr !== stored.disable_adr) edit.disableAdr = form.disableAdr
  if (form.supportsClassC !== stored.supports_class_c) edit.supportsClassC = form.supportsClassC
  // Number('') is 0 and Number(' ') is 0 as well, which would silently set a
  // limit of DR0 — the tightest one there is — whenever the select has no value.
  // Only a non-empty, genuinely numeric entry counts as a number.
  const raw = form.maxDataRate.trim()
  const parsed = raw === '' ? Number.NaN : Number(raw)
  if (Number.isInteger(parsed) && parsed !== stored.max_data_rate) edit.maxDataRate = parsed

  const { map, problem } = attributeMap(form.attributes)
  // An unusable row set produces no edit at all rather than a partial one: the
  // caller shows the problem and keeps the save button disabled.
  if (!problem && map && !sameAttributes(stored.attributes, map)) edit.attributes = map

  const position = positionFromForm(form, stored.location)
  if (position !== undefined) edit.location = position

  // '' is an instruction to clear the association, so it counts as a change
  // whenever a model is currently set; leaving it out would make a model
  // impossible to detach from the console.
  const modelId = form.modelId.trim()
  if (modelId !== (stored.model_id ?? '')) edit.modelId = modelId
  return edit
}

/**
 * What an edit will change, in the operator's terms.
 *
 * Naming the fields rather than saying "saved" matters because two of them change
 * radio behaviour: an operator who toggled ADR should see that reflected rather
 * than a generic confirmation that leaves them checking the panel.
 */
export function describeDeviceEdit(edit: DeviceEdit): string {
  const parts: string[] = []
  if (edit.name !== undefined) parts.push('name')
  if (edit.group !== undefined) parts.push('group')
  if (edit.enabled !== undefined) parts.push(edit.enabled ? 'allowed on the air' : 'paused')
  if (edit.disableAdr !== undefined) parts.push(edit.disableAdr ? 'ADR off' : 'ADR on')
  if (edit.supportsClassC !== undefined) parts.push(edit.supportsClassC ? 'class C' : 'class A')
  if (edit.maxDataRate !== undefined) {
    parts.push(edit.maxDataRate === -1 ? 'data-rate limit lifted' : `limited to DR${edit.maxDataRate}`)
  }
  if (edit.attributes !== undefined) {
    const count = Object.keys(edit.attributes).length
    parts.push(count === 0 ? 'attributes cleared' : `${count} attribute${count === 1 ? '' : 's'}`)
  }
  if (edit.location !== undefined) {
    parts.push(edit.location === null
      ? 'position removed'
      : `position ${edit.location.latitude.toFixed(5)}, ${edit.location.longitude.toFixed(5)}`)
  }
  if (edit.modelId !== undefined) {
    parts.push(edit.modelId === '' ? 'model cleared (uplinks will no longer be decoded)' : `model ${edit.modelId}`)
  }
  if (parts.length === 0) return 'Saved.'
  const radio = parts.some((p) => p.includes('ADR') || p.includes('DR') || p.includes('class'))
  return `Saved: ${parts.join(', ')}.${radio ? ' Takes effect on the device’s next uplink.' : ''}`
}

/**
 * Changes a device's settings.
 *
 * Only the fields the operator actually changed are sent, for the reason above.
 * An empty edit is refused locally rather than round-tripped for a 200 that
 * changed nothing.
 */
export async function editDevice(devEUI: string, edit: DeviceEdit): Promise<ProvisionedDevice> {
  const body: Record<string, unknown> = {}
  if (edit.name !== undefined) body.name = edit.name
  if (edit.group !== undefined) body.group = edit.group
  if (edit.enabled !== undefined) body.enabled = edit.enabled
  if (edit.disableAdr !== undefined) body.disable_adr = edit.disableAdr
  if (edit.supportsClassC !== undefined) body.supports_class_c = edit.supportsClassC
  if (edit.maxDataRate !== undefined) body.max_data_rate = edit.maxDataRate
  if (edit.minDataRate !== undefined) body.min_data_rate = edit.minDataRate
  if (edit.maxTxPower !== undefined) body.max_tx_power = edit.maxTxPower
  // Both are sent as-is, including an empty object and an explicit null: those
  // are the clearing instructions, and dropping them would make a device's tags
  // and position impossible to remove from the console.
  if (edit.attributes !== undefined) body.attributes = edit.attributes
  if (edit.location !== undefined) body.location = edit.location
  // Sent as-is including '': clearing an association is an instruction, and
  // dropping an empty string would make a model impossible to detach.
  if (edit.modelId !== undefined) body.model_id = edit.modelId
  if (Object.keys(body).length === 0) {
    throw new Error('Nothing to save — no setting was changed.')
  }
  return request<ProvisionedDevice>('PATCH', `/devices/${devEUI}`, body, {
    404: 'This device is no longer provisioned on the network. It may have been removed.',
  })
}

/**
 * Revokes a device.
 *
 * The network server removes the session with the device, so this stops it
 * talking rather than merely hiding it. That is worth saying in the UI: an
 * operator who means "pause this device" wants the enabled toggle, and someone
 * who means "this sensor is gone" wants this one.
 */
export const deleteDevice = (devEUI: string) =>
  request<Record<string, never>>('DELETE', `/devices/${devEUI}`, undefined, {
    404: 'This device is not provisioned on the network.',
  })

/**
 * Battery as an operator reads it.
 *
 * The LoRaWAN field is 0..255 with 255 reserved for "cannot measure", which is
 * not a full charge, and an absent value means the device has never been asked.
 * Collapsing those three into one number would report a device that cannot
 * measure its cell as fully charged.
 */
