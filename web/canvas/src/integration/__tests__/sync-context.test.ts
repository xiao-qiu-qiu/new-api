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
