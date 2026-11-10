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
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  getAllLogs,
  getUserLogs,
  getLogStats,
  getUserLogStats,
} from '../../api'
import {
  getLogQueryValidationError,
  getLogStatsQueryKey,
  hasLogErrorFilters,
  isLiveLogQuery,
  requiresLogScanBudget,
} from '../log-query'
import { buildApiParams, fetchLogsByCategory } from '../utils'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('log query resource controls', () => {
  it('budgets open-ended ranges consistently with the backend defaults', () => {
    const now = Date.now()
    expect(requiresLogScanBudget({})).toBe(false)
    expect(requiresLogScanBudget({ endTime: now })).toBe(true)
    expect(requiresLogScanBudget({ startTime: now - 30 * 86400000 })).toBe(true)
    expect(requiresLogScanBudget({ startTime: now - 3600000 })).toBe(false)
    expect(isLiveLogQuery({ endTime: now + 3600000 })).toBe(false)
    expect(isLiveLogQuery({ startTime: now - 30 * 86400000 })).toBe(false)
  })
  it('requires a shared scan budget for fuzzy names and valid ranges over 24 hours', () => {
    const now = Date.now()
    for (const field of ['model', 'token', 'username']) {
      expect(requiresLogScanBudget({ [field]: 'partial-name' })).toBe(true)
      expect(isLiveLogQuery({ [field]: 'partial-name' })).toBe(false)
      expect(requiresLogScanBudget({ [field]: '  ' })).toBe(false)
    }
    expect(requiresLogScanBudget({ errorCode: '403' })).toBe(true)
    expect(
      requiresLogScanBudget({ startTime: now - 86400000, endTime: now })
    ).toBe(false)
    expect(
      requiresLogScanBudget({ startTime: now - 86401000, endTime: now })
    ).toBe(true)
    expect(
      requiresLogScanBudget({ startTime: now, endTime: now - 86401000 })
    ).toBe(false)
    expect(requiresLogScanBudget({ startTime: 'invalid', endTime: now })).toBe(
      false
    )
    expect(
      requiresLogScanBudget({ requestId: 'request-1', group: 'default' })
    ).toBe(false)
  })
  it('detects expensive filters but leaves request-id and log-type lookups live', () => {
    expect(hasLogErrorFilters({ errorKeyword: '403' })).toBe(true)
    expect(hasLogErrorFilters({ errorCode: '403' })).toBe(true)
    expect(hasLogErrorFilters({ requestResult: 'failed' })).toBe(true)
    expect(hasLogErrorFilters({ errorKeyword: '  ', errorCode: '' })).toBe(
      false
    )
    expect(hasLogErrorFilters({ requestId: 'request-1', type: ['5'] })).toBe(
      false
    )
  })

  it('polls today including an end-of-day endpoint but stops for historical or wide queries', () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 10, 11, 12))
    const start = new Date(2026, 10, 11).getTime()
    const end = new Date(2026, 10, 11, 23, 59, 59, 999).getTime()
    expect(isLiveLogQuery({})).toBe(true)
    expect(isLiveLogQuery({ startTime: start, endTime: end })).toBe(true)
    expect(
      isLiveLogQuery({ startTime: start, endTime: end, errorKeyword: '403' })
    ).toBe(false)
    expect(
      isLiveLogQuery({ startTime: start - 86400000, endTime: start - 1 })
    ).toBe(false)
    expect(
      isLiveLogQuery({ startTime: start - 30 * 86400000, endTime: end })
    ).toBe(false)
    expect(
      isLiveLogQuery({ startTime: start + 86400000, endTime: end + 86400000 })
    ).toBe(false)
  })

  it('accepts exactly 24 hours for admins and rejects invalid, missing, or wider error ranges', () => {
    const search = { errorKeyword: '403', startTime: 1000, endTime: 86401000 }
    expect(getLogQueryValidationError(search, true)).toBeUndefined()
    expect(getLogQueryValidationError(search, false)).toBe(
      'Error filters are only available to administrators.'
    )
    expect(
      getLogQueryValidationError({ ...search, endTime: 86402000 }, true)
    ).toBe('Error searches are limited to 24 hours. Shorten the time range.')
    expect(getLogQueryValidationError({ errorCode: '403' }, true)).toBe(
      'Error searches are limited to 24 hours. Shorten the time range.'
    )
    expect(getLogQueryValidationError({ ...search, endTime: 1 }, true)).toBe(
      'Error searches are limited to 24 hours. Shorten the time range.'
    )
  })

  it('caps a future range endpoint at now before enforcing the 24-hour limit', () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 10, 11, 23, 30))
    expect(
      getLogQueryValidationError(
        {
          errorKeyword: '403',
          startTime: new Date(2026, 10, 11).getTime(),
          endTime: Date.now() + 3600000,
        },
        true
      )
    ).toBeUndefined()
    expect(
      isLiveLogQuery({
        startTime: new Date(2026, 10, 11).getTime(),
        endTime: Date.now() + 3600000,
      })
    ).toBe(true)
  })

  it('shares statistics across pagination and normalizes ignored fields while isolating viewers', () => {
    const filter = {
      type: ['0'],
      model: 'gpt',
      channel: '123',
      username: 'other',
      startTime: 1000,
      endTime: 2000,
    }
    const key = getLogStatsQueryKey(
      { ...filter, page: 1, cursor: 'a' },
      false,
      'self',
      7
    )
    expect(
      getLogStatsQueryKey(
        { ...filter, page: 2, pageSize: 100, cursor: 'b', channel: '456' },
        false,
        'self',
        7
      )
    ).toEqual(key)
    expect(getLogStatsQueryKey(filter, false, 'self', 8)).not.toEqual(key)
    expect(getLogStatsQueryKey(filter, true, 'admin', 7)).not.toEqual(key)
    expect(key.at(-1)).not.toHaveProperty('p')
    expect(key.at(-1)).not.toHaveProperty('cursor')
  })

  it('includes the cursor in list requests without applying it to other log categories', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true } })
    expect(
      buildApiParams({
        page: 2,
        pageSize: 20,
        isAdmin: true,
        searchParams: { cursor: 'cursor-2' },
      })
    ).toHaveProperty('cursor', 'cursor-2')
    const signal = new AbortController().signal
    await fetchLogsByCategory({
      logCategory: 'common',
      isAdmin: true,
      page: 2,
      pageSize: 20,
      searchParams: { cursor: 'cursor-2' },
      columnFilters: [],
      signal,
    })
    expect(get).toHaveBeenLastCalledWith(
      expect.stringContaining('cursor=cursor-2'),
      expect.objectContaining({ signal })
    )
    await fetchLogsByCategory({
      logCategory: 'drawing',
      isAdmin: false,
      page: 1,
      pageSize: 20,
      searchParams: { cursor: 'cursor-2' },
      columnFilters: [],
      signal,
    })
    expect(get.mock.lastCall?.[0]).not.toContain('cursor=')
    expect(get.mock.lastCall?.[1]).toMatchObject({
      signal,
      disableDuplicate: true,
    })
    await fetchLogsByCategory({
      logCategory: 'task',
      isAdmin: false,
      page: 1,
      pageSize: 20,
      searchParams: {},
      columnFilters: [],
    })
    expect(get.mock.lastCall?.[1]).toEqual({ disableDuplicate: false })
  })

  it.each([getAllLogs, getUserLogs, getLogStats, getUserLogStats])(
    'passes cancellation to axios for %s',
    async (fetcher) => {
      const get = vi
        .spyOn(api, 'get')
        .mockResolvedValue({ data: { success: true } })
      const signal = new AbortController().signal
      await fetcher({}, signal)
      expect(get).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ signal, disableDuplicate: true })
      )
    }
  )
})
