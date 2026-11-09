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
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { buildApiParams } from '../../lib/utils'
import { CommonLogsFilterBar } from '../common-logs-filter-bar'
import { UsageLogsProvider } from '../usage-logs-provider'

const pointerCaptureDescriptor = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'setPointerCapture'
)

beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', {
    configurable: true,
    value: vi.fn(),
  })
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

function FilterFixture() {
  const table = useReactTable({
    data: [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <UsageLogsProvider>
      <CommonLogsFilterBar table={table} />
    </UsageLogsProvider>
  )
}

async function renderFilter(initialEntry = '/usage-logs/common?page=3') {
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url === '/api/user/self/groups' ? {} : { quota: 0, rpm: 0, tpm: 0 },
    },
  }))
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
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByRole('combobox', { name: 'Request result' })
  return router
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
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

it('applies error filters across log types and sends them to statistics', async () => {
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
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      expect.stringMatching(
        /\/api\/log\/self\/stat\?.*error_keyword=blocked\+by\+policy&error_code=cyber_policy&request_result=failed/
      )
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
