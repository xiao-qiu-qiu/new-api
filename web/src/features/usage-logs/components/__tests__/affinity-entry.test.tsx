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

import { ChannelsDialogs } from '@/features/channels/components/channels-dialogs'
import { ChannelsProvider } from '@/features/channels/components/channels-provider'
import { DataTableRowActions } from '@/features/channels/components/data-table-row-actions'
import { channelSchema } from '@/features/channels/types'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { UsageLogs } from '../..'
import { usageLogSchema } from '../../data/schema'

const originalAuth = useAuthStore.getState().auth
let client: QueryClient

const affinity = {
  binding_id: 'binding-opaque-id',
  rule_name: 'conversation',
  using_group: 'friends',
  key_fp: 'session-fingerprint',
}
const log = usageLogSchema.parse({
  id: 1,
  user_id: 2,
  token_id: 31,
  created_at: 1793580000,
  type: 2,
  content: '',
  model_name: 'gpt-5',
  username: 'friend',
  channel: 17,
  channel_name: 'Previous upstream',
  other: JSON.stringify({ admin_info: { channel_affinity: affinity } }),
})
const binding = {
  id: affinity.binding_id,
  version: 'binding-version',
  channel_id: 23,
  channel_name: 'Sub2API',
  user_id: log.user_id,
  user_name: log.username,
  token_id: log.token_id,
  token_name: 'Friends token',
  rule_name: affinity.rule_name,
  using_group: affinity.using_group,
  models: [log.model_name],
  session_mode: 'fallback',
  expires_at: 1793600000,
  last_seen_at: log.created_at,
  shared: false,
  key_fp: affinity.key_fp,
}

function setChannelAccess(read: boolean) {
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'operator',
    role: ROLE.ADMIN,
    permissions: { admin_permissions: { channel: { read, write: false } } },
  })
}

function mockRequests(logEntry = log) {
  return vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/channel/affinity/bindings') {
      return {
        data: {
          success: true,
          data: { items: [binding], total: 1, page: 1, page_size: 20 },
        },
      }
    }
    if (url.startsWith('/api/log?')) {
      return {
        data: { success: true, data: { items: [logEntry], total: 1 } },
      }
    }
    if (url === '/api/log/channel_affinity_usage_cache') {
      return { data: { success: true, data: { hit: 3, total: 4 } } }
    }
    if (url === '/api/status' || url.startsWith('/api/log/stat?')) {
      return { data: { success: true, data: {} } }
    }
    return { data: { success: true, data: [] } }
  })
}

async function renderLogs(mobile: boolean) {
  const originalMatchMedia = window.matchMedia.bind(window)
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...originalMatchMedia(query),
    matches:
      query === '(max-width: 640px)'
        ? mobile
        : originalMatchMedia(query).matches,
  }))
  const root = createRootRoute()
  const authenticated = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
  })
  const logs = createRoute({
    getParentRoute: () => authenticated,
    path: 'usage-logs/$section',
    component: UsageLogs,
  })
  const router = createRouter({
    routeTree: root.addChildren([authenticated.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: ['/usage-logs/common'] }),
  })
  await router.load()
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

function ChannelEntry() {
  const channel = channelSchema.parse({
    id: 17,
    type: 1,
    name: 'Previous upstream',
    key: '',
    status: 1,
    created_time: 1,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
  })
  const table = useReactTable({
    data: [channel],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <>
      <DataTableRowActions row={table.getRowModel().rows[0]} />
      <ChannelsDialogs />
    </>
  )
}

beforeEach(() => {
  localStorage.clear()
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  setChannelAccess(true)
})

afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.setState({ auth: originalAuth })
  localStorage.clear()
  vi.restoreAllMocks()
})

it.each([false, true])(
  'opens current affinity from a historical log without filtering by its previous channel (mobile: %s)',
  async (mobile) => {
    const get = mockRequests()
    const user = userEvent.setup()
    await renderLogs(mobile)
    const entry = await screen.findByRole('button', { name: 'Manage affinity' })
    entry.focus()
    await user.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog', {
      name: /Manage affinity/,
    })
    expect(await within(dialog).findByText(/Sub2API/)).toBeVisible()
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        '/api/channel/affinity/bindings',
        expect.objectContaining({
          params: expect.objectContaining({ binding_id: affinity.binding_id }),
        })
      )
    )
    const request = get.mock.calls.find(
      ([url]) => url === '/api/channel/affinity/bindings'
    )
    expect(request?.[1]?.params).not.toHaveProperty('channel_id')
    await user.selectOptions(
      within(dialog).getByRole('combobox', { name: 'Operation scope' }),
      'token'
    )
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        '/api/channel/affinity/bindings',
        expect.objectContaining({
          params: expect.objectContaining({
            user_id: log.user_id,
            token_id: log.token_id,
          }),
        })
      )
    )
  }
)

it('uses the rule, group and fingerprint from a legacy log to find its binding', async () => {
  const get = mockRequests({
    ...log,
    other: JSON.stringify({
      admin_info: { channel_affinity: { ...affinity, binding_id: undefined } },
    }),
  })
  const user = userEvent.setup()
  await renderLogs(false)
  await user.click(
    await screen.findByRole('button', { name: 'Manage affinity' })
  )
  const dialog = await screen.findByRole('dialog', { name: /Manage affinity/ })
  expect(await within(dialog).findByText(/Sub2API/)).toBeVisible()
  expect(get).toHaveBeenCalledWith(
    '/api/channel/affinity/bindings',
    expect.objectContaining({
      params: expect.objectContaining({
        rule_name: affinity.rule_name,
        using_group: affinity.using_group,
        key_fp: affinity.key_fp,
        user_id: log.user_id,
        token_id: log.token_id,
      }),
    })
  )
})

it.each([false, true])(
  'keeps the statistics dialog when channel read permission is missing (mobile: %s)',
  async (mobile) => {
    setChannelAccess(false)
    const get = mockRequests()
    const user = userEvent.setup()
    await renderLogs(mobile)
    await user.click(
      await screen.findByRole('button', { name: 'Channel Affinity' })
    )
    const dialog = await screen.findByRole('dialog', {
      name: 'Channel Affinity: Upstream Cache Hit',
    })
    expect(await within(dialog).findByText('3/4 (75.00%)')).toBeVisible()
    expect(get).not.toHaveBeenCalledWith(
      '/api/channel/affinity/bindings',
      expect.anything()
    )
    expect(
      screen.queryByRole('button', { name: 'Manage affinity' })
    ).not.toBeInTheDocument()
  }
)

it.each([false, true])(
  'shows the channel management menu only with channel read permission (read: %s)',
  async (read) => {
    setChannelAccess(read)
    const get = mockRequests()
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={client}>
        <ChannelsProvider>
          <ChannelEntry />
        </ChannelsProvider>
      </QueryClientProvider>
    )
    await user.click(screen.getByRole('button', { name: 'Open menu' }))
    if (!read) {
      expect(
        screen.queryByRole('menuitem', { name: 'Manage affinity' })
      ).not.toBeInTheDocument()
      return
    }
    await user.click(
      await screen.findByRole('menuitem', { name: 'Manage affinity' })
    )
    expect(
      await screen.findByRole('dialog', { name: /Manage affinity/ })
    ).toBeVisible()
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        '/api/channel/affinity/bindings',
        expect.objectContaining({
          params: expect.objectContaining({ channel_id: 17 }),
        })
      )
    )
  }
)
