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
import type { ApiResponse, TokenAutoGroupsConfig } from '@/features/keys/types'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  AdminApiKey,
  AdminApiKeyFilters,
  AdminApiKeyGroupUpdate,
} from './types'

export async function getAdminApiKeys(params: AdminApiKeyFilters) {
  const response = await api.get<
    ApiResponse<{ items: AdminApiKey[]; total: number }>
  >('/api/token/admin', { params })
  return requireServerSuccess(response.data).data
}

export async function getAdminApiKeyGroups(ids: number[]) {
  const response = await api.get<ApiResponse<TokenAutoGroupsConfig>>(
    '/api/token/admin/groups',
    { params: { ids: ids.join(',') } }
  )
  return requireServerSuccess(response.data).data
}

export async function updateAdminApiKeyGroups(data: AdminApiKeyGroupUpdate) {
  const response = await api.put<ApiResponse<{ count: number }>>(
    '/api/token/admin/group',
    data
  )
  return requireServerSuccess(response.data).data
}
