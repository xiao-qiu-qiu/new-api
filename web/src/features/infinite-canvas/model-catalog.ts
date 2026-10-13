/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type { GroupOption, ModelOption } from '../playground/types'
import type { PricingModel } from '../pricing/types'

export type { GroupOption } from '../playground/types'

// Canvas audio nodes generate speech through /v1/audio/speech. Recognition
// endpoints return text but are neither TTS nor chat/Agent generation modes.
export type CanvasCapability = 'image' | 'video' | 'text' | 'audio'

export type CanvasModel = {
  name: string
  capability: CanvasCapability
  capabilities?: CanvasCapability[]
}

export type CanvasChannel = {
  id: string
  name: string
  baseUrl: string
  apiKey: string
  apiFormat: 'openai'
  models: CanvasModel[]
}

/** Optional public task metadata; protocol names follow Task Plugin API v1. */
export type CanvasTaskPlugin = {
  key?: string
  models?: string[]
  protocols?: Array<string | { name: string; models?: string[] }>
}

// Keep compatibility with the current PricingModel without changing its type.
// Current servers omit task_plugins; newer catalog responses can supply it.
export type CanvasPricingModel = Pick<
  PricingModel,
  'model_name' | 'supported_endpoint_types' | 'output_modalities'
> & {
  enable_groups?: string[]
  task_plugins?: Array<CanvasTaskPlugin | string>
}

export type CanvasPricingMetadata = {
  data: CanvasPricingModel[]
  auto_groups?: string[]
  // The Go endpoint map returns EndpointInfo objects; older frontend types
  // describe string paths instead. Accept both actual wire representations.
  supported_endpoint?: Record<
    string,
    string | { path: string; method?: string }
  >
}

const capabilityOrder: CanvasCapability[] = ['image', 'video', 'audio', 'text']
const textEndpoints = new Set([
  'openai',
  'openai-response',
  'anthropic',
  'gemini',
])
const mediaEndpoints = new Map<string, CanvasCapability>([
  ['image-generation', 'image'],
  ['openai-video', 'video'],
])
const mediaPaths = new Map<string, CanvasCapability>([
  ['/v1/images/generations', 'image'],
  ['/v1/images/edits', 'image'],
  ['/v1/videos', 'video'],
  ['/v1/audio/speech', 'audio'],
])
const recognitionPaths = new Set([
  '/v1/audio/transcriptions',
  '/v1/audio/translations',
])
const recognitionModels = new Set([
  'whisper-1',
  'gpt-4o-transcribe',
  'gpt-4o-transcribe-diarize',
  'gpt-4o-mini-transcribe',
  'gpt-4o-mini-transcribe-2025-03-20',
  'gpt-4o-mini-transcribe-2025-12-15',
])

// Exact identities declared in common/model.go, openai/constant.go,
// xai/constants.go and the sora/xai task manifests. No substring guessing:
// native channel metadata also advertises generic chat for these media models.
const knownMediaModels = new Map<string, CanvasCapability>([
  ['dall-e-2', 'image'],
  ['dall-e-3', 'image'],
  ['gpt-image-1', 'image'],
  ['gpt-image-1-mini', 'image'],
  ['gpt-image-1.5', 'image'],
  ['grok-2-image-1212', 'image'],
  ['grok-imagine-image', 'image'],
  ['grok-imagine-image-pro', 'image'],
  ['sora-2', 'video'],
  ['sora-2-pro', 'video'],
  ['grok-imagine-video', 'video'],
  ['grok-imagine-video-1.5', 'video'],
  ['grok-imagine-video-1.5-lite', 'video'],
  ['tts-1', 'audio'],
  ['tts-1-1106', 'audio'],
  ['tts-1-hd', 'audio'],
  ['tts-1-hd-1106', 'audio'],
  ['gpt-4o-mini-tts', 'audio'],
  ['gpt-4o-mini-tts-2025-03-20', 'audio'],
  ['gpt-4o-mini-tts-2025-12-15', 'audio'],
])

export function getCanvasModelCapabilities(
  name: string,
  metadata?: CanvasPricingModel,
  endpointMap: CanvasPricingMetadata['supported_endpoint'] = {}
): CanvasCapability[] {
  const capabilities = new Set<CanvasCapability>()
  const outputs = metadata?.output_modalities
  const endpoints = metadata?.supported_endpoint_types ?? []
  let hasTextEndpoint = false
  let hasRecognitionEndpoint = false
  for (const endpoint of endpoints) {
    const capability = mediaEndpoints.get(endpoint)
    if (capability) capabilities.add(capability)

    const info = endpointMap[endpoint]
    const path = typeof info === 'string' ? info : info?.path
    const normalizedPath = path?.replace(/\/+$/, '')
    const isRecognitionPath = normalizedPath
      ? recognitionPaths.has(normalizedPath)
      : false
    if (isRecognitionPath) hasRecognitionEndpoint = true
    const method = typeof info === 'string' ? 'POST' : (info?.method ?? 'POST')
    let pathCapability: CanvasCapability | undefined
    if (path && method.toUpperCase() === 'POST') {
      pathCapability = mediaPaths.get(path.replace(/\/+$/, ''))
      if (pathCapability) capabilities.add(pathCapability)
    }
    if (textEndpoints.has(endpoint) && !pathCapability && !isRecognitionPath) {
      hasTextEndpoint = true
    }
  }

  const isRecognitionOnly =
    recognitionModels.has(name) || (hasRecognitionEndpoint && !hasTextEndpoint)
  for (const output of outputs ?? []) {
    if (output === 'file' || (output === 'text' && isRecognitionOnly)) continue
    capabilities.add(output)
  }

  const plugins = (metadata?.task_plugins ?? []).filter(
    (plugin): plugin is CanvasTaskPlugin =>
      typeof plugin !== 'string' &&
      (!plugin.models || plugin.models.includes(name))
  )
  for (const plugin of plugins) {
    for (const protocol of plugin.protocols ?? []) {
      if (
        typeof protocol !== 'string' &&
        protocol.models &&
        !protocol.models.includes(name)
      ) {
        continue
      }
      const protocolName =
        typeof protocol === 'string' ? protocol : protocol.name
      if (protocolName === 'openai_image') capabilities.add('image')
      if (protocolName === 'openai_video') capabilities.add('video')
      // openai_responses is also used to render media tasks. It does not
      // establish text output or image/video generation on its own.
    }
  }

  const knownCapability = knownMediaModels.get(name)
  if (
    knownCapability &&
    capabilities.size === 0 &&
    outputs === undefined &&
    endpoints.every((endpoint) => textEndpoints.has(endpoint))
  ) {
    capabilities.add(knownCapability)
  }

  // Explicit output modalities can confirm a media model also produces text.
  // Otherwise ignore inherited chat endpoints for known media and task models.
  if (
    outputs === undefined &&
    !knownCapability &&
    !isRecognitionOnly &&
    plugins.length === 0 &&
    !metadata?.task_plugins?.some((plugin) => typeof plugin === 'string') &&
    hasTextEndpoint
  ) {
    capabilities.add('text')
  }
  return capabilityOrder.filter((capability) => capabilities.has(capability))
}

export function buildCanvasChannels(
  groups: GroupOption[],
  modelsByGroup: ReadonlyMap<string, readonly ModelOption[]>,
  pricing: CanvasPricingMetadata | undefined,
  baseUrl: string
): CanvasChannel[] {
  const metadataByName = new Map<string, CanvasPricingModel[]>()
  for (const metadata of pricing?.data ?? []) {
    const entries = metadataByName.get(metadata.model_name) ?? []
    entries.push(metadata)
    metadataByName.set(metadata.model_name, entries)
  }

  return groups.map((group) => {
    const models: CanvasModel[] = []
    const seen = new Set<string>()
    for (const option of modelsByGroup.get(group.value) ?? []) {
      if (seen.has(option.value)) continue
      seen.add(option.value)
      const metadata = (metadataByName.get(option.value) ?? []).filter(
        (entry) =>
          !entry.enable_groups ||
          entry.enable_groups.includes('all') ||
          entry.enable_groups.includes(group.value) ||
          (group.value === 'auto' &&
            entry.enable_groups.some((value) =>
              pricing?.auto_groups?.includes(value)
            ))
      )
      const capabilities = new Set(
        metadata.flatMap((entry) =>
          getCanvasModelCapabilities(
            option.value,
            entry,
            pricing?.supported_endpoint
          )
        )
      )
      if (!metadataByName.has(option.value)) {
        for (const capability of getCanvasModelCapabilities(option.value)) {
          capabilities.add(capability)
        }
      }
      const ordered = capabilityOrder.filter((capability) =>
        capabilities.has(capability)
      )
      const primary = ordered[0]
      if (primary) {
        // One entry per name: upstream lookups select the first name match.
        // Consumers filter capabilities for a node's actual generation mode.
        models.push({
          name: option.value,
          capability: primary,
          capabilities: ordered,
        })
      }
    }
    return {
      id: group.value,
      name: group.label,
      baseUrl,
      apiKey: '',
      apiFormat: 'openai',
      models,
    }
  })
}
