import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import type { SiteContext } from '../site-context'

const context: SiteContext = {
  userId: 7,
  theme: 'light',
  language: 'en-US',
  group: 'current',
  model: 'multimodal',
  agentGroup: '',
  agentModel: '',
  channels: [],
}
const currentChannel: SiteContext['channels'][number] = {
  id: 'current',
  name: 'Current',
  baseUrl: 'https://station.example',
  apiKey: '',
  apiFormat: 'openai',
  models: [
    {
      name: 'multimodal',
      capability: 'image',
      capabilities: ['image', 'video', 'text', 'audio'],
    },
  ],
}

beforeEach(() => {
  vi.resetModules()
  const values = new Map<string, string>()
  const storage = {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => {
      values.set(key, value)
    },
    removeItem: (key: string) => {
      values.delete(key)
    },
  }
  vi.stubGlobal('localStorage', storage)
  vi.stubGlobal('window', {
    __newApiCanvasHost: { context },
    localStorage: storage,
    location: { origin: 'https://station.example' },
  })
})
afterEach(() => vi.unstubAllGlobals())

it('keeps workbench selections when the Agent model or site theme changes', async () => {
  context.channels = [currentChannel, {
    ...currentChannel,
    id: 'other',
    models: [{ name: 'image', capability: 'image' }, { name: 'chat', capability: 'text' }],
  }]
  context.agentGroup = 'other'
  context.agentModel = 'chat'
  const { defaultConfig, useConfigStore } = await import('@/stores/use-config-store')
  const { applySiteContext } = await import('../sync-context')
  useConfigStore.setState({ config: { ...defaultConfig, imageModel: 'other::image', textModel: 'current::multimodal' } })

  applySiteContext()
  context.theme = 'dark'
  applySiteContext()

  expect(useConfigStore.getState().config.imageModel).toBe('other::image')
  expect(useConfigStore.getState().config.textModel).toBe('current::multimodal')
  context.agentGroup = ''
  context.agentModel = ''
  context.theme = 'light'
})

it.each([
  ['first handshake', '', false],
  ['refresh after a saved model is removed', 'removed::video', true],
] as const)(
  'uses secondary capabilities for defaults on %s',
  async (_title, previousModel, hasOtherGroup) => {
    context.channels = hasOtherGroup
      ? [
          {
            ...currentChannel,
            id: 'other',
            models: [{ name: 'video', capability: 'video' }],
          },
          currentChannel,
        ]
      : [currentChannel]
    const { defaultConfig, resolveModelForCapability, useConfigStore } =
      await import('@/stores/use-config-store')
    const { applySiteContext } = await import('../sync-context')
    useConfigStore.setState({
      config: { ...defaultConfig, videoModel: previousModel },
    })

    applySiteContext()

    const { config, isAiConfigReady } = useConfigStore.getState()
    for (const capability of ['image', 'video', 'text', 'audio'] as const) {
      expect(config[`${capability}Model`]).toBe('current::multimodal')
      const model = resolveModelForCapability(config, undefined, capability)
      expect(model).toBe('current::multimodal')
      expect(isAiConfigReady(config, model)).toBe(true)
    }
  }
)

it('preserves model pricing and descriptions when normalizing channels', async () => {
  const { normalizeChannelModels } = await import('@/stores/use-config-store')
  const models: SiteContext['channels'][number]['models'] = [
    {
      name: 'image',
      capability: 'image',
      description: 'Image generation',
      pricing: { summary: '$0.02 / image', details: ['Standard quality'] },
    },
    {
      name: 'video',
      capability: 'video',
      description: 'Video generation\nSupports text and reference images',
      pricing: { summary: '$0.10 / second' },
    },
  ]

  expect(normalizeChannelModels(models)).toMatchObject(models)
})

it('normalizes legacy models without pricing or descriptions', async () => {
  const { normalizeChannelModels } = await import('@/stores/use-config-store')

  const models = normalizeChannelModels([
    ' gpt-image-1 ',
    { name: 'chat', capability: 'text' },
    'gpt-image-1',
    '',
  ])

  expect(models).toEqual([
    { name: 'gpt-image-1', capability: 'image' },
    { name: 'chat', capability: 'text' },
  ])
})

it('keeps selected models when refreshed site prices and descriptions change', async () => {
  context.channels = [currentChannel, {
    ...currentChannel,
    id: 'other',
    models: [{
      ...currentChannel.models[0],
      pricing: { summary: '$0.02 / image' },
      description: 'Original description',
    }],
  }]
  const { defaultConfig, useConfigStore } = await import('@/stores/use-config-store')
  const { applySiteContext } = await import('../sync-context')
  const selections = {
    imageModel: 'other::multimodal',
    videoModel: 'other::multimodal',
    textModel: 'other::multimodal',
    audioModel: 'other::multimodal',
  }
  useConfigStore.setState({ config: { ...defaultConfig, ...selections } })
  applySiteContext()

  const updatedModel = {
    ...currentChannel.models[0],
    pricing: { summary: '$0.03 / image', details: ['Updated quality prices'] },
    description: 'Updated description',
  }
  context.channels = context.channels.map((channel) => ({
    ...channel,
    models: channel.id === 'other' ? [updatedModel] : channel.models,
  }))
  applySiteContext()

  const { config } = useConfigStore.getState()
  expect(config).toMatchObject(selections)
  expect(config.channels.find((channel) => channel.id === 'other')?.models)
    .toEqual([updatedModel])
})

it('restores model metadata and selections when loading a saved channel configuration', async () => {
  const { CONFIG_STORE_KEY, defaultConfig, useConfigStore } =
    await import('@/stores/use-config-store')
  const model: SiteContext['channels'][number]['models'][number] = {
    name: 'video',
    capability: 'video',
    description: 'Supports reference images',
    pricing: { summary: '$0.10 / second', details: ['720p output'] },
  }
  localStorage.setItem(CONFIG_STORE_KEY, JSON.stringify({
    state: {
      config: {
        ...defaultConfig,
        channels: [{ ...currentChannel, models: [model] }],
        videoModel: 'current::video',
      },
    },
    version: 0,
  }))

  await useConfigStore.persist.rehydrate()

  const { config } = useConfigStore.getState()
  expect(config.videoModel).toBe('current::video')
  expect(config.channels[0].models).toMatchObject([model])
})

it.each([
  ['images::gemini-nano-banana-2.1', 'images::gemini-nano-banana-2.1'],
  ['removed::gemini-nano-banana-2.1', 'current::multimodal'],
])('rehydrates saved selection %s against the current station catalog', async (saved, expected) => {
  context.channels = [currentChannel, {
    ...currentChannel,
    id: 'images',
    models: [{ name: 'gemini-nano-banana-2.1', capability: 'image' }],
  }]
  const { CONFIG_STORE_KEY, defaultConfig, useConfigStore } = await import('@/stores/use-config-store')
  const { applySiteContext } = await import('../sync-context')
  localStorage.setItem(CONFIG_STORE_KEY, JSON.stringify({
    state: { config: { ...defaultConfig, imageModel: saved, channels: [], models: [], apiKey: '' } },
    version: 0,
  }))

  await useConfigStore.persist.rehydrate()
  applySiteContext()

  expect(useConfigStore.getState().config.imageModel).toBe(expected)
  expect(useConfigStore.getState().config.channels).toEqual(context.channels)
})
