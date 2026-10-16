import i18n, { getCanvasLocale } from '@/i18n'
import {
  decodeChannelModel,
  modelOptionsFromChannels,
  selectableModelsByCapability,
  useConfigStore,
} from '@/stores/use-config-store'
import { useThemeStore } from '@/stores/use-theme-store'

import { getSiteContext } from './site-context'

export function applySiteContext(): void {
  const context = getSiteContext()
  useThemeStore.setState({ theme: context.theme })
  void i18n.changeLanguage(getCanvasLocale(context.language))
  const current = useConfigStore.getState().config
  const models = modelOptionsFromChannels(context.channels)
  const next = {
    ...current,
    channels: context.channels,
    models,
    proxyEnabled: false,
    apiKey: '',
    baseUrl: window.location.origin,
  }
  for (const capability of ['image', 'video', 'text', 'audio'] as const) {
    const key = `${capability}Model` as const
    const candidates = selectableModelsByCapability(next, capability)
    if (!candidates.includes(next[key])) {
      next[key] =
        candidates.find(
          (value) => decodeChannelModel(value)?.channelId === context.group
        ) ??
        candidates[0] ??
        ''
    }
  }
  useConfigStore.setState({ config: next })
}
