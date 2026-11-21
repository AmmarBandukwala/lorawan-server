/**
 * Device models: the payload codecs that turn bytes into readable values.
 */

import type { DeviceModel, ModelDeletion, ModelInput } from './types'
import { request } from './http'

/** The slug rule the server enforces, mirrored here for a local message. */
export const MODEL_ID_PATTERN = /^[a-z0-9][a-z0-9_-]{1,63}$/

export function modelIdProblem(id: string): string {
  if (id === '') return 'Give the model an id, for example "cold-chain-probe-v2".'
  if (!MODEL_ID_PATTERN.test(id)) {
    return 'An id is 2 to 64 characters of lower-case letters, digits, "-" or "_", starting with a letter or digit.'
  }
  return ''
}

export const fetchModels = () =>
  request<{ models: DeviceModel[] }>('GET', '/models').then((r) => r.models ?? [])

export const fetchModel = (modelID: string) =>
  request<DeviceModel>('GET', `/models/${modelID}`, undefined, {
    404: 'No device model with that name.',
  })

/**
 * Creates a model. A slug that already exists is a 409 rather than an overwrite:
 * a typo must not silently rewrite a formatter a fleet depends on.
 */
export const createModel = (modelID: string, input: ModelInput) =>
  request<DeviceModel>('POST', `/models/${modelID}`, {
    name: input.name,
    description: input.description ?? '',
    codec_kind: input.codecKind,
    codec_script: input.codecScript ?? '',
  }, {
    409: 'A model with that name already exists. Edit it instead of creating a new one.',
  })

/** Replaces a model. Also how a new slug is created, matching the server's upsert. */
export const updateModel = (modelID: string, input: ModelInput) =>
  request<DeviceModel>('PUT', `/models/${modelID}`, {
    name: input.name,
    description: input.description ?? '',
    codec_kind: input.codecKind,
    codec_script: input.codecScript ?? '',
  })

export const deleteModel = (modelID: string) =>
  request<ModelDeletion>('DELETE', `/models/${modelID}`, undefined, {
    404: 'No device model with that name.',
  })

/**
 * The result of running a formatter against bytes without saving anything.
 *
 * A compile error and a runtime error both arrive as a successful HTTP response
 * with ok:false, because the request was well formed and the honest answer to
 * "does this script work" is "no, and here is why". Treating them as transport
 * failures would hide the message that names the offending line.
 */

export type DecodeTrial = {
  ok: boolean
  data?: Record<string, unknown>
  error?: string
  elapsed_ms?: number
}

export const testModelDecode = (
  modelID: string,
  input: { payload: string; fPort: number; devEUI?: string; script?: string },
) =>
  request<DecodeTrial>('POST', `/models/${modelID}/decode`, {
    payload: input.payload,
    f_port: input.fPort,
    dev_eui: input.devEUI,
    script: input.script,
  })

/**
 * One stored uplink with whatever the formatter made of it.
 *
 * `decoded` is the formatter's own object, passed through untouched so a reading
 * of 41.2 does not become 41.200000000000005 on the way to the screen. Key order
 * is not preserved — the database stores it as JSONB — so a comparison against a
 * vendor tool should compare values, not bytes.
 */
