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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { createElement, type PropsWithChildren } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { getUserModels } from '@/features/playground/api'
import type { GroupOption } from '@/features/playground/types'
import type { PricingData, PricingModel } from '@/features/pricing/types'
import { api } from '@/lib/api'

import {
  buildCanvasChannels,
  getCanvasModelCapabilities,
  type CanvasCapability,
  type CanvasPricingModel,
} from '../model-catalog'
import { useCanvasCatalog } from '../use-canvas-catalog'

function model(
  name: string,
  metadata: Partial<CanvasPricingModel> = {}
): CanvasPricingModel {
  return { model_name: name, ...metadata }
}

const capabilityCases: Array<[string, CanvasPricingModel, CanvasCapability[]]> =
  [
    [
      'Whisper is excluded despite inherited chat',
      model('whisper-1', { supported_endpoint_types: ['openai'] }),
      [],
    ],
    [
      'Transcribe is excluded despite text output',
      model('gpt-4o-transcribe', {
        supported_endpoint_types: ['openai'],
        output_modalities: ['text'],
      }),
      [],
    ],
    [
      'chat-only name containing image/video',
      model('image-video-helper', { supported_endpoint_types: ['openai'] }),
      ['text'],
    ],
    ['unknown name without metadata', model('unknown-image-video'), []],
    [
      'Responses alone',
      model('unknown', { supported_endpoint_types: ['openai-response'] }),
      ['text'],
    ],
    [
      'authoritative image endpoint',
      model('unknown', { supported_endpoint_types: ['image-generation'] }),
      ['image'],
    ],
    [
      'authoritative video endpoint',
      model('unknown', { supported_endpoint_types: ['openai-video'] }),
      ['video'],
    ],
    [
      'multiple endpoints',
      model('unknown', {
        supported_endpoint_types: ['openai', 'image-generation', 'openai'],
      }),
      ['image', 'text'],
    ],
    [
      'explicit media and text outputs',
      model('grok-imagine-image', {
        supported_endpoint_types: ['openai'],
        output_modalities: ['image', 'text'],
      }),
      ['image', 'text'],
    ],
    [
      'explicit image output through Gemini',
      model('unknown', {
        supported_endpoint_types: ['gemini'],
        output_modalities: ['image'],
      }),
      ['image'],
    ],
    [
      'explicit text output overrides a known-name fallback',
      model('grok-imagine-image', {
        supported_endpoint_types: ['openai'],
        output_modalities: ['text'],
      }),
      ['text'],
    ],
    [
      'Grok image with inherited chat',
      model('grok-imagine-image', {
        supported_endpoint_types: ['openai', 'openai-response'],
      }),
      ['image'],
    ],
    [
      'Grok video with inherited chat',
      model('grok-imagine-video', { supported_endpoint_types: ['openai'] }),
      ['video'],
    ],
    [
      'TTS with inherited chat',
      model('gpt-4o-mini-tts', { supported_endpoint_types: ['openai'] }),
      ['audio'],
    ],
    [
      'unknown variant of a known name',
      model('grok-imagine-image-helper', {
        supported_endpoint_types: ['openai'],
      }),
      ['text'],
    ],
    [
      'embeddings and rerank',
      model('unknown', {
        supported_endpoint_types: ['embeddings', 'jina-rerank'],
      }),
      [],
    ],
    [
      'compact and search are not generation endpoints',
      model('unknown', {
        supported_endpoint_types: [
          'openai-response-compact',
          'openai-alpha-search',
        ],
      }),
      [],
    ],
    [
      'task image protocol with inherited chat',
      model('unknown', {
        supported_endpoint_types: ['openai'],
        task_plugins: [{ protocols: ['openai_image'] }],
      }),
      ['image'],
    ],
    [
      'task video and Responses protocols',
      model('unknown', {
        supported_endpoint_types: ['openai'],
        task_plugins: [
          { protocols: ['openai_video', { name: 'openai_responses' }] },
        ],
      }),
      ['video'],
    ],
    [
      'task Responses does not establish a media or text capability',
      model('unknown', {
        supported_endpoint_types: ['openai-response'],
        task_plugins: [{ protocols: [{ name: 'openai_responses' }] }],
      }),
      [],
    ],
    [
      'task protocol scopes only its declared models',
      model('unknown', {
        task_plugins: [
          { protocols: [{ name: 'openai_image', models: ['another-model'] }] },
        ],
      }),
      [],
    ],
    [
      'task manifest scopes only its declared models',
      model('unknown', {
        task_plugins: [
          { models: ['another-model'], protocols: ['openai_video'] },
        ],
      }),
      [],
    ],
    [
      'opaque plugin keys do not imply generation',
      model('unknown', {
        supported_endpoint_types: ['openai'],
        task_plugins: ['image-video-plugin'],
      }),
      [],
    ],
    [
      'authoritative video endpoint overrides known image fallback',
      model('grok-imagine-image', {
        supported_endpoint_types: ['openai-video'],
      }),
      ['video'],
    ],
  ]

it.each(capabilityCases)(
  'classifies %s from its actual metadata',
  (_title, metadata, expected) => {
    expect(getCanvasModelCapabilities(metadata.model_name, metadata)).toEqual(
      expected
    )
  }
)

it('does not confuse input modalities or billing ratios with generation output', () => {
  const vision: PricingModel = {
    id: 1,
    model_name: 'vision-chat',
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 1,
    enable_groups: ['default'],
    supported_endpoint_types: ['openai'],
    input_modalities: ['text', 'image', 'audio', 'video'],
    image_ratio: 2,
    audio_ratio: 3,
  }
  expect(getCanvasModelCapabilities(vision.model_name, vision)).toEqual([
    'text',
  ])
})

it('uses only model-supported creation paths from both endpoint map formats', () => {
  const endpointMap = {
    speech: { path: '/v1/audio/speech', method: 'POST' },
    video: '/v1/videos',
    unrelated: { path: '/v1/images/generations', method: 'POST' },
    query: { path: '/v1/videos', method: 'GET' },
    openai: { path: '/v1/images/generations', method: 'POST' },
    transcribe: { path: '/v1/audio/transcriptions', method: 'POST' },
    translate: '/v1/audio/translations',
  }
  expect(
    getCanvasModelCapabilities(
      'custom',
      model('custom', {
        supported_endpoint_types: ['speech', 'video', 'query'],
      }),
      endpointMap
    )
  ).toEqual(['video', 'audio'])
  expect(
    getCanvasModelCapabilities(
      'custom',
      model('custom', { supported_endpoint_types: ['openai'] }),
      endpointMap
    )
  ).toEqual(['image'])
  expect(
    getCanvasModelCapabilities('custom', model('custom'), endpointMap)
  ).toEqual([])
  expect(
    getCanvasModelCapabilities(
      'speech-recognizer',
      model('speech-recognizer', {
        supported_endpoint_types: ['transcribe', 'translate'],
        output_modalities: ['text'],
      }),
      endpointMap
    )
  ).toEqual([])
  expect(
    getCanvasModelCapabilities(
      'speech-recognizer',
      model('speech-recognizer', { supported_endpoint_types: ['openai'] }),
      { openai: '/v1/audio/transcriptions' }
    )
  ).toEqual([])
})

const groups: GroupOption[] = [
  { value: 'default', label: 'Default', ratio: 1, desc: 'General' },
  { value: 'vip', label: 'VIP', ratio: 2 },
  { value: 'auto', label: 'Auto', ratio: 'auto' },
]

it.each<[string, string[] | undefined, 'chat' | 'responses']>([
  ['Responses-only', ['openai-response'], 'responses'],
  ['Chat and Responses', ['openai', 'openai-response'], 'chat'],
  ['Claude and Responses', ['anthropic', 'openai-response'], 'chat'],
  ['Gemini and Responses', ['gemini', 'openai-response'], 'chat'],
  ['unknown endpoints with confirmed text output', undefined, 'chat'],
])(
  'keeps %s selectable with the correct chat protocol',
  (_title, endpoints, protocol) => {
    const channels = buildCanvasChannels(
      [groups[0]],
      new Map([['default', [{ label: 'agent', value: 'agent' }]]]),
      {
        data: [
          model('agent', {
            supported_endpoint_types: endpoints,
            output_modalities: ['text'],
          }),
        ],
      },
      'https://station.example'
    )
    expect(channels[0].models).toEqual([
      {
        name: 'agent',
        capability: 'text',
        capabilities: ['text'],
        chatProtocol: protocol,
      },
    ])
  }
)

it('merges chat protocols only from metadata available to the selected group', () => {
  const channels = buildCanvasChannels(
    groups,
    new Map(
      groups.map((group) => [
        group.value,
        [{ label: 'shared', value: 'shared' }],
      ])
    ),
    {
      auto_groups: ['vip'],
      data: [
        model('shared', {
          enable_groups: ['all'],
          supported_endpoint_types: ['openai-response'],
          output_modalities: ['image', 'text'],
        }),
        model('shared', {
          enable_groups: ['vip'],
          supported_endpoint_types: ['openai'],
          output_modalities: ['text'],
        }),
      ],
    },
    'https://station.example'
  )
  expect(channels.map((channel) => channel.models)).toEqual([
    [
      {
        name: 'shared',
        capability: 'image',
        capabilities: ['image', 'text'],
        chatProtocol: 'responses',
      },
    ],
    [
      {
        name: 'shared',
        capability: 'image',
        capabilities: ['image', 'text'],
        chatProtocol: 'chat',
      },
    ],
    [
      {
        name: 'shared',
        capability: 'image',
        capabilities: ['image', 'text'],
        chatProtocol: 'chat',
      },
    ],
  ])
})

it('keeps each authorized model name once, unions its capabilities and respects group metadata', () => {
  const options = [
    'shared',
    'shared',
    'not-in-pricing',
    'grok-imagine-image',
  ].map((value) => ({ label: value, value }))
  const catalog = buildCanvasChannels(
    groups,
    new Map([
      ['default', options],
      ['vip', options],
      ['auto', options],
    ]),
    {
      auto_groups: ['vip'],
      data: [
        model('shared', {
          enable_groups: ['default'],
          supported_endpoint_types: ['image-generation', 'openai'],
        }),
        model('shared', {
          enable_groups: ['default'],
          supported_endpoint_types: ['image-generation'],
        }),
        model('shared', {
          enable_groups: ['vip'],
          supported_endpoint_types: ['openai-video'],
        }),
        model('grok-imagine-image', {
          enable_groups: ['vip'],
          supported_endpoint_types: ['openai'],
        }),
        model('pricing-only', {
          enable_groups: ['all'],
          supported_endpoint_types: ['openai'],
        }),
      ],
    },
    'https://station.example'
  )
  expect(catalog).toEqual([
    {
      id: 'default',
      name: 'Default',
      baseUrl: 'https://station.example',
      apiKey: '',
      apiFormat: 'openai',
      models: [
        {
          name: 'shared',
          capability: 'image',
          capabilities: ['image', 'text'],
          chatProtocol: 'chat',
        },
      ],
    },
    {
      id: 'vip',
      name: 'VIP',
      baseUrl: 'https://station.example',
      apiKey: '',
      apiFormat: 'openai',
      models: [
        {
          name: 'shared',
          capability: 'video',
          capabilities: ['video'],
          chatProtocol: 'chat',
        },
        {
          name: 'grok-imagine-image',
          capability: 'image',
          capabilities: ['image'],
          chatProtocol: 'chat',
        },
      ],
    },
    {
      id: 'auto',
      name: 'Auto',
      baseUrl: 'https://station.example',
      apiKey: '',
      apiFormat: 'openai',
      models: [
        {
          name: 'shared',
          capability: 'video',
          capabilities: ['video'],
          chatProtocol: 'chat',
        },
        {
          name: 'grok-imagine-image',
          capability: 'image',
          capabilities: ['image'],
          chatProtocol: 'chat',
        },
      ],
    },
  ])
})

it('retains empty groups and limits missing-metadata fallbacks to exact known models', () => {
  expect(
    buildCanvasChannels([], new Map(), undefined, 'https://station.example')
  ).toEqual([])
  const catalog = buildCanvasChannels(
    groups.slice(0, 2),
    new Map([
      [
        'default',
        ['grok-imagine-image', 'unknown-image'].map((value) => ({
          label: value,
          value,
        })),
      ],
    ]),
    undefined,
    'https://station.example'
  )
  expect(catalog[0].models).toEqual([
    {
      name: 'grok-imagine-image',
      capability: 'image',
      capabilities: ['image'],
      chatProtocol: 'chat',
    },
  ])
  expect(catalog[1].models).toEqual([])
})

function pricingData(data: CanvasPricingModel[]): PricingData {
  return {
    success: true,
    data: data.map((entry, id) => ({
      id,
      quota_type: 0,
      model_ratio: 1,
      completion_ratio: 1,
      enable_groups: ['all'],
      ...entry,
    })),
    vendors: [],
    group_ratio: {},
    usable_group: {},
    supported_endpoint: {},
    auto_groups: [],
  }
}

function deferred<T>() {
  let resolve: (value: T) => void = () => {
    throw new Error('Promise not initialized')
  }
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

const clients: QueryClient[] = []

function renderCatalog() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const wrapper = (props: PropsWithChildren) =>
    createElement(QueryClientProvider, { client }, props.children)
  return { client, wrapper }
}

afterEach(() => {
  cleanup()
  clients.forEach((client) => client.clear())
  clients.length = 0
  vi.restoreAllMocks()
})

it('reuses fresh playground/pricing caches and shares an in-flight playground model request', async () => {
  const { client, wrapper } = renderCatalog()
  client.setQueryData(['playground-groups'], [groups[0]])
  client.setQueryData(
    ['pricing'],
    pricingData([model('chat', { supported_endpoint_types: ['openai'] })])
  )
  const response = deferred<{ data: { success: boolean; data: string[] } }>()
  const get = vi.spyOn(api, 'get').mockReturnValue(response.promise)
  const pendingModels = client.fetchQuery({
    queryKey: ['playground-models', 'default'],
    queryFn: () => getUserModels('default'),
  })
  const { result } = renderHook(useCanvasCatalog, { wrapper })
  expect(result.current.isLoading).toBe(true)
  await act(async () => {
    response.resolve({ data: { success: true, data: ['chat'] } })
    await pendingModels
  })
  await waitFor(() => expect(result.current.isLoading).toBe(false))
  expect(get).toHaveBeenCalledTimes(1)
  expect(result.current.groups).toEqual([groups[0]])
  expect(result.current.channels[0]).toEqual({
    id: 'default',
    name: 'Default',
    baseUrl: window.location.origin,
    apiKey: '',
    apiFormat: 'openai',
    models: [
      {
        name: 'chat',
        capability: 'text',
        capabilities: ['text'],
        chatProtocol: 'chat',
      },
    ],
  })
  expect(result.current.error).toBeNull()
})

it('starts groups and pricing together, then fetches all groups without waiting for pricing', async () => {
  const { wrapper } = renderCatalog()
  const groupsResponse = deferred<{ data: unknown }>()
  const pricingResponse = deferred<{ data: unknown }>()
  const defaultResponse = deferred<{ data: unknown }>()
  const vipResponse = deferred<{ data: unknown }>()
  const get = vi.spyOn(api, 'get').mockImplementation((url, config) => {
    if (url === '/api/user/self/groups') return groupsResponse.promise
    if (url === '/api/pricing') return pricingResponse.promise
    return config?.params.group === 'default'
      ? defaultResponse.promise
      : vipResponse.promise
  })
  const { result } = renderHook(useCanvasCatalog, { wrapper })
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
  await act(async () =>
    groupsResponse.resolve({
      data: {
        success: true,
        data: {
          default: { desc: 'General', ratio: 1 },
          vip: { desc: 'Premium', ratio: 2 },
        },
      },
    })
  )
  await waitFor(() => expect(get).toHaveBeenCalledTimes(4))
  expect(
    get.mock.calls
      .filter(([url]) => url === '/api/user/models')
      .map(([, config]) => config?.params.group)
  ).toEqual(['default', 'vip'])
  expect(result.current.isLoading).toBe(true)
  await act(async () => {
    defaultResponse.resolve({ data: { success: true, data: ['custom'] } })
    vipResponse.resolve({ data: { success: true, data: [] } })
    pricingResponse.resolve({
      data: pricingData([
        model('custom', { supported_endpoint_types: ['image-generation'] }),
      ]),
    })
  })
  await waitFor(() => expect(result.current.isLoading).toBe(false))
  expect(result.current.channels[0].models).toEqual([
    {
      name: 'custom',
      capability: 'image',
      capabilities: ['image'],
      chatProtocol: 'chat',
    },
  ])
  expect(result.current.channels[1].models).toEqual([])
})

it('exposes a pricing business failure and refetches models for newly added groups on recovery', async () => {
  const { wrapper } = renderCatalog()
  let recovered = false
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/pricing') {
      return {
        data: recovered
          ? pricingData([
              model('chat', { supported_endpoint_types: ['openai'] }),
            ])
          : { success: false, message: 'Metadata offline' },
      }
    }
    if (url === '/api/user/self/groups') {
      const data = { default: { desc: 'General', ratio: 1 } }
      return {
        data: {
          success: true,
          data: recovered
            ? { ...data, vip: { desc: 'Premium', ratio: 2 } }
            : data,
        },
      }
    }
    return { data: { success: true, data: ['chat'] } }
  })
  const { result } = renderHook(useCanvasCatalog, { wrapper })
  await waitFor(() =>
    expect(result.current.error?.message).toBe('Metadata offline')
  )
  await waitFor(() => expect(result.current.isLoading).toBe(false))
  expect(result.current.channels[0].models).toEqual([])
  recovered = true
  await act(async () => {
    await result.current.refetch()
  })
  await waitFor(() => expect(result.current.error).toBeNull())
  await waitFor(() =>
    expect(result.current.channels.map((channel) => channel.models)).toEqual([
      [
        {
          name: 'chat',
          capability: 'text',
          capabilities: ['text'],
          chatProtocol: 'chat',
        },
      ],
      [
        {
          name: 'chat',
          capability: 'text',
          capabilities: ['text'],
          chatProtocol: 'chat',
        },
      ],
    ])
  )
  expect(
    get.mock.calls.some(
      ([url, config]) =>
        url === '/api/user/models' && config?.params.group === 'vip'
    )
  ).toBe(true)
})

it('exposes per-group model business failures instead of reporting an empty successful catalog', async () => {
  const { client, wrapper } = renderCatalog()
  client.setQueryData(['playground-groups'], [groups[0]])
  client.setQueryData(['pricing'], pricingData([]))
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: false, message: 'Models offline' },
  })
  const { result } = renderHook(useCanvasCatalog, { wrapper })
  await waitFor(() =>
    expect(result.current.error?.message).toBe('Models offline')
  )
  expect(result.current.isLoading).toBe(false)
  expect(result.current.channels[0].models).toEqual([])
})
