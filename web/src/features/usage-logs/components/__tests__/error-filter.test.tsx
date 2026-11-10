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
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import {
  cleanup,
  act,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { getLogStatsQueryKey } from '../../lib/log-query'
import { buildApiParams } from '../../lib/utils'
import { CommonLogsFilterBar } from '../common-logs-filter-bar'
import { UsageLogsProvider, useLogsViewScope } from '../usage-logs-provider'

let filterProps: {
  onRefresh?: () => Promise<unknown>
  isRefreshing?: boolean
} = {}
let selfOnly = false
let client: QueryClient

const pointerCaptureDescriptor = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'setPointerCapture'
)

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 10, 11, 12))
  filterProps = {}
  selfOnly = false
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', {
    configurable: true,
    value: vi.fn(),
  })
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

function FilterContents() {
  const { setViewScope } = useLogsViewScope()
  useEffect(() => {
    if (selfOnly) setViewScope('self')
  }, [setViewScope])
  const table = useReactTable({
    data: [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
  })
  return <CommonLogsFilterBar table={table} {...filterProps} />
}

function FilterFixture() {
  return (
    <UsageLogsProvider>
      <FilterContents />
    </UsageLogsProvider>
  )
}

async function renderFilter(initialEntry = '/usage-logs/common?page=3') {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = { quota: 0, rpm: 0, tpm: 0 }
    if (url === '/api/group/') data = []
    if (url === '/api/user/self/groups') data = {}
    return { data: { success: true, data } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: FilterFixture,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByRole('button', { name: 'Search' })
  return router
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.useRealTimers()
  client?.clear()
  useAuthStore.getState().auth.setUser(null)
  if (pointerCaptureDescriptor) {
    Object.defineProperty(
      HTMLElement.prototype,
      'setPointerCapture',
      pointerCaptureDescriptor
    )
  } else {
    Reflect.deleteProperty(HTMLElement.prototype, 'setPointerCapture')
  }
})

it('applies administrator error filters but only loads their statistics on demand', async () => {
  const router = await renderFilter()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Expand' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Error keyword' }),
    'blocked by policy'
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Error code (exact match)' }),
    'cyber_policy'
  )
  await user.click(screen.getByRole('combobox', { name: 'Request result' }))
  await user.click(screen.getByRole('option', { name: 'Failed requests' }))
  expect(router.state.location.search).not.toHaveProperty('errorKeyword')
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      page: 1,
      type: ['0'],
      errorKeyword: 'blocked by policy',
      errorCode: 'cyber_policy',
      requestResult: 'failed',
    })
  )
  expect(api.get).not.toHaveBeenCalledWith(
    expect.stringMatching(/\/stat\?.*error_keyword=/),
    expect.anything()
  )
  await user.click(screen.getByRole('button', { name: 'Load statistics' }))
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      expect.stringMatching(
        /\/api\/log\/stat\?.*error_keyword=blocked\+by\+policy&error_code=cyber_policy&request_result=failed/
      ),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
  )
  expect(
    buildApiParams({
      page: 1,
      pageSize: 20,
      searchParams: router.state.location.search,
      isAdmin: false,
    })
  ).toMatchObject({
    error_keyword: 'blocked by policy',
    error_code: 'cyber_policy',
    request_result: 'failed',
  })
  await user.click(screen.getByRole('button', { name: 'Reset' }))
  await waitFor(() => {
    expect(router.state.location.search).not.toHaveProperty('errorKeyword')
    expect(router.state.location.search).not.toHaveProperty('errorCode')
    expect(router.state.location.search).not.toHaveProperty('requestResult')
  })
  expect(screen.getByRole('textbox', { name: 'Error keyword' })).toHaveValue('')
  expect(
    screen.getByRole('textbox', { name: 'Error code (exact match)' })
  ).toHaveValue('')
  expect(
    screen.getByRole('combobox', { name: 'Request result' })
  ).toHaveTextContent('All request results')
})

it('hides expensive filters for an ordinary user on desktop', async () => {
  useAuthStore.getState().auth.setUser({ id: 2, username: 'viewer', role: 1 })
  await renderFilter()
  await userEvent.click(screen.getByRole('button', { name: 'Expand' }))
  expect(
    screen.queryByRole('combobox', { name: 'Request result' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('textbox', { name: 'Error keyword' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('textbox', { name: 'Error code (exact match)' })
  ).not.toBeInTheDocument()
})

it('keeps administrator error filters available in the only-mine view', async () => {
  selfOnly = true
  await renderFilter()
  await userEvent.click(screen.getByRole('button', { name: 'Expand' }))
  expect(screen.getByRole('combobox', { name: 'Request result' })).toBeVisible()
  expect(screen.getByRole('textbox', { name: 'Error keyword' })).toBeVisible()
})

it('hides expensive mobile filters while allowing an ordinary user to reset an old error-filter URL', async () => {
  const originalMatchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...originalMatchMedia(query),
    matches: query === '(max-width: 640px)',
  }))
  useAuthStore.getState().auth.setUser({ id: 2, username: 'viewer', role: 1 })
  const router = await renderFilter('/usage-logs/common?errorKeyword=blocked')
  await userEvent.click(screen.getByRole('button', { name: 'Filter' }))
  const dialog = await screen.findByRole('dialog', { name: 'Filter' })
  expect(
    within(dialog).queryByRole('combobox', { name: 'Request result' })
  ).not.toBeInTheDocument()
  expect(
    within(dialog).queryByRole('textbox', { name: 'Error keyword' })
  ).not.toBeInTheDocument()
  expect(
    within(dialog).queryByRole('textbox', { name: 'Error code (exact match)' })
  ).not.toBeInTheDocument()
  await userEvent.click(within(dialog).getByRole('button', { name: 'Reset' }))
  await waitFor(() =>
    expect(router.state.location.search).not.toHaveProperty('errorKeyword')
  )
})

it('rejects an error search over 24 hours without navigation or database queries', async () => {
  const end = Date.now()
  const router = await renderFilter(
    `/usage-logs/common?startTime=${end - 30 * 86400000}&endTime=${end}`
  )
  const invalidation = vi.spyOn(client, 'invalidateQueries')
  await userEvent.click(screen.getByRole('button', { name: 'Expand' }))
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Error keyword' }),
    '403'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Error searches are limited to 24 hours. Shorten the time range.'
  )
  expect(router.state.location.search).not.toHaveProperty('errorKeyword')
  expect(invalidation).not.toHaveBeenCalled()
})

it('navigates changed filters and clears the cursor without invalidating old queries', async () => {
  const router = await renderFilter(
    '/usage-logs/common?page=3&cursor=old-cursor&pageSize=50'
  )
  const invalidation = vi.spyOn(client, 'invalidateQueries')
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Model Name' }),
    'gpt'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      model: 'gpt',
      page: 1,
      pageSize: 50,
    })
  )
  expect(router.state.location.search).not.toHaveProperty('cursor')
  expect(invalidation).not.toHaveBeenCalled()
})

it('refreshes identical filters through the callback and only shows manual progress', async () => {
  let finish!: () => void
  filterProps.onRefresh = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve
      })
  )
  const start = new Date()
  start.setHours(0, 0, 0, 0)
  const end = new Date(start.getTime() + 3600000)
  await renderFilter(
    `/usage-logs/common?page=1&type=%5B%220%22%5D&startTime=${start.getTime()}&endTime=${end.getTime()}`
  )
  const button = screen.getByRole('button', { name: 'Search' })
  expect(button).toHaveAttribute('aria-busy', 'false')
  await userEvent.click(button)
  expect(filterProps.onRefresh).toHaveBeenCalledOnce()
  expect(button).toBeDisabled()
  expect(button).toHaveAttribute('aria-busy', 'true')
  finish()
  await waitFor(() => expect(button).toBeEnabled())
  expect(button).toHaveAttribute('aria-busy', 'false')
})

it('keeps background log fetches from disabling or animating the Search button', async () => {
  await renderFilter()
  let finish!: () => void
  let fetching!: Promise<boolean>
  act(() => {
    fetching = client.fetchQuery({
      queryKey: ['logs', 'background'],
      queryFn: () =>
        new Promise<boolean>((resolve) => {
          finish = () => resolve(true)
        }),
    })
  })
  const button = screen.getByRole('button', { name: 'Search' })
  expect(button).toBeEnabled()
  expect(button).toHaveAttribute('aria-busy', 'false')
  await act(async () => {
    finish()
    await fetching
  })
})

it('does not repeat statistics when only pagination or the cursor changes', async () => {
  const router = await renderFilter(
    '/usage-logs/common?page=1&startTime=1000&endTime=2000'
  )
  await screen.findByText('Usage')
  const statsCalls = () =>
    vi.mocked(api.get).mock.calls.filter(([url]) => url.includes('/stat?'))
  expect(statsCalls()).toHaveLength(1)
  await act(async () => {
    await router.navigate({
      to: '/usage-logs/$section',
      params: { section: 'common' },
      search: {
        page: 2,
        pageSize: 50,
        cursor: 'page-2',
        startTime: 1000,
        endTime: 2000,
      },
    })
  })
  expect(statsCalls()).toHaveLength(1)
})

it.each(['model', 'token', 'username', 'history', 'startOnly', 'endOnly'])(
  'does not automatically load statistics for an expensive %s query',
  async (filter) => {
    const now = Date.now()
    let query = `${filter}=partial-name&startTime=${now - 3600000}&endTime=${now}`
    if (filter === 'history') {
      query = `startTime=${now - 30 * 86400000}&endTime=${now}`
    }
    if (filter === 'startOnly') query = `startTime=${now - 30 * 86400000}`
    if (filter === 'endOnly') query = `endTime=${now}`
    await renderFilter(`/usage-logs/common?${query}`)
    const load = await screen.findByRole('button', { name: 'Load statistics' })
    expect(
      vi.mocked(api.get).mock.calls.filter(([url]) => url.includes('/stat?'))
    ).toHaveLength(0)
    await userEvent.click(load)
    await screen.findByText('Usage')
    expect(
      vi.mocked(api.get).mock.calls.filter(([url]) => url.includes('/stat?'))
    ).toHaveLength(1)
  }
)

it('does not display another viewer statistics while the next viewer is loading', async () => {
  const router = await renderFilter(
    '/usage-logs/common?startTime=1000&endTime=2000'
  )
  await screen.findByText('Usage')
  act(() => {
    client.setQueryData(
      getLogStatsQueryKey(router.state.location.search, true, 'admin', 1),
      { quota: 0, rpm: 123, tpm: 0 }
    )
  })
  expect(await screen.findByText('123')).toBeVisible()
  let finish!: (value: unknown) => void
  vi.mocked(api.get).mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  act(() => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'other-admin', role: 10 })
  })
  expect(screen.queryByText('123')).not.toBeInTheDocument()
  await act(async () => {
    finish({ data: { success: true, data: { quota: 0, rpm: 456, tpm: 0 } } })
  })
  expect(await screen.findByText('456')).toBeVisible()
})

it('does not retry a failed explicit statistics query', async () => {
  const end = Date.now()
  await renderFilter(
    `/usage-logs/common?errorKeyword=%22403%22&startTime=${end - 3600000}&endTime=${end}`
  )
  client.setDefaultOptions({ queries: { retry: 4 } })
  vi.mocked(api.get).mockRejectedValue(new Error('Query execution timed out'))
  await userEvent.click(screen.getByRole('button', { name: 'Load statistics' }))
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Load statistics' })
    ).toBeEnabled()
  )
  const statRequests = vi
    .mocked(api.get)
    .mock.calls.filter(([url]) => url.includes('/stat?'))
  expect(statRequests).toHaveLength(1)
})

it('restores saved error filters and updates them after navigating back', async () => {
  const router = await renderFilter(
    '/usage-logs/common?errorKeyword=blocked&errorCode=cyber_policy&requestResult=stream_error'
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: /Expand/ }))
  expect(screen.getByRole('textbox', { name: 'Error keyword' })).toHaveValue(
    'blocked'
  )
  expect(
    screen.getByRole('textbox', { name: 'Error code (exact match)' })
  ).toHaveValue('cyber_policy')
  expect(
    screen.getByRole('combobox', { name: 'Request result' })
  ).toHaveTextContent('Stream errors')
  await user.clear(screen.getByRole('textbox', { name: 'Error keyword' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Error keyword' }),
    'another error'
  )
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toHaveProperty(
      'errorKeyword',
      'another error'
    )
  )
  router.history.back()
  await waitFor(() =>
    expect(screen.getByRole('textbox', { name: 'Error keyword' })).toHaveValue(
      'blocked'
    )
  )
})

it('keeps mobile error filters pending until Search and closes the drawer after applying', async () => {
  const originalMatchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...originalMatchMedia(query),
    matches: query === '(max-width: 640px)',
  }))
  const rendering = renderFilter()
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: 'Filter' }))
  const router = await rendering
  const drawer = await screen.findByRole('dialog', { name: 'Filter' })
  await user.type(
    within(drawer).getByRole('textbox', { name: 'Error keyword' }),
    'blocked by policy'
  )
  await user.type(
    within(drawer).getByRole('textbox', { name: 'Error code (exact match)' }),
    'cyber_policy'
  )
  expect(router.state.location.search).not.toHaveProperty('errorKeyword')
  await user.click(within(drawer).getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      page: 1,
      errorKeyword: 'blocked by policy',
      errorCode: 'cyber_policy',
    })
  )
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Filter' })
    ).not.toBeInTheDocument()
  )
})
