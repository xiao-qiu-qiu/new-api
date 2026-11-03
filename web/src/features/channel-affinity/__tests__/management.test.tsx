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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CanceledError, type AxiosAdapter } from 'axios'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { ChannelAffinitySessions } from '..'
import { getAffinityBindings } from '../api'
import { ChannelAffinityManagerDialog } from '../manager-dialog'
import type {
  AffinityBinding,
  AffinityBindingFilters,
  AffinityBindingList,
} from '../types'

const binding: AffinityBinding = {
  id: 'a'.repeat(64),
  version: 'epoch-1',
  channel_id: 38,
  channel_name: 'Upstream',
  user_id: 7,
  user_name: 'friend',
  token_id: 77,
  token_name: 'desktop',
  rule_name: 'codex',
  using_group: 'default',
  models: ['gpt-test'],
  session_mode: 'strict',
  expires_at: 2000000000,
  last_seen_at: 1900000000,
  shared: false,
  ownership_known: true,
  metadata_complete: true,
  key_fp: 'session-a',
}
const second: AffinityBinding = {
  ...binding,
  id: 'b'.repeat(64),
  key_fp: 'session-b',
  version: 'epoch-2',
  token_id: 78,
}
const otherChannel: AffinityBinding = {
  ...second,
  channel_id: 39,
  channel_name: 'Other upstream',
}
const target = {
  binding_id: binding.id,
  rule_name: 'codex',
  using_group: 'default',
  key_fp: 'session-a',
  user_id: 7,
  token_id: 77,
}
const clients: QueryClient[] = []

function renderManager(
  props: Partial<React.ComponentProps<typeof ChannelAffinityManagerDialog>> = {}
) {
  return renderWithQueryClient(
    <ChannelAffinityManagerDialog open onOpenChange={vi.fn()} {...props} />
  )
}

function renderWithQueryClient(element: React.ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>{element}</QueryClientProvider>
  )
}

function listResponse(
  items: AffinityBinding[],
  pagination: Partial<Omit<AffinityBindingList, 'items'>> = {}
) {
  return {
    data: {
      success: true,
      data: {
        items,
        total: items.length,
        page: 1,
        page_size: 20,
        ...pagination,
      },
    },
  }
}

beforeEach(() => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'admin', role: ROLE.SUPER_ADMIN })
})

afterEach(() => {
  for (const client of clients.splice(0)) client.clear()
  useAuthStore.getState().auth.reset()
  vi.unstubAllGlobals()
})

describe('channel affinity management', () => {
  test('a narrow viewport shows the current channel and lifetime in a selectable card', async () => {
    vi.stubGlobal('innerWidth', 390)
    vi.spyOn(api, 'get').mockResolvedValue(listResponse([binding]))
    const user = userEvent.setup()
    renderManager({ channelId: 38 })
    await waitFor(() => {
      expect(screen.queryByRole('table')).not.toBeInTheDocument()
      expect(screen.getByText('Upstream #38')).toBeVisible()
      expect(screen.getByText('Binding lifetime')).toBeVisible()
    })
    await user.click(
      screen.getByRole('checkbox', { name: 'Select session session-a' })
    )
    expect(
      screen.getByRole('button', { name: 'Migrate affinity' })
    ).toBeEnabled()
  })
  test('a log opens the live binding without filtering by its historical channel, and reset requires confirmation', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue(
        listResponse([{ ...binding, channel_id: 3, channel_name: 'Sub2API' }])
      )
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          succeeded: 1,
          expired: 0,
          conflicted: 0,
          failed: 0,
          items: [],
        },
      },
    })
    const user = userEvent.setup()
    renderManager({ target })
    expect(await screen.findByText('Sub2API #3')).toBeVisible()
    expect(get).toHaveBeenCalledWith(
      '/api/channel/affinity/bindings',
      expect.objectContaining({
        params: { binding_id: binding.id, page: 1, page_size: 20 },
      })
    )
    await user.click(screen.getByRole('button', { name: 'Reset affinity' }))
    expect(post).not.toHaveBeenCalled()
    expect(
      within(screen.getByRole('alertdialog')).getByText(
        'The next request will select a channel again. It may select the same channel.'
      )
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(post).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Reset affinity' }))
    await user.click(screen.getByRole('button', { name: 'Confirm reset' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledExactlyOnceWith(
        '/api/channel/affinity/actions',
        {
          action: 'reset',
          bindings: [{ id: binding.id, version: 'epoch-1', channel_id: 3 }],
        }
      )
    )
  })

  test('channel migration sends only checked sessions to the selected eligible channel', async () => {
    vi.spyOn(api, 'get').mockResolvedValue(listResponse([binding, second]))
    const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
      if (url.endsWith('/targets')) {
        return {
          data: {
            success: true,
            data: { items: [{ id: 3, name: 'Sub2API' }] },
          },
        }
      }
      return {
        data: {
          success: true,
          data: {
            succeeded: 1,
            expired: 0,
            conflicted: 0,
            failed: 0,
            items: [],
          },
        },
      }
    })
    const user = userEvent.setup()
    renderManager({ channelId: 38, channelName: 'Upstream' })
    const checkbox = await screen.findByRole('checkbox', {
      name: 'Select session session-a',
    })
    checkbox.focus()
    await user.keyboard(' ')
    expect(checkbox).toBeChecked()
    expect(
      screen.getByRole('checkbox', { name: 'Select session session-b' })
    ).not.toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Migrate affinity' }))
    const confirm = screen.getByRole('button', { name: 'Confirm migration' })
    expect(confirm).toBeDisabled()
    await user.click(
      await screen.findByRole('combobox', { name: 'Target channel' })
    )
    await user.click(await screen.findByRole('option', { name: 'Sub2API #3' }))
    expect(confirm).toBeEnabled()
    await user.click(confirm)
    await waitFor(() =>
      expect(post).toHaveBeenLastCalledWith('/api/channel/affinity/actions', {
        action: 'migrate',
        target_channel_id: 3,
        bindings: [{ id: binding.id, version: 'epoch-1', channel_id: 38 }],
      })
    )
    expect(
      await screen.findByText(
        'Updated: 1 · Expired: 0 · Conflicts: 0 · Failed: 0'
      )
    ).toBeVisible()
  })

  test('expanding a log to its token clears the single-session filter and selection', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue(listResponse([binding]))
    const user = userEvent.setup()
    renderManager({ target })
    expect(
      await screen.findByRole('checkbox', { name: 'Select session session-a' })
    ).toBeChecked()
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Operation scope' }),
      'token'
    )
    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith(
        '/api/channel/affinity/bindings',
        expect.objectContaining({
          params: { user_id: 7, token_id: 77, page: 1, page_size: 20 },
        })
      )
    )
    expect(
      screen.getByRole('checkbox', { name: 'Select session session-a' })
    ).not.toBeChecked()
  })

  test('invalid owner filters do not query, and applying valid filters clears the selection', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue(listResponse([binding]))
    const user = userEvent.setup()
    renderManager({ channelId: 38 })
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select session session-a' })
    )
    await user.type(screen.getByLabelText('User ID'), '-7')
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    expect(await screen.findByText('Enter a positive ID')).toBeVisible()
    expect(get).toHaveBeenCalledTimes(1)
    await user.clear(screen.getByLabelText('User ID'))
    await user.type(screen.getByLabelText('User ID'), '7')
    await user.type(screen.getByLabelText('Token ID'), '77')
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith(
        '/api/channel/affinity/bindings',
        expect.objectContaining({
          params: {
            channel_id: 38,
            user_id: 7,
            token_id: 77,
            page: 1,
            page_size: 20,
          },
        })
      )
    )
    expect(
      screen.getByRole('checkbox', { name: 'Select session session-a' })
    ).not.toBeChecked()
  })

  test('an unavailable target list shows a retry and cannot submit a migration', async () => {
    vi.spyOn(api, 'get').mockResolvedValue(listResponse([binding]))
    const post = vi
      .spyOn(api, 'post')
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValue({ data: { success: true, data: { items: [] } } })
    const user = userEvent.setup()
    renderManager({ target })
    await screen.findByText('Upstream #38')
    await user.click(screen.getByRole('button', { name: 'Migrate affinity' }))
    expect(
      await screen.findByText('Could not load eligible channels')
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Confirm migration' })
    ).toBeDisabled()
    await user.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Retry',
      })
    )
    expect(
      await screen.findByText(
        'No channel is eligible for every selected session.'
      )
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Confirm migration' })
    ).toBeDisabled()
    expect(post.mock.calls.every(([url]) => url.endsWith('/targets'))).toBe(
      true
    )
  })

  test('a pending batch cannot be submitted twice and partial failures remain visible after refresh', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue(listResponse([binding, second]))
    let finish!: (value: unknown) => void
    const post = vi.spyOn(api, 'post').mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve
        })
    )
    const user = userEvent.setup()
    renderManager({ channelId: 38 })
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select current page' })
    )
    await user.click(screen.getByRole('button', { name: 'Reset affinity' }))
    await user.click(screen.getByRole('button', { name: 'Confirm reset' }))
    expect(screen.getByRole('button', { name: 'Confirm reset' })).toBeDisabled()
    get.mockResolvedValue(listResponse([second]))
    await act(async () =>
      finish({
        data: {
          success: true,
          data: {
            succeeded: 1,
            expired: 0,
            conflicted: 1,
            failed: 0,
            items: [
              {
                id: second.id,
                status: 'conflicted',
                message: 'Binding changed. Refresh and try again.',
              },
            ],
          },
        },
      })
    )
    expect(
      await screen.findByText(
        'Updated: 1 · Expired: 0 · Conflicts: 1 · Failed: 0'
      )
    ).toBeVisible()
    expect(
      screen.getByText('Binding changed. Refresh and try again.')
    ).toBeVisible()
    await waitFor(() =>
      expect(
        screen.queryByRole('checkbox', { name: 'Select session session-a' })
      ).not.toBeInTheDocument()
    )
    expect(post).toHaveBeenCalledTimes(1)
  })

  test('shared and unattributed bindings require individual selection and confirmation of their wider scope', async () => {
    vi.spyOn(api, 'get').mockResolvedValue(
      listResponse([
        { ...binding, shared: true },
        { ...second, user_id: 0, token_id: 0, ownership_known: false },
      ])
    )
    const user = userEvent.setup()
    renderManager({ channelId: 38 })
    const shared = await screen.findByRole('checkbox', {
      name: 'Select session session-a',
    })
    expect(shared).not.toBeChecked()
    expect(
      screen.getByRole('checkbox', { name: 'Select current page' })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByRole('button', { name: 'Reset affinity' })
    ).toBeDisabled()
    await user.click(shared)
    await user.click(screen.getByRole('button', { name: 'Reset affinity' }))
    expect(
      within(screen.getByRole('alertdialog')).getByText(
        'Shared or unattributed bindings are included. This changes the whole binding for everyone using it.'
      )
    ).toBeVisible()
  })

  test('empty or expired lookup shows no available action', async () => {
    vi.spyOn(api, 'get').mockResolvedValue(listResponse([]))
    renderManager({ target })
    expect(await screen.findByText('No active bindings found')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Migrate affinity' })
    ).toBeDisabled()
  })

  test('a log without binding identity never falls back to querying all bindings', () => {
    const get = vi.spyOn(api, 'get')
    renderManager({ target: { user_id: 7, token_id: 77 } })
    expect(screen.getByText('No active bindings found')).toBeInTheDocument()
    expect(get).not.toHaveBeenCalled()
  })

  test('channel-read permission permits inspection but not operations', async () => {
    useAuthStore.getState().auth.setUser({
      id: 2,
      username: 'viewer',
      role: ROLE.ADMIN,
      permissions: {
        admin_permissions: { channel: { read: true, operate: false } },
      },
    })
    vi.spyOn(api, 'get').mockResolvedValue(listResponse([binding]))
    renderManager({ target })
    expect(
      await screen.findByRole('checkbox', { name: 'Select session session-a' })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByRole('button', { name: 'Reset affinity' })
    ).toBeDisabled()
  })

  test('users without channel-read permission do not load or display bindings', () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 7, username: 'friend', role: ROLE.USER })
    const get = vi.spyOn(api, 'get')
    renderManager({ target })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(get).not.toHaveBeenCalled()
  })
})

describe('affinity sessions page', () => {
  test('canceling a list request and immediately querying the same filters starts a fresh request that succeeds', async () => {
    const originalAdapter = api.defaults.adapter
    const controller = new AbortController()
    const filters = { page: 1, page_size: 20 }
    let notifyStarted!: () => void
    const started = new Promise<void>((resolve) => {
      notifyStarted = resolve
    })
    const adapter = vi.fn<AxiosAdapter>((config) => {
      if (adapter.mock.calls.length === 1) {
        return new Promise((_, reject) => {
          config.signal?.addEventListener?.(
            'abort',
            () => reject(new CanceledError('canceled', config)),
            { once: true }
          )
          notifyStarted()
        })
      }
      return Promise.resolve({
        ...listResponse([binding]),
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      })
    })
    api.defaults.adapter = adapter

    try {
      const first = getAffinityBindings(filters, controller.signal)
      await started
      controller.abort()
      const next = getAffinityBindings(filters, new AbortController().signal)
      const [canceled, reloaded] = await Promise.allSettled([first, next])

      expect(canceled).toMatchObject({
        status: 'rejected',
        reason: { code: 'ERR_CANCELED' },
      })
      expect(reloaded).toEqual({
        status: 'fulfilled',
        value: listResponse([binding]).data.data,
      })
      expect(adapter).toHaveBeenCalledTimes(2)
    } finally {
      controller.abort()
      api.defaults.adapter = originalAdapter
    }
  })

  test('opening the full page lists all bindings without a channel or session scope and without a modal', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue(listResponse([binding, otherChannel]))
    renderWithQueryClient(<ChannelAffinitySessions />)

    expect(await screen.findByText('Other upstream #39')).toBeVisible()
    expect(screen.getByText('Upstream #38')).toBeVisible()
    expect(
      screen.getByRole('heading', { name: 'Affinity sessions' })
    ).toBeVisible()
    expect(screen.getByRole('main')).toBeVisible()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('combobox', { name: 'Operation scope' })
    ).not.toBeInTheDocument()
    expect(get).toHaveBeenCalledExactlyOnceWith(
      '/api/channel/affinity/bindings',
      expect.objectContaining({ params: { page: 1, page_size: 20 } })
    )
    expect(
      within(screen.getByRole('combobox', { name: 'Rows per page' }))
        .getAllByRole('option')
        .map((option) => option.getAttribute('value'))
    ).toEqual(['20', '50', '100', '200'])
    expect(
      screen.getByRole('button', { name: 'Reset affinity' })
    ).toBeDisabled()
  })

  test('an invalid channel ID keeps the query and selection, while valid combined filters and reset filters clear the selection', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue(listResponse([binding]))
    const user = userEvent.setup()
    renderWithQueryClient(<ChannelAffinitySessions />)
    const checkbox = await screen.findByRole('checkbox', {
      name: 'Select session session-a',
    })
    await user.click(checkbox)
    await user.type(screen.getByLabelText('Channel ID'), '-38')
    await user.click(screen.getByRole('button', { name: 'Filter' }))

    expect(await screen.findByText('Enter a positive ID')).toBeVisible()
    expect(screen.getByLabelText('Channel ID')).toHaveAttribute(
      'aria-invalid',
      'true'
    )
    expect(get).toHaveBeenCalledTimes(1)
    expect(checkbox).toBeChecked()

    await user.clear(screen.getByLabelText('Channel ID'))
    await user.type(screen.getByLabelText('Channel ID'), '38')
    await user.type(screen.getByLabelText('User ID'), '7')
    await user.type(screen.getByLabelText('Token ID'), '77')
    await user.type(screen.getByLabelText('Model'), ' gpt-test ')
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith(
        '/api/channel/affinity/bindings',
        expect.objectContaining({
          params: {
            channel_id: 38,
            user_id: 7,
            token_id: 77,
            model: 'gpt-test',
            page: 1,
            page_size: 20,
          },
        })
      )
    )
    const filtered = await screen.findByRole('checkbox', {
      name: 'Select session session-a',
    })
    expect(filtered).not.toBeChecked()
    expect(screen.getByLabelText('Channel ID')).toHaveAttribute(
      'aria-invalid',
      'false'
    )

    await user.click(filtered)
    await user.click(screen.getByRole('button', { name: 'Reset filters' }))
    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith(
        '/api/channel/affinity/bindings',
        expect.objectContaining({ params: { page: 1, page_size: 20 } })
      )
    )
    for (const label of ['Channel ID', 'User ID', 'Token ID', 'Model']) {
      expect(screen.getByLabelText(label)).toHaveValue('')
    }
    expect(
      await screen.findByRole('checkbox', { name: 'Select session session-a' })
    ).not.toBeChecked()
    expect(
      screen.getByRole('button', { name: 'Migrate affinity' })
    ).toBeDisabled()
  })

  test('pagination and page-size changes retain every applied filter and clear the previous selection', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockImplementation(async (_url, config) => {
        const params = config?.params as AffinityBindingFilters
        return listResponse(params.page === 1 ? [binding] : [second], {
          total: 75,
          page: params.page,
          page_size: params.page_size,
        })
      })
    const user = userEvent.setup()
    renderWithQueryClient(<ChannelAffinitySessions />)
    await screen.findByText('Upstream #38')
    await user.type(screen.getByLabelText('Channel ID'), '38')
    await user.type(screen.getByLabelText('User ID'), '7')
    await user.type(screen.getByLabelText('Token ID'), '77')
    await user.type(screen.getByLabelText('Model'), 'gpt-test')
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select session session-a' })
    )
    expect(screen.getByRole('button', { name: 'Reset affinity' })).toBeEnabled()

    await user.click(screen.getByRole('button', { name: 'Next' }))
    const nextPage = await screen.findByRole('checkbox', {
      name: 'Select session session-b',
    })
    const filters = {
      channel_id: 38,
      user_id: 7,
      token_id: 77,
      model: 'gpt-test',
    }
    expect(get).toHaveBeenLastCalledWith(
      '/api/channel/affinity/bindings',
      expect.objectContaining({
        params: { ...filters, page: 2, page_size: 20 },
      })
    )
    expect(nextPage).not.toBeChecked()
    expect(screen.getByText('Selected sessions: 0')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Reset affinity' })
    ).toBeDisabled()

    await user.click(nextPage)
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Rows per page' }),
      '50'
    )
    const resizedPage = await screen.findByRole('checkbox', {
      name: 'Select session session-a',
    })
    expect(get).toHaveBeenLastCalledWith(
      '/api/channel/affinity/bindings',
      expect.objectContaining({
        params: { ...filters, page: 1, page_size: 50 },
      })
    )
    expect(resizedPage).not.toBeChecked()
    expect(screen.getByText('1 / 2')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Migrate affinity' })
    ).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Next' }))
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select session session-b' })
    )
    await user.click(screen.getByRole('button', { name: 'Previous' }))
    expect(
      await screen.findByRole('checkbox', { name: 'Select session session-a' })
    ).not.toBeChecked()
    expect(get).toHaveBeenLastCalledWith(
      '/api/channel/affinity/bindings',
      expect.objectContaining({
        params: { ...filters, page: 1, page_size: 50 },
      })
    )
    expect(screen.getByText('Selected sessions: 0')).toBeVisible()
  })

  test('cross-channel migration submits only explicitly checked refs and the eligible target after confirmation', async () => {
    const unchecked = {
      ...binding,
      id: 'c'.repeat(64),
      key_fp: 'session-c',
      version: 'epoch-3',
    }
    vi.spyOn(api, 'get').mockResolvedValue(
      listResponse([binding, otherChannel, unchecked])
    )
    const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
      if (url.endsWith('/targets')) {
        return {
          data: {
            success: true,
            data: { items: [{ id: 3, name: 'Sub2API' }] },
          },
        }
      }
      return {
        data: {
          success: true,
          data: {
            succeeded: 2,
            expired: 0,
            conflicted: 0,
            failed: 0,
            items: [],
          },
        },
      }
    })
    const refs = [
      { id: binding.id, version: binding.version, channel_id: 38 },
      { id: otherChannel.id, version: otherChannel.version, channel_id: 39 },
    ]
    const user = userEvent.setup()
    renderWithQueryClient(<ChannelAffinitySessions />)
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select session session-a' })
    )
    await user.click(
      screen.getByRole('checkbox', { name: 'Select session session-b' })
    )
    expect(
      screen.getByRole('checkbox', { name: 'Select session session-c' })
    ).not.toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Migrate affinity' }))
    const dialog = screen.getByRole('alertdialog')
    expect(
      within(dialog).getByText('Upstream #38, Other upstream #39')
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Confirm migration' })
    ).toBeDisabled()
    await user.click(
      await screen.findByRole('combobox', { name: 'Target channel' })
    )
    await user.click(await screen.findByRole('option', { name: 'Sub2API #3' }))
    expect(post).toHaveBeenCalledWith(
      '/api/channel/affinity/targets',
      { bindings: refs },
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    expect(post.mock.calls.some(([url]) => url.endsWith('/actions'))).toBe(
      false
    )

    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(post.mock.calls.some(([url]) => url.endsWith('/actions'))).toBe(
      false
    )
    expect(
      screen.getByRole('checkbox', { name: 'Select session session-a' })
    ).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Migrate affinity' }))
    await user.click(
      await screen.findByRole('combobox', { name: 'Target channel' })
    )
    await user.click(await screen.findByRole('option', { name: 'Sub2API #3' }))
    await user.click(screen.getByRole('button', { name: 'Confirm migration' }))
    await waitFor(() =>
      expect(post).toHaveBeenLastCalledWith('/api/channel/affinity/actions', {
        action: 'migrate',
        target_channel_id: 3,
        bindings: refs,
      })
    )
    expect(
      post.mock.calls.filter(([url]) => url.endsWith('/actions'))
    ).toHaveLength(1)
    expect(
      await screen.findByText(
        'Updated: 2 · Expired: 0 · Conflicts: 0 · Failed: 0'
      )
    ).toBeVisible()
    expect(
      screen.getByRole('checkbox', { name: 'Select session session-a' })
    ).not.toBeChecked()
    expect(
      screen.getByRole('checkbox', { name: 'Select session session-b' })
    ).not.toBeChecked()
  })

  test('selecting the current global page can reset bindings from multiple source channels after confirmation', async () => {
    vi.spyOn(api, 'get').mockResolvedValue(
      listResponse([binding, otherChannel])
    )
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: { succeeded: 2, expired: 0, conflicted: 0, failed: 0, items: [] },
      },
    })
    const user = userEvent.setup()
    renderWithQueryClient(<ChannelAffinitySessions />)
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select current page' })
    )
    await user.click(screen.getByRole('button', { name: 'Reset affinity' }))
    expect(post).not.toHaveBeenCalled()
    expect(
      within(screen.getByRole('alertdialog')).getByText(
        'Upstream #38, Other upstream #39'
      )
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Confirm reset' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledExactlyOnceWith(
        '/api/channel/affinity/actions',
        {
          action: 'reset',
          bindings: [
            { id: binding.id, version: binding.version, channel_id: 38 },
            {
              id: otherChannel.id,
              version: otherChannel.version,
              channel_id: 39,
            },
          ],
        }
      )
    )
  })

  test.each([
    ['an ordinary user with a channel-read grant', ROLE.USER, true],
    ['an administrator without channel-read', ROLE.ADMIN, false],
  ] as const)(
    '%s sees forbidden and makes no API calls',
    (_name, role, read) => {
      useAuthStore.getState().auth.setUser({
        id: 2,
        username: 'restricted',
        role,
        permissions: {
          admin_permissions: { channel: { read, operate: true } },
        },
      })
      const get = vi.spyOn(api, 'get')
      const post = vi.spyOn(api, 'post')
      renderWithQueryClient(<ChannelAffinitySessions />)

      expect(screen.getByText('Access Forbidden')).toBeVisible()
      expect(
        screen.queryByRole('heading', { name: 'Affinity sessions' })
      ).not.toBeInTheDocument()
      expect(get).not.toHaveBeenCalled()
      expect(post).not.toHaveBeenCalled()
    }
  )

  test('a read-only administrator can inspect bindings but cannot select or submit any change', async () => {
    useAuthStore.getState().auth.setUser({
      id: 2,
      username: 'viewer',
      role: ROLE.ADMIN,
      permissions: {
        admin_permissions: { channel: { read: true, operate: false } },
      },
    })
    vi.spyOn(api, 'get').mockResolvedValue(
      listResponse([binding, otherChannel])
    )
    const post = vi.spyOn(api, 'post')
    const user = userEvent.setup()
    renderWithQueryClient(<ChannelAffinitySessions />)
    expect(await screen.findByText('Other upstream #39')).toBeVisible()
    for (const checkbox of screen.getAllByRole('checkbox')) {
      expect(checkbox).toHaveAttribute('aria-disabled', 'true')
      await user.click(checkbox)
      expect(checkbox).not.toBeChecked()
    }
    for (const name of ['Reset affinity', 'Migrate affinity']) {
      const button = screen.getByRole('button', { name })
      expect(button).toBeDisabled()
      await user.click(button)
    }
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(post).not.toHaveBeenCalled()
  })

  test('a failed global list can be retried without adding a channel or session scope', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValue(listResponse([binding]))
    const user = userEvent.setup()
    renderWithQueryClient(<ChannelAffinitySessions />)
    expect(
      await screen.findByText('Could not load session bindings')
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Migrate affinity' })
    ).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Upstream #38')).toBeVisible()
    expect(
      screen.queryByText('Could not load session bindings')
    ).not.toBeInTheDocument()
    expect(get).toHaveBeenLastCalledWith(
      '/api/channel/affinity/bindings',
      expect.objectContaining({ params: { page: 1, page_size: 20 } })
    )
  })
})
