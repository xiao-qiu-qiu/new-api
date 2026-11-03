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
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { useState, type ReactElement } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { AuthenticatedLayout } from '@/components/layout/components/authenticated-layout'
import { Button } from '@/components/ui/button'
import {
  Sidebar,
  SidebarProvider,
  SidebarTrigger,
} from '@/components/ui/sidebar'
import { DirectionProvider } from '@/context/direction-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { CanvasViewport, PersistentCanvas } from '../persistent-canvas'

type CanvasBridge = {
  context: { userId: number }
  fetch: (path: string, init?: RequestInit) => Promise<Response>
}

const clients: QueryClient[] = []
let originalStorage: Map<string, string>
let originalSidebarCookie: string | undefined

function signIn(userId: number): void {
  useAuthStore.getState().auth.setBundle({
    access_token: `canvas-test-${userId}`,
    token_type: 'Bearer',
    access_expires_at: Math.floor(Date.now() / 1000) + 3600,
    user: { id: userId, username: `canvas-user-${userId}`, role: 1 },
    session: {
      sid: `canvas-session-${userId}`,
      current: true,
      login_method: 'password',
      ip: '',
      user_agent: '',
      created_at: 0,
      last_active_at: 0,
      expires_at: Math.floor(Date.now() / 1000) + 3600,
    },
  })
}

function RouteFixture(): ReactElement {
  const [route, setRoute] = useState<'logs' | 'canvas' | 'pricing'>('logs')
  return (
    <PersistentCanvas>
      <nav aria-label='Test routes'>
        <Button onClick={() => setRoute('canvas')}>Canvas route</Button>
        <Button onClick={() => setRoute('logs')}>Logs route</Button>
        <Button onClick={() => setRoute('pricing')}>Pricing route</Button>
      </nav>
      {route === 'pricing' ? (
        <main>Pricing page</main>
      ) : (
        <SidebarProvider defaultOpen>
          <Sidebar collapsible='icon' aria-label='Site navigation' />
          <SidebarTrigger />
          {route === 'canvas' ? <CanvasViewport /> : <main>Logs page</main>}
        </SidebarProvider>
      )}
    </PersistentCanvas>
  )
}

function renderRoutes(
  content: ReactElement = <RouteFixture />
): ReturnType<typeof render> & { client: QueryClient } {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  client.setQueryData(
    ['playground-groups'],
    [{ label: 'Default', value: 'default', ratio: 1 }]
  )
  client.setQueryData(
    ['playground-models', 'default'],
    [{ label: 'gpt-image-1', value: 'gpt-image-1' }]
  )
  client.setQueryData(['pricing'], { success: true, data: [] })
  const view = render(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18next}>
        <ThemeProvider defaultTheme='light'>{content}</ThemeProvider>
      </I18nextProvider>
    </QueryClientProvider>
  )
  return { ...view, client }
}

async function connectEditor(frame: HTMLIFrameElement): Promise<CanvasBridge> {
  // jsdom does not execute the bundled editor: deliver its real bridge handshake.
  const editor = frame.contentWindow as Window & {
    __newApiCanvasHost?: CanvasBridge
  }
  expect(editor).not.toBeNull()
  await act(async () => {
    for (const type of ['new-api:canvas-ready', 'new-api:canvas-mounted']) {
      window.dispatchEvent(
        new MessageEvent('message', {
          data: { type },
          origin: window.location.origin,
          source: editor,
        })
      )
    }
  })
  await waitFor(() => expect(editor.__newApiCanvasHost).toBeDefined())
  const bridge = editor.__newApiCanvasHost
  if (!bridge) throw new Error('Canvas bridge was not installed')
  return bridge
}

beforeEach(() => {
  originalStorage = new Map(
    Array.from({ length: localStorage.length }, (_, index) => {
      const key = localStorage.key(index) ?? ''
      return [key, localStorage.getItem(key) ?? '']
    })
  )
  originalSidebarCookie = document.cookie
    .split('; ')
    .find((cookie) => cookie.startsWith('sidebar_state='))
  signIn(42)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue(
    new DOMRect(40, 60, 900, 600)
  )
  vi.stubGlobal(
    'ResizeObserver',
    class implements ResizeObserver {
      constructor(private callback: ResizeObserverCallback) {}
      observe(target: Element): void {
        this.callback(
          [
            {
              target,
              contentRect: target.getBoundingClientRect(),
            } as ResizeObserverEntry,
          ],
          this
        )
      }
      unobserve(): void {}
      disconnect(): void {}
    }
  )
})

afterEach(() => {
  cleanup()
  for (const client of clients) client.clear()
  clients.length = 0
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  for (const [key, value] of originalStorage) localStorage.setItem(key, value)
  document.cookie = 'sidebar_state=; path=/; max-age=0'
  if (originalSidebarCookie) {
    document.cookie = `${originalSidebarCookie}; path=/`
  }
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

it('waits for the first canvas visit and preserves the iframe across layout unmounts', async () => {
  const user = userEvent.setup()
  const view = renderRoutes()
  expect(screen.getByText('Logs page')).toBeVisible()
  expect(screen.queryByTitle('Infinite Canvas')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Pricing route' }))
  expect(screen.queryByTitle('Infinite Canvas')).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  const frame = (await screen.findByTitle(
    'Infinite Canvas'
  )) as HTMLIFrameElement
  const editor = frame.contentWindow
  await connectEditor(frame)
  expect(frame).toBeVisible()
  const parent = frame.parentElement

  await user.click(screen.getByRole('button', { name: 'Pricing route' }))
  expect(screen.getByText('Pricing page')).toBeVisible()
  expect(view.container.querySelector('[data-slot="sidebar"]')).toBeNull()
  expect(screen.getByTitle('Infinite Canvas')).toBe(frame)
  expect(frame).not.toBeVisible()
  expect(frame.closest('[hidden]')).not.toBeNull()
  expect(frame.closest('[inert]')).not.toBeNull()
  expect(frame.parentElement).toBe(parent)
  expect(frame.contentWindow).toBe(editor)

  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  expect(screen.getByTitle('Infinite Canvas')).toBe(frame)
  expect(frame).toBeVisible()
  expect(frame.closest('[inert]')).toBeNull()
  expect(frame.parentElement).toBe(parent)
  expect(frame.contentWindow).toBe(editor)
})

it('finishes pending media and starts another bridge request while its layout is absent', async () => {
  const user = userEvent.setup()
  renderRoutes()
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  const frame = (await screen.findByTitle(
    'Infinite Canvas'
  )) as HTMLIFrameElement
  const bridge = await connectEditor(frame)
  let finishGeneration!: (response: Response) => void
  const response = new Promise<Response>((resolve) => {
    finishGeneration = resolve
  })
  const fetch = vi
    .spyOn(globalThis, 'fetch')
    .mockImplementationOnce(() => response)
    .mockResolvedValueOnce(
      Response.json({ data: [{ url: '/generated/second.png' }] })
    )
  const signal = new AbortController().signal
  const pending = bridge.fetch('/pg/images/generations', {
    method: 'POST',
    body: JSON.stringify({ model: 'gpt-image-1', prompt: 'A paper boat' }),
    signal,
  })
  await waitFor(() => expect(fetch).toHaveBeenCalledOnce())
  const requestSignal = fetch.mock.calls[0][1]?.signal
  expect(requestSignal).toBeDefined()

  await user.click(screen.getByRole('button', { name: 'Pricing route' }))
  expect(frame).not.toBeVisible()
  expect(signal.aborted).toBe(false)
  expect(requestSignal?.aborted).toBe(false)
  finishGeneration(Response.json({ data: [{ url: '/generated/first.png' }] }))
  expect(await (await pending).json()).toEqual({
    data: [{ url: '/generated/first.png' }],
  })
  const background = await bridge.fetch('/pg/images/generations', {
    method: 'POST',
    body: JSON.stringify({ model: 'gpt-image-1', prompt: 'A paper kite' }),
    signal,
  })
  expect(await background.json()).toEqual({
    data: [{ url: '/generated/second.png' }],
  })
  expect(fetch).toHaveBeenCalledTimes(2)
  expect(signal.aborted).toBe(false)
  expect(requestSignal?.aborted).toBe(false)

  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  expect(screen.getByTitle('Infinite Canvas')).toBe(frame)
  expect(frame).toBeVisible()
  expect(
    (frame.contentWindow as Window & { __newApiCanvasHost?: CanvasBridge })
      .__newApiCanvasHost
  ).toBe(bridge)
})

it('fits the current viewport after resizing and returning without replacing the editor', async () => {
  const user = userEvent.setup()
  renderRoutes()
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  const frame = (await screen.findByTitle(
    'Infinite Canvas'
  )) as HTMLIFrameElement
  const editor = frame.contentWindow
  const surface = frame.closest('main')
  expect(surface).toHaveStyle({
    top: '60px',
    left: '40px',
    width: '900px',
    height: '600px',
  })

  vi.mocked(HTMLElement.prototype.getBoundingClientRect).mockReturnValue(
    new DOMRect(0, 48, 390, 796)
  )
  fireEvent(window, new Event('resize'))
  expect(surface).toHaveStyle({
    top: '48px',
    left: '0px',
    width: '390px',
    height: '796px',
  })

  await user.click(screen.getByRole('button', { name: 'Pricing route' }))
  vi.mocked(HTMLElement.prototype.getBoundingClientRect).mockReturnValue(
    new DOMRect()
  )
  fireEvent(window, new Event('resize'))
  expect(surface).toHaveStyle({ width: '390px', height: '796px' })

  vi.mocked(HTMLElement.prototype.getBoundingClientRect).mockReturnValue(
    new DOMRect(68, 48, 1212, 672)
  )
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  expect(surface).toHaveStyle({
    left: '68px',
    width: '1212px',
    height: '672px',
  })
  expect(frame).toBeVisible()
  expect(frame.contentWindow).toBe(editor)
})

it('retains the ready editor when a background catalog refresh fails', async () => {
  const user = userEvent.setup()
  const { client } = renderRoutes()
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  const frame = (await screen.findByTitle(
    'Infinite Canvas'
  )) as HTMLIFrameElement
  await connectEditor(frame)
  await user.click(screen.getByRole('button', { name: 'Pricing route' }))
  vi.spyOn(api, 'get').mockRejectedValue(new Error('Catalog unavailable'))

  await act(async () => {
    await client.invalidateQueries({ queryKey: ['pricing'], exact: true })
  })
  expect(client.getQueryState(['pricing'])?.status).toBe('error')
  expect(screen.getByTitle('Infinite Canvas')).toBe(frame)
  expect(frame).not.toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  expect(screen.getByTitle('Infinite Canvas')).toBe(frame)
  expect(frame).toBeVisible()
  expect(
    screen.queryByText('Failed to load canvas models')
  ).not.toBeInTheDocument()
})

it('restores the sidebar on leaving the viewport and collapses it on returning', async () => {
  const user = userEvent.setup()
  const view = renderRoutes()
  const sidebar = view.container.querySelector('[data-slot="sidebar"]')
  expect(sidebar).toHaveAttribute('data-state', 'expanded')
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  await screen.findByTitle('Infinite Canvas')
  expect(sidebar).toHaveAttribute('data-state', 'collapsed')

  await user.click(screen.getByRole('button', { name: 'Logs route' }))
  expect(sidebar).toHaveAttribute('data-state', 'expanded')
  await user.click(screen.getByRole('button', { name: 'Toggle Sidebar' }))
  expect(sidebar).toHaveAttribute('data-state', 'collapsed')
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  expect(sidebar).toHaveAttribute('data-state', 'collapsed')
  await user.click(screen.getByRole('button', { name: 'Logs route' }))
  expect(sidebar).toHaveAttribute('data-state', 'collapsed')
  await user.click(screen.getByRole('button', { name: 'Toggle Sidebar' }))
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  expect(sidebar).toHaveAttribute('data-state', 'collapsed')
})

it('restores the expanded sidebar after a delayed navigation leaves the real canvas layout', async () => {
  const user = userEvent.setup()
  document.cookie = 'sidebar_state=true; path=/'
  vi.stubGlobal('scrollTo', vi.fn())
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data: '' } })
  let finishLoading!: () => void
  const loading = new Promise<void>((resolve) => {
    finishLoading = resolve
  })
  const root = createRootRoute()
  const authenticated = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
    component: AuthenticatedLayout,
  })
  const canvas = createRoute({
    getParentRoute: () => authenticated,
    path: 'canvas/',
    component: CanvasViewport,
  })
  const logs = createRoute({
    getParentRoute: () => authenticated,
    path: 'usage-logs/common',
    loader: () => loading,
    component: () => <main>Delayed logs page</main>,
  })
  const router = createRouter({
    routeTree: root.addChildren([authenticated.addChildren([canvas, logs])]),
    history: createMemoryHistory({ initialEntries: ['/canvas'] }),
    defaultPendingMs: Infinity,
  })
  await router.load()
  const view = renderRoutes(
    <DirectionProvider>
      <RouterProvider router={router} />
    </DirectionProvider>
  )
  const sidebar = view.container.querySelector('[data-slot="sidebar"]')
  try {
    expect(sidebar).toHaveAttribute('data-state', 'collapsed')
    await user.click(screen.getByRole('link', { name: 'Usage Logs' }))
    expect(router.state.location.pathname).toBe('/usage-logs/common')
    expect(router.state.matches.at(-1)?.routeId).toBe(canvas.id)
    expect(sidebar).toHaveAttribute('data-state', 'collapsed')

    await act(async () => finishLoading())
    expect(await screen.findByText('Delayed logs page')).toBeVisible()
    expect(sidebar).toHaveAttribute('data-state', 'expanded')
  } finally {
    finishLoading()
  }
})

it('aborts pending media on logout and requires another visit after signing in again', async () => {
  const user = userEvent.setup()
  renderRoutes()
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  const frame = (await screen.findByTitle(
    'Infinite Canvas'
  )) as HTMLIFrameElement
  const bridge = await connectEditor(frame)
  const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(
    (_input, init) =>
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener(
          'abort',
          () => reject(init.signal?.reason),
          {
            once: true,
          }
        )
      })
  )
  const pending = bridge.fetch('/pg/images/generations', {
    method: 'POST',
    body: JSON.stringify({ model: 'gpt-image-1', prompt: 'A paper boat' }),
  })
  const aborted = expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  await waitFor(() => expect(fetch).toHaveBeenCalledOnce())
  const signal = fetch.mock.calls[0][1]?.signal
  expect(signal?.aborted).toBe(false)
  await user.click(screen.getByRole('button', { name: 'Pricing route' }))
  expect(signal?.aborted).toBe(false)

  act(() => useAuthStore.getState().auth.reset())
  expect(signal?.aborted).toBe(true)
  await aborted
  expect(frame).not.toBeInTheDocument()
  expect(screen.queryByTitle('Infinite Canvas')).not.toBeInTheDocument()
  act(() => signIn(42))
  expect(screen.queryByTitle('Infinite Canvas')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  expect(await screen.findByTitle('Infinite Canvas')).not.toBe(frame)
})

it('gives a different account a new iframe without inheriting the previous visit', async () => {
  const user = userEvent.setup()
  renderRoutes()
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  const frame = (await screen.findByTitle(
    'Infinite Canvas'
  )) as HTMLIFrameElement
  const editor = frame.contentWindow
  const bridge = await connectEditor(frame)
  await user.click(screen.getByRole('button', { name: 'Pricing route' }))

  act(() => signIn(99))
  expect(frame).not.toBeInTheDocument()
  expect(screen.queryByTitle('Infinite Canvas')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Canvas route' }))
  const replacement = (await screen.findByTitle(
    'Infinite Canvas'
  )) as HTMLIFrameElement
  expect(replacement).not.toBe(frame)
  expect(Object.is(replacement.contentWindow, editor)).toBe(false)
  const nextBridge = await connectEditor(replacement)
  expect(nextBridge.context.userId).toBe(99)
  expect(nextBridge).not.toBe(bridge)
})
