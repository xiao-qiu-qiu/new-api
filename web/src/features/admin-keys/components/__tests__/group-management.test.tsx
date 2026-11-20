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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { TooltipProvider } from '@/components/ui/tooltip'
import { api } from '@/lib/api'
import { Route as AdminKeysRoute } from '@/routes/_authenticated/admin/keys/index'
import { useAuthStore } from '@/stores/auth-store'

import { AdminApiKeys } from '../../index'
import type { AdminApiKey } from '../../types'
import { AdminApiKeyGroupDialog } from '../admin-api-key-group-dialog'
import { AdminApiKeysTable } from '../admin-api-keys-table'

const clients: QueryClient[] = []
const key: AdminApiKey = {
  id: 21,
  user_id: 7,
  username: 'alice',
  display_name: 'Alice',
  user_group: 'default',
  editable: true,
  name: 'production',
  key: 'abcd************************wxyz',
  status: 1,
  group: 'legacy',
  auto_groups: null,
  cross_group_retry: false,
  created_time: 0,
  accessed_time: 0,
  expired_time: -1,
}

function Providers(props: { children: React.ReactNode }) {
  const [client] = clients.slice(-1)
  return (
    <QueryClientProvider client={client}>
      <TooltipProvider>{props.children}</TooltipProvider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  localStorage.clear()
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.spyOn(toast, 'success').mockImplementation(() => 'success')
  vi.spyOn(toast, 'error').mockImplementation(() => 'error')
  clients.push(
    new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
  )
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
  useAuthStore.getState().auth.reset()
})

async function renderTable(
  options: { entry?: string; keys?: AdminApiKey[] } = {}
) {
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/token/admin/groups') {
      return {
        data: {
          success: true,
          data: { groups: ['default', 'premium'], max_count: 3 },
        },
      }
    }
    return {
      data: { success: true, data: { items: options.keys ?? [key], total: 1 } },
    }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const keys = createRoute({
    getParentRoute: () => auth,
    path: 'admin/keys/',
    validateSearch: AdminKeysRoute.options.validateSearch,
    component: AdminApiKeysTable,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([keys])]),
    history: createMemoryHistory({
      initialEntries: [options.entry ?? '/admin/keys/'],
    }),
  })
  await router.load()
  render(
    <Providers>
      <RouterProvider router={router} />
    </Providers>
  )
  await waitFor(() => expect(get).toHaveBeenCalled())
  return { get, router }
}

function renderDialog(keys = [key], groups = ['default', 'premium']) {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { groups, max_count: 3 } },
  })
  const onClose = vi.fn()
  const onSaved = vi.fn()
  render(
    <Providers>
      <AdminApiKeyGroupDialog keys={keys} onClose={onClose} onSaved={onSaved} />
    </Providers>
  )
  return { onClose, onSaved }
}

async function addGroup(
  user: ReturnType<typeof userEvent.setup>,
  group: string
) {
  const editor = await screen.findByRole('group', { name: 'Selected groups' })
  await user.click(within(editor).getByRole('combobox', { name: 'Add group' }))
  await user.click(await screen.findByRole('option', { name: group }))
}

it('restores owner and legacy group filters from the URL and searches all users on the server', async () => {
  const { get } = await renderTable({
    entry: '/admin/keys/?filter=alice&user_id=7&group=legacy',
  })
  await screen.findByText('production')
  expect(get).toHaveBeenCalledWith('/api/token/admin', {
    params: {
      p: 1,
      page_size: 20,
      keyword: 'alice',
      user_id: '7',
      group: 'legacy',
    },
  })
  expect(screen.getByRole('textbox', { name: 'User ID' })).toHaveValue('7')
  expect(
    screen.getByRole('textbox', { name: 'Filter by group...' })
  ).toHaveValue('legacy')
  expect(screen.getByText(key.key)).toBeVisible()
  expect(screen.getByText('alice')).toBeVisible()
})

it('clears the selected keys when the group filter changes so bulk edits stay on the visible results', async () => {
  const user = userEvent.setup()
  const { get } = await renderTable()
  await screen.findByText('production')
  await user.click(screen.getByRole('checkbox', { name: 'Select row' }))
  expect(screen.getByRole('toolbar', { name: /Bulk actions/ })).toBeVisible()
  await user.type(
    screen.getByRole('textbox', { name: 'Filter by group...' }),
    'legacy'
  )
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/token/admin', {
      params: expect.objectContaining({ group: 'legacy', p: 1 }),
    })
  )
  expect(
    screen.queryByRole('toolbar', { name: /Bulk actions/ })
  ).not.toBeInTheDocument()
})

it('restores the displayed search and clears selection when navigating between keyword URLs', async () => {
  const user = userEvent.setup()
  const { get, router } = await renderTable({
    entry: '/admin/keys/?filter=alice',
  })
  await screen.findByText('production')
  await user.click(screen.getByRole('checkbox', { name: 'Select row' }))
  await act(async () => {
    router.history.push('/admin/keys/?filter=bob')
    await router.load()
  })
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/token/admin', {
      params: expect.objectContaining({ keyword: 'bob' }),
    })
  )
  expect(
    screen.getByPlaceholderText('Search key name, username, or user ID...')
  ).toHaveValue('bob')
  expect(
    screen.queryByRole('toolbar', { name: /Bulk actions/ })
  ).not.toBeInTheDocument()
  await act(async () => {
    router.history.back()
  })
  await waitFor(() =>
    expect(
      screen.getByPlaceholderText('Search key name, username, or user ID...')
    ).toHaveValue('alice')
  )
  expect(screen.getByRole('checkbox', { name: 'Select row' })).toHaveAttribute(
    'aria-checked',
    'false'
  )
})

it('shows higher-role keys but disables their selection and group changes', async () => {
  await renderTable({ keys: [{ ...key, editable: false }] })
  await screen.findByText('production')
  expect(screen.getByRole('checkbox', { name: 'Select row' })).toHaveAttribute(
    'aria-disabled',
    'true'
  )
  expect(screen.getByRole('button', { name: 'Change group' })).toBeDisabled()
})

it('keeps higher-role selection and edits disabled in the mobile list', async () => {
  const originalMatchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...originalMatchMedia(query),
    matches:
      query.includes('max-width') || query.includes('prefers-reduced-motion'),
  }))
  await renderTable({ keys: [{ ...key, editable: false }] })
  await screen.findByText('production')
  expect(
    screen.getByRole('checkbox', { name: 'Select row 1' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('button', { name: 'Change group' })).toBeDisabled()
})

it('shows the batch group action after selecting a key on a narrow screen', async () => {
  const user = userEvent.setup()
  const originalMatchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...originalMatchMedia(query),
    matches:
      query.includes('max-width') || query.includes('prefers-reduced-motion'),
  }))
  await renderTable()
  await screen.findByText('production')
  await user.click(screen.getByRole('checkbox', { name: 'Select row 1' }))
  const toolbar = screen.getByRole('toolbar', { name: /Bulk actions/ })
  await user.click(
    within(toolbar).getByRole('button', { name: 'Change group' })
  )
  expect(
    await screen.findByRole('dialog', { name: 'Change API key groups' })
  ).toBeVisible()
})

it('offers a retry after listing fails and displays the recovered result', async () => {
  const user = userEvent.setup()
  const { get } = await renderTable({ keys: [] })
  get.mockRejectedValueOnce(new Error('offline'))
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  await screen.findByText('Failed to load API keys')
  get.mockResolvedValue({
    data: { success: true, data: { items: [key], total: 1 } },
  })
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('production')).toBeVisible()
})

it('prevents ordinary users from rendering the administration screen or requesting any keys', () => {
  useAuthStore.getState().auth.setUser({ id: 7, username: 'alice', role: 1 })
  const get = vi.spyOn(api, 'get')
  render(
    <Providers>
      <AdminApiKeys />
    </Providers>
  )
  expect(screen.getByText('Access Forbidden')).toBeVisible()
  expect(get).not.toHaveBeenCalled()
})

it('confirms and sends only the selected key IDs and group settings for a batch change', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, data: { count: 2 } } })
  const { onClose, onSaved } = renderDialog([
    key,
    { ...key, id: 22, user_id: 8, username: 'bob' },
  ])
  await addGroup(user, 'premium')
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  const confirmation = await screen.findByRole('alertdialog')
  expect(put).not.toHaveBeenCalled()
  await user.click(
    within(confirmation).getByRole('button', { name: 'Save changes' })
  )
  await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
  expect(put).toHaveBeenCalledWith('/api/token/admin/group', {
    ids: [21, 22],
    group: 'premium',
    auto_groups: [],
    cross_group_retry: false,
  })
  expect(onClose).toHaveBeenCalledOnce()
  expect(toast.success).toHaveBeenCalledWith('Updated groups for 2 API keys')
})

it('keeps the current group first when adding another group to a single-group key', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, data: { count: 1 } } })
  renderDialog([{ ...key, group: 'default' }])
  expect(
    await screen.findByRole('button', { name: 'Remove default' })
  ).toBeVisible()
  expect(
    screen.queryByRole('switch', { name: 'Cross-group retry' })
  ).not.toBeInTheDocument()
  await addGroup(user, 'premium')
  expect(
    screen.getByRole('switch', { name: 'Cross-group retry' })
  ).not.toBeChecked()
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  const confirmation = await screen.findByRole('alertdialog')
  await user.click(
    within(confirmation).getByRole('button', { name: 'Save changes' })
  )
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/token/admin/group', {
      ids: [21],
      group: 'auto',
      auto_groups: ['default', 'premium'],
      cross_group_retry: false,
    })
  )
})

it('saves the edited order and preserves retry for an existing multi-group key', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, data: { count: 1 } } })
  renderDialog([
    {
      ...key,
      group: 'auto',
      auto_groups: ['premium', 'default'],
      cross_group_retry: true,
    },
  ])
  await screen.findByRole('button', { name: 'Move premium down' })
  await user.click(screen.getByRole('button', { name: 'Move premium down' }))
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  const confirmation = await screen.findByRole('alertdialog')
  await user.click(
    within(confirmation).getByRole('button', { name: 'Save changes' })
  )
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/token/admin/group', {
      ids: [21],
      group: 'auto',
      auto_groups: ['default', 'premium'],
      cross_group_retry: true,
    })
  )
})

it('saves a single group and clears cross-group retry after removing a group', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, data: { count: 1 } } })
  renderDialog([
    {
      ...key,
      group: 'auto',
      auto_groups: ['premium', 'default'],
      cross_group_retry: true,
    },
  ])
  expect(
    await screen.findByRole('switch', { name: 'Cross-group retry' })
  ).toBeChecked()
  await user.click(screen.getByRole('button', { name: 'Remove default' }))
  expect(
    screen.queryByRole('switch', { name: 'Cross-group retry' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  const confirmation = await screen.findByRole('alertdialog')
  await user.click(
    within(confirmation).getByRole('button', { name: 'Save changes' })
  )
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/token/admin/group', {
      ids: [21],
      group: 'premium',
      auto_groups: [],
      cross_group_retry: false,
    })
  )
})

it('blocks saving after the only selected group is removed', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, data: { count: 1 } } })
  renderDialog([{ ...key, group: 'default' }])
  await user.click(
    await screen.findByRole('button', { name: 'Remove default' })
  )
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(
    await screen.findByText('Select at least one group for this token')
  ).toBeVisible()
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(put).not.toHaveBeenCalled()
})

it('blocks saving when no shared group is available for the selected owners', async () => {
  const put = vi.spyOn(api, 'put')
  renderDialog([key], [])
  expect(
    await screen.findByText('These users have no groups in common.')
  ).toBeVisible()
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled()
  expect(put).not.toHaveBeenCalled()
})

it('requires removal of an unavailable Auto group before confirmation can open', async () => {
  const user = userEvent.setup()
  const put = vi.spyOn(api, 'put')
  renderDialog([{ ...key, group: 'auto', auto_groups: ['legacy', 'premium'] }])
  await screen.findByRole('button', { name: 'Remove legacy' })
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(
    await screen.findByText('Remove unavailable groups before saving.')
  ).toBeVisible()
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(put).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Remove legacy' }))
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  expect(await screen.findByRole('alertdialog')).toBeVisible()
})

it('disables saving while owner groups are unavailable and allows retry after they fail to load', async () => {
  const user = userEvent.setup()
  let rejectGroups: (reason: Error) => void = () => {}
  const get = vi.spyOn(api, 'get').mockImplementation(
    () =>
      new Promise((_resolve, reject) => {
        rejectGroups = reject
      })
  )
  render(
    <Providers>
      <AdminApiKeyGroupDialog
        keys={[key]}
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />
    </Providers>
  )
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled()
  rejectGroups(new Error('offline'))
  expect(await screen.findByText('Failed to load groups')).toBeVisible()
  get.mockResolvedValue({
    data: { success: true, data: { groups: ['premium'], max_count: 3 } },
  })
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  const editor = await screen.findByRole('group', { name: 'Selected groups' })
  expect(
    within(editor).getByRole('combobox', { name: 'Add group' })
  ).toBeEnabled()
})

it('keeps the editor open and reports the server error when an update is rejected', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: false, message: 'Group is no longer available' },
  })
  const { onClose, onSaved } = renderDialog()
  await user.click(await screen.findByRole('button', { name: 'Remove legacy' }))
  await addGroup(user, 'premium')
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  const confirmation = await screen.findByRole('alertdialog')
  await user.click(
    within(confirmation).getByRole('button', { name: 'Save changes' })
  )
  await waitFor(() =>
    expect(toast.error).toHaveBeenCalledWith('Group is no longer available')
  )
  expect(onClose).not.toHaveBeenCalled()
  expect(onSaved).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Remove premium' })).toBeVisible()
})
