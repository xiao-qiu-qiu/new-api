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
  RankedUser,
  RankingMetric,
} from '../hooks/use-user-rankings'

vi.mock('@/lib/api', () => ({ api: { get: vi.fn(), put: vi.fn() } }))

const clients = new Set<QueryClient>()
const initialPreference: Participation = {
  participating: false,
  spending_participating: false,
  model_details_public: false,
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

function mockRankings(preference: Participation, users: RankedUser[] = []) {
  vi.mocked(api.get).mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.includes('/self/')
        ? preference
        : {
            users,
            participants: users.length,
            total_tokens: 120,
            total_quota: 500000,
          },
    },
  }))
  vi.mocked(api.put).mockImplementation(async (_url, body) => {
    Object.assign(preference, body)
    if ('participating' in (body as Record<string, unknown>)) {
      preference.prompted = true
    }
    return { data: { success: true, data: { ...preference } } }
  })
}

describe('ranking administrator mode', () => {
  const publicUser: RankedUser = {
    rank: 1,
    name: 'p****c',
    total_tokens: 120,
    usage_public: false,
    is_self: false,
  }
  const privateUser: RankedUser = {
    rank: 1,
    name: 'Private nickname',
    nickname: 'Private nickname',
    username: 'private-user',
    total_tokens: 9000,
    total_quota: 500000,
    usage_public: true,
    is_self: false,
    models: [
      { model_name: 'private-model', total_tokens: 9000, total_quota: 500000 },
    ],
  }

  function mockAdministratorRankings(rejected = false) {
    vi.mocked(api.get).mockImplementation(async (url) => {
      if (url.includes('/self/')) {
        return {
          data: {
            success: true,
            data: { ...initialPreference, prompted: true },
          },
        }
      }
      const admin = url.endsWith('/admin')
      if (admin && rejected) {
        return { data: { success: false, message: 'Admin access required' } }
      }
      return {
        data: {
          success: true,
          data: {
            users: admin ? [privateUser] : [publicUser],
            participants: 1,
            total_tokens: admin ? 9000 : 120,
            total_quota: admin ? 500000 : 0,
          },
        },
      }
    })
  }

  test.each([10, 100])(
    'role %s starts in public mode and can toggle private usage on and off',
    async (role) => {
      useAuthStore.getState().auth.setUser({ id: 1, username: 'viewer', role })
      mockAdministratorRankings()
      renderRankings()
      const control = screen.getByRole('switch', { name: 'Administrator mode' })
      expect(control).not.toBeChecked()
      expect(
        await within(await screen.findByRole('table')).findByText('p****c')
      ).toBeVisible()
      expect(api.get).not.toHaveBeenCalledWith(
        '/api/rankings/users/admin',
        expect.anything()
      )

      await userEvent.click(control)
      expect(control).toBeChecked()
      expect(
        await within(await screen.findByRole('table')).findByText(
          'private-user'
        )
      ).toBeVisible()
      expect(screen.queryByText('p****c')).not.toBeInTheDocument()
      expect(
        screen.getAllByRole('button', { name: 'private-model · 100%' }).length
      ).toBeGreaterThan(0)
      expect(api.get).toHaveBeenCalledWith('/api/rankings/users/admin', {
        params: { period: 'week', metric: 'tokens' },
      })
      expect(api.get).toHaveBeenCalledWith('/api/rankings/users/admin', {
        params: { period: 'week', metric: 'spending' },
      })

      await userEvent.click(control)
      expect(control).not.toBeChecked()
      expect(
        await within(await screen.findByRole('table')).findByText('p****c')
      ).toBeVisible()
      expect(screen.queryByText('private-user')).not.toBeInTheDocument()
      expect(
        screen.queryByRole('button', { name: 'private-model · 100%' })
      ).not.toBeInTheDocument()
      expect(api.put).not.toHaveBeenCalled()
    }
  )

  test('an ordinary user sees no administrator switch or private requests', async () => {
    mockAdministratorRankings()
    renderRankings()
    expect(
      await within(await screen.findByRole('table')).findByText('p****c')
    ).toBeVisible()
    expect(
      screen.queryByRole('switch', { name: 'Administrator mode' })
    ).not.toBeInTheDocument()
    expect(api.get).not.toHaveBeenCalledWith(
      '/api/rankings/users/admin',
      expect.anything()
    )
  })

  test('revoking the viewer role immediately removes private data and the switch', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'viewer', role: 10 })
    mockAdministratorRankings()
    renderRankings()
    await userEvent.click(
      screen.getByRole('switch', { name: 'Administrator mode' })
    )
    expect(
      await within(await screen.findByRole('table')).findByText('private-user')
    ).toBeVisible()
    act(() =>
      useAuthStore
        .getState()
        .auth.setUser({ id: 1, username: 'viewer', role: 1 })
    )
    expect(screen.queryByText('private-user')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('switch', { name: 'Administrator mode' })
    ).not.toBeInTheDocument()
    expect(
      await within(await screen.findByRole('table')).findByText('p****c')
    ).toBeVisible()
  })

  test('a rejected administrator request shows an error without reusing public data', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'viewer', role: 10 })
    mockAdministratorRankings(true)
    renderRankings()
    expect(
      await within(await screen.findByRole('table')).findByText('p****c')
    ).toBeVisible()
    await userEvent.click(
      screen.getByRole('switch', { name: 'Administrator mode' })
    )
    expect(await screen.findByText('Unable to load rankings')).toBeVisible()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })
})

describe('ranking participation', () => {
  test('first joining follows rules, nickname and enabled preferences before confirming participation', async () => {
    mockRankings({ ...initialPreference })
    renderRankings()
    const rules = await screen.findByRole('dialog', { name: 'Ranking rules' })
    const actions = within(rules).getAllByRole('button')
    expect(actions.map((button) => button.textContent)).toEqual([
      'Not joining for now',
      'Join rankings',
      'Close',
    ])
    expect(api.put).not.toHaveBeenCalled()
    await userEvent.click(
      within(rules).getByRole('button', { name: 'Join rankings' })
    )
    const nickname = await screen.findByRole('dialog', {
      name: 'Ranking nickname',
    })
    await userEvent.type(
      within(nickname).getByRole('textbox', { name: 'Nickname' }),
      '星河旅人'
    )
    await userEvent.click(
      within(nickname).getByRole('button', { name: 'Continue' })
    )
    const preferences = await screen.findByRole('dialog', {
      name: 'Your ranking preferences',
    })
    expect(api.put).toHaveBeenCalledExactlyOnceWith(
      '/api/user/self/ranking',
      { nickname: '星河旅人' },
      { skipErrorHandler: true }
    )
    expect(screen.queryByText('Participating')).not.toBeInTheDocument()
    const switches = within(preferences).getAllByRole('switch')
    expect(switches).toHaveLength(3)
    for (const control of switches) expect(control).toBeChecked()
    await userEvent.click(
      within(preferences).getByRole('button', { name: 'Save preferences' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(api.put).toHaveBeenLastCalledWith('/api/user/self/ranking', {
      participating: true,
      spending_participating: true,
      model_details_public: true,
    })
    expect(screen.getByText('Participating')).toBeVisible()
  })

  test.each(['Not joining for now', 'Close'])(
    'a first visitor selecting %s records no participation and can read the board',
    async (action) => {
      mockRankings({ ...initialPreference }, [
        {
          rank: 1,
          name: 'a***e',
          total_tokens: 120,
          usage_public: true,
          is_self: false,
        },
      ])
      renderRankings()
      const rules = await screen.findByRole('dialog', { name: 'Ranking rules' })
      await userEvent.click(within(rules).getByRole('button', { name: action }))
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      )
      expect(api.put).toHaveBeenCalledExactlyOnceWith(
        '/api/user/self/ranking',
        {
          participating: false,
          spending_participating: false,
          model_details_public: false,
        }
      )
      expect(within(screen.getByRole('table')).getByText('a***e')).toBeVisible()
      expect(screen.getByText('Viewing only')).toBeVisible()
    }
  )

  test.each(['', '星河旅人'])(
    'an unchanged first nickname %j continues without a write, and canceling preferences declines',
    async (nickname) => {
      mockRankings({ ...initialPreference, nickname })
      renderRankings()
      const rules = await screen.findByRole('dialog', { name: 'Ranking rules' })
      await userEvent.click(
        within(rules).getByRole('button', { name: 'Join rankings' })
      )
      const nicknameDialog = await screen.findByRole('dialog', {
        name: 'Ranking nickname',
      })
      expect(
        within(nicknameDialog).getByRole('textbox', { name: 'Nickname' })
      ).toHaveValue(nickname)
      await userEvent.click(
        within(nicknameDialog).getByRole('button', { name: 'Continue' })
      )
      const preferences = await screen.findByRole('dialog', {
        name: 'Your ranking preferences',
      })
      expect(api.put).not.toHaveBeenCalled()
      await userEvent.click(
        within(preferences).getByRole('button', { name: 'Not now' })
      )
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      )
      expect(api.put).toHaveBeenCalledExactlyOnceWith(
        '/api/user/self/ranking',
        {
          participating: false,
          spending_participating: false,
          model_details_public: false,
        }
      )
    }
  )

  test('a failed first nickname stays editable and cancel returns to the rules without joining', async () => {
    mockRankings({ ...initialPreference })
    vi.mocked(api.put).mockResolvedValue({
      data: { success: false, message: 'Nickname not saved' },
    })
    renderRankings()
    const rules = await screen.findByRole('dialog', { name: 'Ranking rules' })
    await userEvent.click(
      within(rules).getByRole('button', { name: 'Join rankings' })
    )
    const nickname = await screen.findByRole('dialog', {
      name: 'Ranking nickname',
    })
    await userEvent.type(
      within(nickname).getByRole('textbox', { name: 'Nickname' }),
      '星河旅人'
    )
    await userEvent.click(
      within(nickname).getByRole('button', { name: 'Continue' })
    )
    expect(await within(nickname).findByRole('alert')).toHaveTextContent(
      'Nickname not saved'
    )
    expect(
      within(nickname).getByRole('textbox', { name: 'Nickname' })
    ).toHaveValue('星河旅人')
    expect(
      screen.queryByRole('dialog', { name: 'Your ranking preferences' })
    ).not.toBeInTheDocument()
    await userEvent.click(
      within(nickname).getByRole('button', { name: 'Cancel' })
    )
    expect(
      await screen.findByRole('dialog', { name: 'Ranking rules' })
    ).toBeVisible()
    expect(api.put).toHaveBeenCalledExactlyOnceWith(
      '/api/user/self/ranking',
      { nickname: '星河旅人' },
      { skipErrorHandler: true }
    )
  })

  test.each([
    {
      board: 'Token leaderboard',
      otherBoard: 'Spending leaderboard',
      metric: 'tokens' as const,
      choice: {
        participating: true,
        spending_participating: false,
        model_details_public: true,
      },
    },
    {
      board: 'Spending leaderboard',
      otherBoard: 'Token leaderboard',
      metric: 'spending' as const,
      choice: {
        participating: false,
        spending_participating: true,
        model_details_public: true,
      },
    },
  ])(
    '$board joins independently while one model setting survives leaving both boards',
    async (scenario) => {
      mockRankings({ ...initialPreference, prompted: true })
      renderRankings(scenario.metric)
      await userEvent.click(
        await screen.findByRole('button', { name: 'Ranking preferences' })
      )
      const controls = within(
        screen.getByRole('dialog', { name: 'Your ranking preferences' })
      )
      expect(controls.getAllByRole('switch')).toHaveLength(3)
      for (const control of controls.getAllByRole('switch')) {
        expect(control).not.toBeChecked()
      }
      await userEvent.click(
        controls.getByRole('switch', { name: /^Share model breakdown/ })
      )
      await userEvent.click(
        controls.getByRole('switch', { name: new RegExp(`^${scenario.board}`) })
      )
      expect(
        controls.getByRole('switch', {
          name: new RegExp(`^${scenario.otherBoard}`),
        })
      ).not.toBeChecked()
      await userEvent.click(
        controls.getByRole('button', { name: 'Save preferences' })
      )
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      )
      expect(api.put).toHaveBeenLastCalledWith(
        '/api/user/self/ranking',
        scenario.choice
      )
      expect(screen.getByText('Participating')).toBeVisible()
      await userEvent.click(
        screen.getByRole('button', { name: 'Ranking preferences' })
      )
      const saved = within(
        screen.getByRole('dialog', { name: 'Your ranking preferences' })
      )
      expect(
        saved.getByRole('switch', { name: new RegExp(`^${scenario.board}`) })
      ).toBeChecked()
      expect(
        saved.getByRole('switch', {
          name: new RegExp(`^${scenario.otherBoard}`),
        })
      ).not.toBeChecked()
      expect(
        saved.getByRole('switch', { name: /^Share model breakdown/ })
      ).toBeChecked()
      await userEvent.click(
        saved.getByRole('switch', { name: new RegExp(`^${scenario.board}`) })
      )
      expect(
        saved.getByRole('switch', { name: /^Share model breakdown/ })
      ).toBeChecked()
      expect(
        saved.getByRole('switch', { name: /^Share model breakdown/ })
      ).not.toHaveAttribute('aria-disabled', 'true')
      await userEvent.click(
        saved.getByRole('button', { name: 'Save preferences' })
      )
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      )
      expect(api.put).toHaveBeenLastCalledWith('/api/user/self/ranking', {
        participating: false,
        spending_participating: false,
        model_details_public: true,
      })
      expect(screen.getByText('Viewing only')).toBeVisible()
    }
  )

  test('a failed initial preference save retains the selected boards and shared disclosure for retry', async () => {
    mockRankings({ ...initialPreference })
    vi.mocked(api.put).mockResolvedValue({
      data: { success: false, message: 'Preference not saved' },
    })
    const client = renderRankings()
    const rules = await screen.findByRole('dialog', { name: 'Ranking rules' })
    await userEvent.click(
      within(rules).getByRole('button', { name: 'Join rankings' })
    )
    const nickname = await screen.findByRole('dialog', {
      name: 'Ranking nickname',
    })
    await userEvent.click(
      within(nickname).getByRole('button', { name: 'Continue' })
    )
    const dialog = await screen.findByRole('dialog', {
      name: 'Your ranking preferences',
    })
    const controls = within(dialog)
    await userEvent.click(
      controls.getByRole('switch', { name: /^Spending leaderboard/ })
    )
    await userEvent.click(
      controls.getByRole('button', { name: 'Save preferences' })
    )
    expect(await controls.findByRole('alert')).toHaveTextContent(
      'Preference not saved'
    )
    expect(screen.getByRole('dialog')).toBeVisible()
    expect(
      controls.getByRole('switch', { name: /^Token leaderboard/ })
    ).toBeChecked()
    expect(
      controls.getByRole('switch', { name: /^Spending leaderboard/ })
    ).not.toBeChecked()
    expect(
      controls.getByRole('switch', {
        name: /^Share model breakdown/,
      })
    ).toBeChecked()
    expect(screen.queryByText('Participating')).not.toBeInTheDocument()
    const choice: ParticipationChoice = {
      participating: true,
      spending_participating: false,
      model_details_public: true,
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

  test('both boards offer five distinct periods and retain their selection until a fresh page session', async () => {
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
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual([
      '24 hours',
      'Today',
      'Week',
      'Month',
      'Year',
    ])
    expect(screen.getByRole('tab', { name: 'Today' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await userEvent.click(screen.getByRole('tab', { name: '24 hours' }))
    expect(screen.getByRole('tab', { name: '24 hours' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    expect(screen.getByRole('tab', { name: 'Today' })).toHaveAttribute(
      'aria-selected',
      'false'
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
    expect(screen.getByRole('tab', { name: 'Today' })).toHaveAttribute(
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
    const publicName = await within(
      await screen.findByRole('table')
    ).findByText('星河旅人')
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
    '%s board shares its total while hiding model details when disclosure is off',
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
                    total_quota: 500000,
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
      expect(
        within(table).getByText(metric === 'tokens' ? '987,654' : '$1')
      ).toBeVisible()
      expect(within(table).queryByText('Usage hidden')).not.toBeInTheDocument()
      expect(within(table).getAllByText('Model usage hidden')).toHaveLength(2)
      expect(
        screen.queryByRole('group', { name: 'Model breakdown' })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole('button', { name: /private-model/, hidden: true })
      ).not.toBeInTheDocument()
      expect(document.body).not.toHaveTextContent(
        /private-model|765,?432|654,?321/
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
