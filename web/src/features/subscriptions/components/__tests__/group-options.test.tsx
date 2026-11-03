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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SubscriptionsMutateDrawer } from '../subscriptions-mutate-drawer'
import { SubscriptionsProvider } from '../subscriptions-provider'

const clients: QueryClient[] = []
afterEach(() => clients.splice(0).forEach((client) => client.clear()))

test('plan upgrade and downgrade selectors use account groups even when billing groups are cached', async () => {
  const user = userEvent.setup()
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: { success: true, data: url === '/api/group/user' ? ['members'] : [] },
  }))
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  client.setQueryData(['groups'], { success: true, data: ['route-only'] })
  render(
    <QueryClientProvider client={client}>
      <SubscriptionsProvider>
        <SubscriptionsMutateDrawer open onOpenChange={() => {}} />
      </SubscriptionsProvider>
    </QueryClientProvider>
  )
  await waitFor(() => expect(get).toHaveBeenCalledWith('/api/group/user'))
  await user.click(screen.getByRole('combobox', { name: 'Upgrade Group' }))
  expect(
    await screen.findByRole('option', { name: 'members' })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('option', { name: 'route-only' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('option', { name: 'members' }))
  await user.click(screen.getByRole('combobox', { name: 'Downgrade Group' }))
  expect(
    await screen.findByRole('option', { name: 'members' })
  ).toBeInTheDocument()
  expect(get).not.toHaveBeenCalledWith('/api/group/')
})
