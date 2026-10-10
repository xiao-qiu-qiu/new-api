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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { RankingPeriods } from '../components/ranking-periods'
import { UserLeaderboard } from '../components/user-leaderboard'
import { useRankingPeriods } from '../hooks/use-ranking-periods'

vi.mock('@/lib/api', () => ({ api: { get: vi.fn(), put: vi.fn() } }))

beforeEach(() => {
  vi.clearAllMocks()
  useRankingPeriods.setState({ users: 'today', models: 'week' })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'viewer', role: 1 })
})

function renderRankings() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <UserLeaderboard period='week' />
    </QueryClientProvider>
  )
  return client
}

describe('ranking participation', () => {
  test('a new visitor can decline and still read the leaderboard', async () => {
    let preference = { participating: false, prompted: false }
    vi.mocked(api.get).mockImplementation(async (url) => ({
      data: {
        success: true,
        data: url.includes('/self/')
          ? preference
          : {
              users: [
                { rank: 1, name: 'a***e', total_tokens: 120, is_self: false },
              ],
              participants: 1,
              total_tokens: 120,
            },
      },
    }))
    vi.mocked(api.put).mockImplementation(async (_url, body) => {
      preference = {
        participating: (body as { participating: boolean }).participating,
        prompted: true,
      }
      return { data: { success: true, data: preference } }
    })
    const client = renderRankings()
    const dialog = await screen.findByRole('alertdialog')
    expect(api.put).not.toHaveBeenCalled()
    expect(
      within(dialog).getByText(
        'Only token usage is shown. The models you use are never displayed.'
      )
    ).toBeVisible()
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Not now' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(api.put).toHaveBeenCalledWith('/api/user/self/ranking', {
      participating: false,
    })
    expect(within(screen.getByRole('table')).getByText('a***e')).toBeVisible()
    expect(screen.getByText('Viewing only')).toBeVisible()
    client.clear()
  })

  test('a failed join keeps the choice dialog open and does not claim participation', async () => {
    vi.mocked(api.get).mockImplementation(async (url) => ({
      data: {
        success: true,
        data: url.includes('/self/')
          ? { participating: false, prompted: false }
          : { users: [], participants: 0, total_tokens: 0 },
      },
    }))
    vi.mocked(api.put).mockResolvedValue({
      data: { success: false, message: 'Preference not saved' },
    })
    const client = renderRankings()
    const dialog = await screen.findByRole('alertdialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Join rankings' })
    )
    await waitFor(() => expect(api.put).toHaveBeenCalled())
    expect(screen.getByRole('alertdialog')).toBeVisible()
    expect(screen.queryByText('Participating')).not.toBeInTheDocument()
    client.clear()
  })

  test('each board remembers its own selected period until a fresh page session', async () => {
    function Controls() {
      const [board, setBoard] = useState<'users' | 'models'>('users')
      const state = useRankingPeriods()
      return (
        <>
          <button type='button' onClick={() => setBoard('users')}>
            Users
          </button>
          <button type='button' onClick={() => setBoard('models')}>
            Models
          </button>
          <RankingPeriods
            period={state[board]}
            onChange={(period) => state.setPeriod(board, period)}
          />
        </>
      )
    }
    const view = render(<Controls />)
    expect(screen.getByRole('tab', { name: '24 hours' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await userEvent.click(screen.getByRole('tab', { name: 'Month' }))
    await userEvent.click(screen.getByRole('button', { name: 'Models' }))
    expect(screen.getByRole('tab', { name: 'Week' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await userEvent.click(screen.getByRole('tab', { name: 'Year' }))
    await userEvent.click(screen.getByRole('button', { name: 'Users' }))
    expect(screen.getByRole('tab', { name: 'Month' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await userEvent.click(screen.getByRole('button', { name: 'Models' }))
    expect(screen.getByRole('tab', { name: 'Year' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    view.unmount()
    useRankingPeriods.setState(useRankingPeriods.getInitialState(), true)
    render(<Controls />)
    expect(screen.getByRole('tab', { name: '24 hours' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
  })

  test('saving a nickname updates the public name and locks the next edit', async () => {
    let preference = {
      participating: true,
      prompted: true,
      nickname: '',
      next_nickname_change_at: 0,
    }
    vi.mocked(api.get).mockImplementation(async (url) => ({
      data: {
        success: true,
        data: url.includes('/self/')
          ? preference
          : {
              users: [
                {
                  rank: 1,
                  name: preference.nickname || 'a***e',
                  nickname: preference.nickname,
                  masked_username: 'a***e',
                  total_tokens: 120,
                  is_self: true,
                },
              ],
              participants: 1,
              total_tokens: 120,
            },
      },
    }))
    vi.mocked(api.put).mockImplementation(async (_url, body) => {
      preference = {
        ...preference,
        nickname: (body as { nickname: string }).nickname,
        next_nickname_change_at: Math.floor(Date.now() / 1000) + 2592000,
      }
      return { data: { success: true, data: preference } }
    })
    const client = renderRankings()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Set nickname' })
    )
    const dialog = screen.getByRole('dialog')
    await userEvent.type(
      within(dialog).getByRole('textbox', { name: 'Nickname' }),
      '星河旅人'
    )
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    const publicName = await within(screen.getByRole('table')).findByText(
      '星河旅人'
    )
    expect(publicName).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: 'Edit nickname' }))
    expect(screen.getByRole('textbox', { name: 'Nickname' })).toBeDisabled()
    expect(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Save' })
    ).toBeDisabled()
    expect(api.put).toHaveBeenCalledWith(
      '/api/user/self/ranking',
      { nickname: '星河旅人' },
      { skipErrorHandler: true }
    )
    client.clear()
  })
})
