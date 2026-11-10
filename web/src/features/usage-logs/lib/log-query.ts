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
import type { GetLogStatsParams } from '../types'
import { buildApiParams } from './utils'

const MAX_ERROR_QUERY_RANGE = 24 * 60 * 60 * 1000

export function hasLogErrorFilters(search: Record<string, unknown>): boolean {
  return [search.errorKeyword, search.errorCode, search.requestResult].some(
    (value) => typeof value === 'string' && value.trim() !== ''
  )
}

export function requiresLogScanBudget(
  search: Record<string, unknown>
): boolean {
  if (hasLogErrorFilters(search)) return true
  if (
    [search.model, search.token, search.username].some(
      (value) => typeof value === 'string' && value.trim() !== ''
    )
  ) {
    return true
  }
  if (search.startTime == null && search.endTime == null) return false
  if (search.startTime == null) return true

  const start = Number(search.startTime)
  const end =
    search.endTime == null
      ? Date.now()
      : Math.min(Number(search.endTime), Date.now())
  return (
    Number.isFinite(start) &&
    Number.isFinite(end) &&
    end >= start &&
    end - start > MAX_ERROR_QUERY_RANGE
  )
}

export function isLiveLogQuery(search: Record<string, unknown>): boolean {
  if (requiresLogScanBudget(search)) return false
  if (search.startTime == null && search.endTime == null) return true

  const now = Date.now()
  const dayStart = new Date(now)
  dayStart.setHours(0, 0, 0, 0)
  const start =
    search.startTime == null ? dayStart.getTime() : Number(search.startTime)
  const end = search.endTime == null ? now : Number(search.endTime)
  return (
    Number.isFinite(start) &&
    Number.isFinite(end) &&
    start <= now &&
    end >= now &&
    end >= start &&
    Math.min(end, now) - start <= MAX_ERROR_QUERY_RANGE
  )
}

export function getLogQueryValidationError(
  search: Record<string, unknown>,
  canManageScope: boolean
): string | undefined {
  if (!hasLogErrorFilters(search)) return undefined
  if (!canManageScope) {
    return 'Error filters are only available to administrators.'
  }

  const start = Number(search.startTime)
  const end = Math.min(Number(search.endTime), Date.now())
  if (
    search.startTime == null ||
    search.endTime == null ||
    !Number.isFinite(start) ||
    !Number.isFinite(end) ||
    end < start ||
    end - start > MAX_ERROR_QUERY_RANGE
  ) {
    return 'Error searches are limited to 24 hours. Shorten the time range.'
  }
  return undefined
}

export function getLogStatsParams(
  search: Record<string, unknown>,
  isAdmin: boolean
): GetLogStatsParams {
  const {
    p: _page,
    page_size: _size,
    cursor: _cursor,
    ...params
  } = buildApiParams({
    page: 1,
    pageSize: 1,
    searchParams: search,
    isAdmin,
  })
  if (params.type === 0) delete params.type
  return Object.fromEntries(
    Object.entries(params).filter(
      ([, value]) => value !== undefined && value !== ''
    )
  )
}

export function getLogStatsQueryKey(
  search: Record<string, unknown>,
  isAdmin: boolean,
  viewAccess: string,
  userId: number | undefined
) {
  const params = getLogStatsParams(search, isAdmin)
  // Implicit today's bounds are resolved when the request runs. They must not
  // create a different cache entry on every render as the clock advances.
  if (search.startTime == null && search.endTime == null) {
    delete params.start_timestamp
    delete params.end_timestamp
  }
  return ['usage-logs-stats', viewAccess, userId, params] as const
}
