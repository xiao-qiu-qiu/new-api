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
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { ModelUsageBar } from '../components/model-usage-bar'
import { RankingPeriods } from '../components/ranking-periods'
import { UserLeaderboard } from '../components/user-leaderboard'
import { useRankingPeriods } from '../hooks/use-ranking-periods'
import type {
  Participation,
  ParticipationChoice,
  RankedModel,
  RankingMetric,
} from '../hooks/use-user-rankings'

vi.mock('@/lib/api', () => ({ api: { get: vi.fn(), put: vi.fn() } }))

const clients = new Set<QueryClient>()
const initialPreference: Participation = {
  participating: false,
  spending_participating: false,
  token_usage_public: false,
  spending_usage_public: false,
  prompted: false,
}

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG },
  })
  useRankingPeriods.setState({ users: 'today', models: 'week' })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'viewer', role: 1 })
})

afterEach(() => {
  for (const client of clients) client.clear()
  clients.clear()
  localStorage.clear()
})

function renderRankings(metric: RankingMetric = 'tokens') {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.add(client)
  render(
    <QueryClientProvider client={client}>
      <UserLeaderboard period='week' metric={metric} />
    </QueryClientProvider>
  )
  return client
}

describe('ranking participation', () => {
  test('a new visitor can decline and still read the leaderboard', async () => {
    let preference = { ...initialPreference }
    vi.mocked(api.get).mockImplementation(async (url) => ({
      data: {
        success: true,
        data: url.includes('/self/')
          ? preference
          : {
              users: [
                {
                  rank: 1,
                  name: 'a***e',
                  total_tokens: 120,
                  usage_public: true,
                  is_self: false,
                },
              ],
              participants: 1,
              total_tokens: 120,
            },
      },
    }))
    vi.mocked(api.put).mockImplementation(async (_url, body) => {
      preference = {
        ...(body as ParticipationChoice),
        prompted: true,
      }
      return { data: { success: true, data: preference } }
    })
    const client = renderRankings()
    const dialog = await screen.findByRole('dialog', {
      name: 'Your ranking preferences',
    })
    expect(api.put).not.toHaveBeenCalled()
    expect(
      within(dialog).getByRole('switch', { name: 'Token leaderboard' })
    ).not.toBeChecked()
    expect(
      within(dialog).getByRole('switch', {
        name: 'Show token totals and model breakdown',
      })
    ).toHaveAttribute('aria-disabled', 'true')
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Not now' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(api.put).toHaveBeenCalledWith('/api/user/self/ranking', {
      participating: false,
      spending_participating: false,
      token_usage_public: false,
      spending_usage_public: false,
    })
    expect(within(screen.getByRole('table')).getByText('a***e')).toBeVisible()
    expect(screen.getByText('Viewing only')).toBeVisible()
    client.clear()
  })

  test.each([
    {
      board: 'Token leaderboard',
      details: 'Show token totals and model breakdown',
      otherBoard: 'Spending leaderboard',
      otherDetails: 'Show spending totals and model breakdown',
      metric: 'tokens' as const,
      choice: {
        participating: true,
        spending_participating: false,
        token_usage_public: true,
        spending_usage_public: false,
      },
    },
    {
      board: 'Spending leaderboard',
      details: 'Show spending totals and model breakdown',
      otherBoard: 'Token leaderboard',
      otherDetails: 'Show token totals and model breakdown',
      metric: 'spending' as const,
      choice: {
        participating: false,
        spending_participating: true,
        token_usage_public: false,
        spending_usage_public: true,
      },
    },
  ])(
    '$board saves participation and disclosure independently',
    async (scenario) => {
      let preference = { ...initialPreference }
      vi.mocked(api.get).mockImplementation(async (url) => ({
        data: {
          success: true,
          data: url.includes('/self/')
            ? preference
            : { users: [], participants: 0, total_tokens: 0, total_quota: 0 },
        },
      }))
      vi.mocked(api.put).mockImplementation(async (_url, body) => {
        preference = { ...(body as ParticipationChoice), prompted: true }
        return { data: { success: true, data: preference } }
      })
      renderRankings(scenario.metric)
      const dialog = await screen.findByRole('dialog')
      const controls = within(dialog)
      await userEvent.click(
        controls.getByRole('switch', { name: scenario.board })
      )
      await userEvent.click(
        controls.getByRole('switch', { name: scenario.details })
      )
      expect(
        controls.getByRole('switch', { name: scenario.otherBoard })
      ).not.toBeChecked()
      expect(
        controls.getByRole('switch', { name: scenario.otherDetails })
      ).toHaveAttribute('aria-disabled', 'true')
      await userEvent.click(
        controls.getByRole('button', { name: 'Save preferences' })
      )
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      )
      expect(api.put).toHaveBeenCalledWith(
        '/api/user/self/ranking',
        scenario.choice
      )
      expect(screen.getByText('Participating')).toBeVisible()

      await userEvent.click(
        screen.getByRole('button', { name: 'Ranking preferences' })
      )
      const saved = within(screen.getByRole('dialog'))
      expect(saved.getByRole('switch', { name: scenario.board })).toBeChecked()
      expect(
        saved.getByRole('switch', { name: scenario.details })
      ).toBeChecked()
      expect(
        saved.getByRole('switch', { name: scenario.otherBoard })
      ).not.toBeChecked()
      expect(
        saved.getByRole('switch', { name: scenario.otherDetails })
      ).not.toBeChecked()
      await userEvent.click(saved.getByRole('switch', { name: scenario.board }))
      expect(
        saved.getByRole('switch', { name: scenario.details })
      ).not.toBeChecked()
      expect(
        saved.getByRole('switch', { name: scenario.details })
      ).toHaveAttribute('aria-disabled', 'true')
    }
  )

  test('a failed save retains both board choices and disclosure settings for retry', async () => {
    vi.mocked(api.get).mockImplementation(async (url) => ({
      data: {
        success: true,
        data: url.includes('/self/')
          ? { ...initialPreference }
          : { users: [], participants: 0, total_tokens: 0 },
      },
    }))
    vi.mocked(api.put).mockResolvedValue({
      data: { success: false, message: 'Preference not saved' },
    })
    const client = renderRankings()
    const dialog = await screen.findByRole('dialog')
    const controls = within(dialog)
    await userEvent.click(
      controls.getByRole('switch', { name: 'Token leaderboard' })
    )
    await userEvent.click(
      controls.getByRole('switch', {
        name: 'Show token totals and model breakdown',
      })
    )
    await userEvent.click(
      controls.getByRole('switch', { name: 'Spending leaderboard' })
    )
    await userEvent.click(
      controls.getByRole('button', { name: 'Save preferences' })
    )
    expect(await controls.findByRole('alert')).toHaveTextContent(
      'Preference not saved'
    )
    expect(screen.getByRole('dialog')).toBeVisible()
    expect(
      controls.getByRole('switch', { name: 'Token leaderboard' })
    ).toBeChecked()
    expect(
      controls.getByRole('switch', { name: 'Spending leaderboard' })
    ).toBeChecked()
    expect(
      controls.getByRole('switch', {
        name: 'Show token totals and model breakdown',
      })
    ).toBeChecked()
    expect(
      controls.getByRole('switch', {
        name: 'Show spending totals and model breakdown',
      })
    ).not.toBeChecked()
    expect(screen.queryByText('Participating')).not.toBeInTheDocument()
    const choice: ParticipationChoice = {
      participating: true,
      spending_participating: true,
      token_usage_public: true,
      spending_usage_public: false,
    }
    expect(api.put).toHaveBeenLastCalledWith('/api/user/self/ranking', choice)
    vi.mocked(api.put).mockResolvedValue({
      data: { success: true, data: { ...choice, prompted: true } },
    })
    await userEvent.click(
      controls.getByRole('button', { name: 'Save preferences' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(api.put).toHaveBeenLastCalledWith('/api/user/self/ranking', choice)
    expect(screen.getByText('Participating')).toBeVisible()
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
      ...initialPreference,
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
                  usage_public: true,
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

  test.each(['tokens', 'spending'] as const)(
    '%s board hides sensitive totals and models when disclosure is off',
    async (metric) => {
      vi.mocked(api.get).mockImplementation(async (url) => ({
        data: {
          success: true,
          data: url.includes('/self/')
            ? { ...initialPreference, prompted: true }
            : {
                users: [
                  {
                    rank: 1,
                    name: 'a***e',
                    usage_public: false,
                    total_tokens: 987654,
                    total_quota: 1234567,
                    models: [
                      {
                        model_name: 'private-model',
                        total_tokens: 765432,
                        total_quota: 654321,
                      },
                    ],
                    is_self: false,
                  },
                ],
                participants: 1,
                total_tokens: 0,
                total_quota: 0,
              },
        },
      }))
      renderRankings(metric)
      const table = await screen.findByRole('table')
      expect(within(table).getByText('a***e')).toBeVisible()
      expect(within(table).getByText('Usage hidden')).toBeVisible()
      expect(within(table).getAllByText('Model usage hidden')).toHaveLength(2)
      expect(
        screen.queryByRole('group', { name: 'Model breakdown' })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole('button', { name: /private-model/, hidden: true })
      ).not.toBeInTheDocument()
      expect(document.body).not.toHaveTextContent(
        /private-model|987,?654|765,?432|1,?234,?567|654,?321|\$2\.47/
      )
      expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
    }
  )
})

describe('ranking model disclosure', () => {
  test('mouse hover immediately shows model tokens and follows the next segment without a provider', async () => {
    const user = userEvent.setup()
    const view = render(
      <ModelUsageBar
        metric='tokens'
        entry={{
          rank: 1,
          name: 'a***e',
          is_self: false,
          usage_public: true,
          models: [
            { model_name: 'first-model', total_tokens: 12000 },
            { model_name: 'second-model', total_tokens: 4000 },
          ],
        }}
      />
    )
    try {
      const first = screen.getByRole('button', { name: 'first-model · 75%' })
      const second = screen.getByRole('button', { name: 'second-model · 25%' })
      await user.hover(first)
      expect(screen.getByText('first-model')).toBeVisible()
      expect(screen.getByText('12,000')).toBeVisible()
      expect(first).not.toHaveFocus()

      await user.hover(second)
      expect(screen.getByText('second-model')).toBeVisible()
      expect(screen.getByText('4,000')).toBeVisible()
      expect(screen.queryByText('12,000')).not.toBeInTheDocument()
      expect(screen.queryByText('Spending')).not.toBeInTheDocument()

      await user.unhover(second)
      expect(screen.queryByText('4,000')).not.toBeInTheDocument()
    } finally {
      view.unmount()
    }
  })

  test.each<{
    fields: string
    metric: RankingMetric
    model: RankedModel
    tokens: boolean
    spending: boolean
  }>([
    {
      fields: 'tokens only',
      metric: 'tokens',
      model: { model_name: 'shared-model', total_tokens: 12000 },
      tokens: true,
      spending: false,
    },
    {
      fields: 'tokens and spending on the token board',
      metric: 'tokens',
      model: {
        model_name: 'shared-model',
        total_tokens: 12000,
        total_quota: 500000,
      },
      tokens: true,
      spending: true,
    },
    {
      fields: 'spending only',
      metric: 'spending',
      model: { model_name: 'shared-model', total_quota: 500000 },
      tokens: false,
      spending: true,
    },
  ])('tooltip shows only authorized fields: $fields', async (scenario) => {
    render(
      <ModelUsageBar
        metric={scenario.metric}
        entry={{
          rank: 1,
          name: 'a***e',
          is_self: false,
          usage_public: true,
          models: [scenario.model],
        }}
      />
    )
    await userEvent.tab()
    expect(
      screen.getByRole('button', { name: 'shared-model · 100%' })
    ).toHaveFocus()
    expect(await screen.findByText('100%')).toBeVisible()
    if (scenario.tokens) {
      expect(screen.getByText('Total tokens')).toBeVisible()
      expect(screen.getByText('12,000')).toBeVisible()
    } else {
      expect(screen.queryByText('Total tokens')).not.toBeInTheDocument()
    }
    if (scenario.spending) {
      expect(screen.getByText('Spending')).toBeVisible()
      expect(screen.getByText('$1')).toBeVisible()
    } else {
      expect(screen.queryByText('Spending')).not.toBeInTheDocument()
      expect(screen.queryByText(/\$/)).not.toBeInTheDocument()
    }
  })
})
