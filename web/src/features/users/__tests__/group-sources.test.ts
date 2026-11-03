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
import { expect, test, vi } from 'vitest'

import { getGroups as getBillingGroups } from '@/features/channels/api'
import { getGroups as getSubscriptionGroups } from '@/features/subscriptions/api'
import { api } from '@/lib/api'

import { getGroups as getAccountGroups } from '../api'

test('account and subscription groups use their own endpoint while channels retain billing groups', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url === '/api/group/user' ? ['members'] : ['standard'],
    },
  }))
  expect((await getAccountGroups()).data).toEqual(['members'])
  expect((await getSubscriptionGroups()).data).toEqual(['members'])
  expect((await getBillingGroups()).data).toEqual(['standard'])
  expect(api.get).toHaveBeenNthCalledWith(1, '/api/group/user')
  expect(api.get).toHaveBeenNthCalledWith(2, '/api/group/user')
  expect(api.get).toHaveBeenNthCalledWith(3, '/api/group/')
})
