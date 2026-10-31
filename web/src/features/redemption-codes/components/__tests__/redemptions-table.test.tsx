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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import type { Redemption } from '../../types'
import { RedemptionUser } from '../redemption-user'
import { RedemptionsProvider } from '../redemptions-provider'
import { RedemptionsTable } from '../redemptions-table'

const redemption: Redemption = {
  id: 12,
  user_id: 1,
  name: 'Monthly credit',
  key: 'a0b1c2d3e4f56789a0b1c2d3e4f56789',
  status: 3,
  quota: 100,
  created_time: 1,
  redeemed_time: 2,
  expired_time: 0,
  used_user_id: 42,
  used_username: 'alice',
  used_user_remark: 'Long-term customer',
}
const clients: QueryClient[] = []

beforeEach(() => {
  localStorage.clear()
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
})

async function renderTable(initialEntry = '/redemption-codes/') {
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    const params = new URL(String(url), 'http://localhost').searchParams
    return {
      data: {
        success: true,
        data: { items: [redemption], total: params.get('keyword') ? 1 : 40 },
      },
    }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const codesRoute = createRoute({
    getParentRoute: () => auth,
    path: 'redemption-codes/',
    component: () => (
      <RedemptionsProvider>
        <RedemptionsTable />
      </RedemptionsProvider>
    ),
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([codesRoute])]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByText(redemption.name)
  return get
}

test('pasting a redemption code searches the server on page one with the selected status', async () => {
  const get = await renderTable(
    '/redemption-codes/?page=2&pageSize=20&status=%5B%223%22%5D'
  )
  const user = userEvent.setup()
  const search = screen.getByPlaceholderText('Filter by code, name or ID...')
  await user.click(search)
  await user.paste(redemption.key)

  await waitFor(() => {
    const request = get.mock.calls.at(-1)
    expect(request).toBeDefined()
    const url = new URL(String(request?.[0]), 'http://localhost')
    expect(url.pathname).toBe('/api/redemption/search')
    expect(url.searchParams.get('keyword')).toBe(redemption.key)
    expect(url.searchParams.get('p')).toBe('1')
    expect(url.searchParams.get('status')).toBe('3')
  })
  expect(screen.getByText(redemption.name)).toBeVisible()

  await user.clear(search)
  await waitFor(() => {
    const url = new URL(String(get.mock.calls.at(-1)?.[0]), 'http://localhost')
    expect(url.searchParams.get('keyword')).toBe('')
    expect(url.searchParams.get('status')).toBe('3')
  })
})

test.each([false, true])(
  'redeemed codes display the user number, username and remark when mobile is %s',
  async (mobile) => {
    if (mobile) {
      const matchMedia = window.matchMedia.bind(window)
      vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
        ...matchMedia(query),
        matches: query === '(max-width: 640px)' || matchMedia(query).matches,
      }))
    }

    await renderTable()

    expect(
      screen.getByText('User 42 · alice · Long-term customer')
    ).toBeVisible()
  }
)

test('missing user details fall back to the number and unused codes show a dash', () => {
  render(
    <>
      <RedemptionUser
        redemption={{ ...redemption, used_username: '', used_user_remark: '' }}
      />
      <RedemptionUser redemption={{ ...redemption, used_user_id: 0 }} />
    </>
  )

  expect(screen.getByText('User 42')).toBeVisible()
  expect(screen.getByText('-')).toBeVisible()
  expect(screen.queryByText(/alice/)).not.toBeInTheDocument()
})

test('keyboard focus reveals the complete long remark and redemption time', async () => {
  const remark = 'Customer with a detailed administrator note. '.repeat(5)
  render(
    <RedemptionUser redemption={{ ...redemption, used_user_remark: remark }} />
  )

  await userEvent.setup().tab()

  expect(await screen.findByText(`Remark: ${remark.trim()}`)).toBeVisible()
  expect(screen.getByText('Username: alice')).toBeVisible()
  expect(screen.getByText(/^Redeemed at:/)).toBeVisible()
})
