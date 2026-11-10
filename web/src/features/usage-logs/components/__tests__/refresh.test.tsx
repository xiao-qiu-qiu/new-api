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
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { usageLogSchema } from '../../data/schema'
import { UsageLogsProvider, useLogsViewScope } from '../usage-logs-provider'
import { UsageLogsTable } from '../usage-logs-table'

let client: QueryClient
const log = usageLogSchema.parse({
  id: 1,
  user_id: 1,
  created_at: 100,
  type: 2,
  content: '',
  request_id: 'old',
  model_name: 'old-model',
})

beforeEach(() => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'reader', role: 1 })
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  localStorage.clear()
})
afterEach(() => {
  cleanup()
  client?.clear()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.setUser(null)
})

async function renderLogs(
  gate: () => Promise<void>,
  rows: () => (typeof log)[],
  initialEntry = '/usage-logs/common'
) {
  const requests = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url.startsWith('/api/log/self?') || url.startsWith('/api/log?')) {
      await gate()
      return {
        data: {
          success: true,
          data: {
            items: rows(),
            total: rows().length,
            page: 1,
            page_size: 100,
          },
        },
      }
    }
    if (url.startsWith('/api/log/self/active')) {
      return {
        data: {
          success: true,
          data: { items: [], total: 0, server_time: Date.now() },
        },
      }
    }
    let extraData: unknown = { quota: 0, rpm: 0, tpm: 0, subscriptions: [] }
    if (url === '/api/group/') extraData = []
    else if (url.includes('groups')) extraData = {}
    return {
      data: {
        success: true,
        data: extraData,
      },
    }
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logsRoute = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: () => (
      <UsageLogsProvider>
        <UsageLogsTable logCategory='common' />
        <ScopeSwitcher />
      </UsageLogsProvider>
    ),
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logsRoute])]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByText('old-model')
  return { router, requests }
}

function ScopeSwitcher() {
  const { setViewScope } = useLogsViewScope()
  return (
    <button type='button' onClick={() => setViewScope('self')}>
      Self scope
    </button>
  )
}

it('keeps the table interactive and the search button idle during a background refresh', async () => {
  let release: (() => void) | undefined
  let gate = Promise.resolve()
  await renderLogs(
    () => gate,
    () => [log]
  )
  gate = new Promise<void>((resolve) => {
    release = resolve
  })
  let refresh = Promise.resolve()
  act(() => {
    refresh = client.invalidateQueries({ queryKey: ['logs'] })
  })
  await waitFor(() => expect(client.isFetching({ queryKey: ['logs'] })).toBe(1))
  expect(screen.getByRole('button', { name: 'Search' })).toBeEnabled()
  const table = screen.getByRole('table')
  expect(table).toHaveStyle({ tableLayout: 'fixed' })
  expect(table.closest('.pointer-events-none')).toBeNull()
  expect(table.closest('.opacity-60')).toBeNull()
  await act(async () => {
    release?.()
    await refresh
  })
})

it('keeps the visible rows while scrolled down and applies new logs on demand', async () => {
  let rows = [log]
  await renderLogs(
    async () => {},
    () => rows
  )
  const scroller = screen.getByRole('table').parentElement
  if (!scroller) throw new Error('Missing table scroller')
  fireEvent.scroll(scroller, { target: { scrollTop: 300 } })
  rows = [{ ...log, id: 2, request_id: 'new', model_name: 'new-model' }, log]
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['logs'] })
  })
  const updateButton = await screen.findByRole('button', {
    name: 'New logs available',
  })
  expect(screen.queryByText('new-model')).not.toBeInTheDocument()
  expect(screen.getByText('old-model')).toBeVisible()
  expect(scroller.scrollTop).toBe(300)
  await userEvent.setup().click(updateButton)
  expect(screen.getByText('new-model')).toBeVisible()
  expect(scroller.scrollTop).toBe(0)
})

it('replaces placeholder rows when a different search finishes while the reader scrolls', async () => {
  let release: (() => void) | undefined
  let gate = Promise.resolve()
  let rows = [log]
  const { router } = await renderLogs(
    () => gate,
    () => rows
  )
  gate = new Promise<void>((resolve) => {
    release = resolve
  })
  await act(async () => {
    await router.navigate({
      to: '/usage-logs/$section',
      params: { section: 'common' },
      search: { model: 'new-model' },
    })
  })
  await waitFor(() => expect(client.isFetching({ queryKey: ['logs'] })).toBe(1))
  const scroller = screen.getByRole('table').parentElement
  if (!scroller) throw new Error('Missing table scroller')
  fireEvent.scroll(scroller, { target: { scrollTop: 300 } })
  rows = [{ ...log, id: 2, request_id: 'new', model_name: 'new-model' }]
  await act(async () => {
    release?.()
  })
  expect(await screen.findByText('new-model')).toBeVisible()
  expect(screen.queryByText('old-model')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'New logs available' })
  ).not.toBeInTheDocument()
})

it('clears a cursor before requesting a different viewing scope', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  const { router, requests } = await renderLogs(
    async () => {},
    () => [log],
    '/usage-logs/common?page=2&cursor=admin-cursor&errorKeyword=%22403%22'
  )
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Self scope' }))
  await waitFor(() =>
    expect(router.state.location.search.cursor).toBeUndefined()
  )
  await waitFor(() =>
    expect(
      requests.mock.calls.some(([url]) => url.startsWith('/api/log/self?'))
    ).toBe(true)
  )
  const selfRequests = requests.mock.calls
    .map(([url]) => url)
    .filter((url) => url.startsWith('/api/log/self?'))
  expect(
    selfRequests.every(
      (url) => !new URL(url, 'http://localhost').searchParams.has('cursor')
    )
  ).toBe(true)
  expect(router.state.location.search.page).toBe(1)
})
