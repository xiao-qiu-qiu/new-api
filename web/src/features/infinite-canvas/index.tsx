import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { useSidebar } from '@/components/ui/sidebar'
import { useTheme } from '@/context/theme-provider'
import { useAuthStore } from '@/stores/auth-store'

import { fetchCanvasRelay } from './host-fetch'
import { useCanvasCatalog } from './use-canvas-catalog'

type Selection = {
  group: string
  model: string
  agentGroup: string
  agentModel: string
}

export function InfiniteCanvasPage() {
  const userId = useAuthStore((state) => state.auth.user?.id)
  if (!userId) return null
  return <CanvasWorkspace key={userId} userId={userId} />
}

function CanvasWorkspace(props: { userId: number }) {
  const { t, i18n } = useTranslation()
  const { resolvedTheme } = useTheme()
  const sidebar = useSidebar()
  const sidebarRef = useRef(sidebar)
  sidebarRef.current = sidebar

  useEffect(() => {
    const previousOpen = sidebarRef.current.open
    sidebarRef.current.setOpen(false)
    sidebarRef.current.setOpenMobile(false)
    return () => sidebarRef.current.setOpen(previousOpen)
  }, [])
  const catalog = useCanvasCatalog()
  const storageKey = `new-api:canvas-selection:${props.userId}`
  const [selection, setSelection] = useState<Selection>(() => {
    try {
      const stored = JSON.parse(
        localStorage.getItem(storageKey) || '{}'
      ) as Partial<Selection>
      return {
        group: stored.group || '',
        model: stored.model || '',
        agentGroup: stored.agentGroup || '',
        agentModel: stored.agentModel || '',
      }
    } catch {
      return { group: '', model: '', agentGroup: '', agentModel: '' }
    }
  })
  const frameRef = useRef<HTMLIFrameElement>(null)
  const [ready, setReady] = useState(false)
  const [failed, setFailed] = useState(false)
  const [revision, setRevision] = useState(0)
  const channels = catalog.channels
  const mediaChannels = channels.filter((channel) =>
    channel.models.some(
      (model) => model.capability === 'image' || model.capability === 'video'
    )
  )
  const agentChannels = channels.filter((channel) =>
    channel.models.some(
      (model) =>
        model.capability === 'text' || model.capabilities?.includes('text')
    )
  )
  const group =
    mediaChannels.find((channel) => channel.id === selection.group)?.id ||
    mediaChannels[0]?.id ||
    ''
  const agentGroup =
    agentChannels.find((channel) => channel.id === selection.agentGroup)?.id ||
    agentChannels[0]?.id ||
    ''
  const mediaModels =
    channels
      .find((channel) => channel.id === group)
      ?.models.filter(
        (model) => model.capability === 'image' || model.capability === 'video'
      ) || []
  const agentModels =
    channels
      .find((channel) => channel.id === agentGroup)
      ?.models.filter(
        (model) =>
          model.capability === 'text' || model.capabilities?.includes('text')
      ) || []
  const model =
    mediaModels.find((item) => item.name === selection.model)?.name ||
    mediaModels[0]?.name ||
    ''
  const agentModel =
    agentModels.find((item) => item.name === selection.agentModel)?.name ||
    agentModels[0]?.name ||
    ''

  useEffect(() => {
    try {
      localStorage.setItem(storageKey, JSON.stringify(selection))
    } catch {
      /* Browser storage may be full; the editor remains usable. */
    }
  }, [selection, storageKey])

  const context = useMemo(
    () => ({
      userId: props.userId,
      theme: resolvedTheme,
      language: i18n.resolvedLanguage || i18n.language,
      group,
      model,
      agentGroup,
      agentModel,
      channels,
    }),
    [
      props.userId,
      resolvedTheme,
      i18n.resolvedLanguage,
      i18n.language,
      group,
      model,
      agentGroup,
      agentModel,
      channels,
    ]
  )
  const contextRef = useRef(context)
  contextRef.current = context

  useEffect(() => {
    const receive = (event: MessageEvent) => {
      const frame = frameRef.current?.contentWindow
      if (
        !frame ||
        event.source !== frame ||
        event.origin !== window.location.origin
      ) {
        return
      }
      if (event.data?.type === 'new-api:canvas-mounted') {
        setReady(true)
        return
      }
      if (event.data?.type === 'new-api:canvas-error') {
        setFailed(true)
        return
      }
      if (event.data?.type !== 'new-api:canvas-ready') return
      const hostFrame = frame as Window & { __newApiCanvasHost?: unknown }
      hostFrame.__newApiCanvasHost = {
        context: contextRef.current,
        fetch: (path: string, init?: RequestInit) =>
          fetchCanvasRelay(props.userId, path, init),
        selectAgentModel: (nextGroup: string, nextModel: string) =>
          setSelection((current) => ({
            ...current,
            agentGroup: nextGroup,
            agentModel: nextModel,
          })),
      }
      frame.postMessage(
        { type: 'new-api:canvas-context' },
        window.location.origin
      )
    }
    window.addEventListener('message', receive)
    const timeout = window.setTimeout(() => setFailed(true), 30_000)
    return () => {
      window.removeEventListener('message', receive)
      window.clearTimeout(timeout)
    }
  }, [props.userId, revision])

  useEffect(() => {
    if (!ready) return
    const frame = frameRef.current?.contentWindow as
      | (Window & { __newApiCanvasHost?: { context: typeof context } })
      | null
    if (!frame?.__newApiCanvasHost) return
    frame.__newApiCanvasHost.context = context
    frame.postMessage(
      { type: 'new-api:canvas-context' },
      window.location.origin
    )
  }, [ready, context])

  if (catalog.isLoading) return <LoadingState />
  if (catalog.error) {
    return (
      <ErrorState
        description={t('Failed to load canvas models')}
        onRetry={() => void catalog.refetch()}
      />
    )
  }

  return (
    <section
      data-canvas-workspace
      className='flex size-full min-h-0 flex-col overflow-hidden'
    >
      <div className='relative min-h-0 flex-1'>
        {!ready && (
          <div className='bg-background absolute inset-0 z-10'>
            {failed ? (
              <ErrorState
                description={t('Failed to load canvas')}
                onRetry={() => {
                  setFailed(false)
                  setReady(false)
                  setRevision((value) => value + 1)
                }}
              />
            ) : (
              <LoadingState />
            )}
          </div>
        )}
        {/* This bundled first-party editor shares IndexedDB and the host bridge. The iframe isolates CSS, not untrusted code. */}
        {/* eslint-disable-next-line react/iframe-missing-sandbox -- same-origin first-party application */}
        <iframe
          key={revision}
          ref={frameRef}
          src='/canvas-app/index.html#/canvas'
          title={t('Infinite Canvas')}
          className='size-full border-0'
          allow='clipboard-read; clipboard-write; fullscreen'
        />
      </div>
    </section>
  )
}
