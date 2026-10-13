export type SiteChannel = {
  id: string
  name: string
  baseUrl: string
  apiKey: string
  apiFormat: 'openai'
  models: Array<{
    name: string
    capability: 'image' | 'video' | 'text' | 'audio'
    capabilities?: Array<'image' | 'video' | 'text' | 'audio'>
    chatProtocol?: 'chat' | 'responses'
  }>
}

export type SiteContext = {
  userId: number
  theme: 'light' | 'dark'
  language: string
  group: string
  model: string
  agentGroup: string
  agentModel: string
  channels: SiteChannel[]
}

type CanvasHost = {
  context: SiteContext
  fetch: (path: string, init?: RequestInit) => Promise<Response>
  selectModel: (group: string, model: string) => void
}

declare global {
  interface Window {
    __newApiCanvasHost?: CanvasHost
  }
}

export function getSiteContext(): SiteContext {
  if (!window.__newApiCanvasHost) {
    throw new Error('Open Infinite Canvas from new-api.')
  }
  return window.__newApiCanvasHost.context
}

export function siteFetch(path: string, init?: RequestInit): Promise<Response> {
  if (!window.__newApiCanvasHost) {
    return Promise.reject(new Error('Open Infinite Canvas from new-api.'))
  }
  return window.__newApiCanvasHost.fetch(path, init)
}

export function selectSiteModel(group: string, model: string): void {
  window.__newApiCanvasHost?.selectModel(group, model)
}

export async function initializeSiteContext(): Promise<void> {
  if (window.parent === window) {
    window.location.replace('/canvas')
    return new Promise(() => {})
  }
  await new Promise<void>((resolve) => {
    const receive = (event: MessageEvent) => {
      if (
        event.source !== window.parent ||
        event.origin !== window.location.origin ||
        event.data?.type !== 'new-api:canvas-context'
      ) {
        return
      }
      if (!window.__newApiCanvasHost?.context.userId) return
      window.removeEventListener('message', receive)
      resolve()
    }
    window.addEventListener('message', receive)
    window.parent.postMessage(
      { type: 'new-api:canvas-ready' },
      window.location.origin
    )
  })
}
