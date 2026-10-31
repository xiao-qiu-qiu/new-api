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
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { RedemptionHistoryDialog } from '../components/dialogs/redemption-history-dialog'
import type { RedemptionRecord } from '../types'

const i18n = createInstance()
let client: QueryClient

const records: RedemptionRecord[] = Array.from({ length: 11 }, (_, index) => ({
  id: index + 1,
  quota: index === 10 ? 1_000_000 : 500_000,
  redeemed_time: new Date(2026, 0, index + 1, 12).getTime() / 1000,
}))
const totals = { total: 11, total_quota: 6_000_000 }
const emptyResponse = {
  data: {
    success: true,
    data: { items: [], total: 0, total_quota: 0 },
  },
}

beforeEach(async () => {
  localStorage.clear()
  useAuthStore.setState(useAuthStore.getInitialState())
  useAuthStore.getState().auth.setUser({
    id: 42,
    username: 'wallet-history-user',
    role: 1,
  })
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG },
  })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  await i18n.use(initReactI18next).init({
    lng: 'en',
    fallbackLng: false,
    keySeparator: false,
    nsSeparator: false,
    resources: { en },
    interpolation: { escapeValue: false },
  })
})

afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.setState(useAuthStore.getInitialState())
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState())
  localStorage.clear()
})

function renderHistory(open = true) {
  return render(
    <RedemptionHistoryDialog open={open} onOpenChange={vi.fn()} />,
    {
      wrapper: ({ children }) => (
        <QueryClientProvider client={client}>
          <I18nextProvider i18n={i18n}>{children}</I18nextProvider>
        </QueryClientProvider>
      ),
    }
  )
}

it('shows the first ten records and keeps all eleven redemptions in the totals after paging', async () => {
  const user = userEvent.setup()
  const getHistory = vi
    .spyOn(api, 'get')
    .mockResolvedValue(emptyResponse)
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: { ...totals, items: records.slice(0, 10) },
      },
    })
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: { ...totals, items: records.slice(10) },
      },
    })
  renderHistory()

  expect(
    await screen.findByRole('row', { name: '2026-01-01 12:00:00 $1' })
  ).toBeVisible()
  expect(getHistory).toHaveBeenNthCalledWith(
    1,
    '/api/user/redemption/self?p=1&page_size=10'
  )
  expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(11)
  expect(
    screen.getByRole('row', { name: '2026-01-10 12:00:00 $1' })
  ).toBeVisible()
  expect(
    screen.getAllByRole('definition').map((item) => item.textContent)
  ).toEqual(['11', '$12'])

  await user.click(
    screen.getByRole('button', { name: i18n.t('Go to next page') })
  )

  expect(
    await screen.findByRole('row', { name: '2026-01-11 12:00:00 $2' })
  ).toBeVisible()
  expect(getHistory).toHaveBeenNthCalledWith(
    2,
    '/api/user/redemption/self?p=2&page_size=10'
  )
  expect(getHistory).toHaveBeenCalledTimes(2)
  expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(2)
  expect(screen.queryByText('2026-01-01 12:00:00')).not.toBeInTheDocument()
  expect(
    screen.getAllByRole('definition').map((item) => item.textContent)
  ).toEqual(['11', '$12'])
  expect(
    screen.getByRole('button', { name: i18n.t('Go to next page') })
  ).toBeDisabled()
})

it('does not request history while closed and shows zero totals and the empty state after opening', async () => {
  const getHistory = vi.spyOn(api, 'get').mockResolvedValue(emptyResponse)
  const view = renderHistory(false)

  expect(getHistory).not.toHaveBeenCalled()
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

  view.rerender(<RedemptionHistoryDialog open onOpenChange={vi.fn()} />)

  expect(
    await screen.findByText(i18n.t('No redemption records yet'))
  ).toBeVisible()
  expect(
    screen.getByText(i18n.t('Your redeemed codes will appear here'))
  ).toBeVisible()
  expect(
    screen.getAllByRole('definition').map((item) => item.textContent)
  ).toEqual(['0', '$0'])
  expect(
    screen.queryByRole('navigation', { name: i18n.t('Page') })
  ).not.toBeInTheDocument()
  expect(getHistory).toHaveBeenCalledOnce()
})

it('shows an error for success:false and lets the user retry the request', async () => {
  const user = userEvent.setup()
  const getHistory = vi
    .spyOn(api, 'get')
    .mockResolvedValue(emptyResponse)
    .mockResolvedValueOnce({
      data: { success: false, message: 'Redemption history unavailable' },
    })
  renderHistory()

  expect(
    await screen.findByText(i18n.t('Failed to load redemption history'))
  ).toBeVisible()
  expect(
    screen.queryByText(i18n.t('No redemption records yet'))
  ).not.toBeInTheDocument()
  expect(getHistory).toHaveBeenCalledOnce()

  await user.click(screen.getByRole('button', { name: i18n.t('Retry') }))

  expect(
    await screen.findByText(i18n.t('No redemption records yet'))
  ).toBeVisible()
  expect(
    screen.queryByText(i18n.t('Failed to load redemption history'))
  ).not.toBeInTheDocument()
  expect(getHistory).toHaveBeenCalledTimes(2)
  expect(getHistory).toHaveBeenLastCalledWith(
    '/api/user/redemption/self?p=1&page_size=10'
  )
})
