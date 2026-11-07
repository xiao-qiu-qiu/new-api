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
import {
  focusManager,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from '@tanstack/react-query'
import { useSyncExternalStore } from 'react'

import {
  getActiveRequests,
  type ActiveRequestSnapshot,
} from '../lib/active-requests'
import { buildApiParams } from '../lib/utils'

export interface UseActiveRequestsOptions {
  enabled: boolean
  isAdmin: boolean
  viewAccess: string
  userId: number | undefined
  searchParams: Record<string, unknown>
  columnFilters: Array<{ id: string; value: unknown }>
}

function isPageVisible(): boolean {
  return focusManager.isFocused()
}

export function useActiveRequests(
  options: UseActiveRequestsOptions
): UseQueryResult<ActiveRequestSnapshot | undefined, Error> {
  const queryClient = useQueryClient()
  const visible = useSyncExternalStore(focusManager.subscribe, isPageVisible)
  const params = buildApiParams({
    page: 1,
    pageSize: Number(options.searchParams.pageSize) || 20,
    searchParams: options.searchParams,
    columnFilters: options.columnFilters,
    isAdmin: options.isAdmin,
  })
  let typeFilter = options.searchParams.type
  for (const filter of options.columnFilters) {
    if (
      filter.id === 'type' &&
      filter.value !== undefined &&
      filter.value !== null &&
      filter.value !== ''
    ) {
      typeFilter = filter.value
    }
  }
  const selectedTypes = Array.isArray(typeFilter) ? typeFilter : [typeFilter]
  const includesConsume =
    selectedTypes.length === 0 ||
    selectedTypes.some(
      (type) => type == null || type === '' || [0, 2].includes(Number(type))
    )
  const hasTimeFilter =
    options.searchParams.startTime ?? options.searchParams.endTime
  const stableFilters = {
    ...params,
    isAdmin: options.isAdmin,
    typeFilter,
    // Default dates move with the clock and belong in the request, not the key.
    start_timestamp: hasTimeFilter ? params.start_timestamp : undefined,
    end_timestamp: hasTimeFilter ? params.end_timestamp : undefined,
  }
  const queryKey = [
    'active-requests',
    options.userId,
    options.viewAccess,
    stableFilters,
  ] as const
  const shouldEnable =
    options.enabled && options.userId != null && includesConsume && visible

  return useQuery({
    queryKey,
    enabled: shouldEnable,
    queryFn: async ({ signal }) => {
      // Build again at fetch time so the implicit today/now range stays current.
      const snapshot = await getActiveRequests(
        buildApiParams({
          page: 1,
          pageSize: params.page_size ?? 20,
          searchParams: options.searchParams,
          columnFilters: options.columnFilters,
          isAdmin: options.isAdmin,
        }),
        options.isAdmin,
        signal
      )
      signal.throwIfAborted()
      const previous = queryClient.getQueryData<ActiveRequestSnapshot>(queryKey)
      const requestIds = new Set(snapshot.items.map((item) => item.request_id))
      if (previous?.items.some((item) => !requestIds.has(item.request_id))) {
        // Keep the previous active rows until the visible persisted logs finish
        // refreshing. Errors retain that snapshot for the next polling attempt.
        await queryClient.invalidateQueries(
          {
            queryKey: ['logs', 'common', options.viewAccess],
            predicate: (query) => query.queryKey.at(-1) === options.userId,
          },
          { throwOnError: true }
        )
        signal.throwIfAborted()
      }
      return snapshot
    },
    refetchInterval: 2000,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
    staleTime: 0,
    placeholderData: undefined,
    select: shouldEnable ? undefined : () => undefined,
    meta: { errorToast: false },
  })
}
