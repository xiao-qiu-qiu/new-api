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
import { act, cleanup, renderHook } from '@testing-library/react'
import { useState, type CompositionEvent } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { useDebouncedColumnFilter } from '@/components/data-table/hooks/use-debounced-column-filter'

import { useTableUrlState } from '../use-table-url-state'

function useFilters(initialSearch: Record<string, unknown>) {
  const [search, setSearch] = useState(initialSearch)
  const table = useTableUrlState({
    search,
    navigate: (options) => {
      const update = options.search
      if (update === true) return
      setSearch((previous) =>
        typeof update === 'function' ? update(previous) : update
      )
    },
    columnFilters: [
      { columnId: 'group', searchKey: 'group', type: 'string' },
      { columnId: '_userId', searchKey: 'user_id', type: 'string' },
    ],
  })
  const group = useDebouncedColumnFilter({
    columnFilters: table.columnFilters,
    columnId: 'group',
    onColumnFiltersChange: table.onColumnFiltersChange,
  })
  const owner = useDebouncedColumnFilter({
    columnFilters: table.columnFilters,
    columnId: '_userId',
    onColumnFiltersChange: table.onColumnFiltersChange,
  })
  return { search, setSearch, table, group, owner }
}

beforeEach(() => {
  vi.useFakeTimers()
  localStorage.clear()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  localStorage.clear()
})

it('commits a typed group only after the debounce and resets pagination', () => {
  const { result } = renderHook(() => useFilters({ group: 'legacy', page: 3 }))
  act(() => result.current.group.setInputValue('premium'))
  act(() => vi.advanceTimersByTime(499))
  expect(result.current.search).toEqual({ group: 'legacy', page: 3 })
  act(() => vi.advanceTimersByTime(1))
  expect(result.current.search).toMatchObject({
    group: 'premium',
    page: undefined,
  })
  expect(result.current.group.inputValue).toBe('premium')
})

it('restores the keyword when browser history returns to an earlier URL', () => {
  const { result } = renderHook(() => useFilters({ filter: 'alice' }))
  act(() => result.current.table.onGlobalFilterChange?.('bob'))
  expect(result.current.search.filter).toBe('bob')
  act(() => result.current.setSearch({ filter: 'alice' }))
  expect(result.current.table.globalFilter).toBe('alice')
})

it('external owner and group navigation cancels an older pending group draft', () => {
  const { result } = renderHook(() =>
    useFilters({ group: 'legacy', user_id: '7' })
  )
  act(() => result.current.group.setInputValue('unfinished'))
  act(() => result.current.setSearch({ group: 'premium', user_id: '8' }))
  expect(result.current.group.inputValue).toBe('premium')
  expect(result.current.owner.inputValue).toBe('8')
  act(() => vi.advanceTimersByTime(500))
  expect(result.current.search).toEqual({ group: 'premium', user_id: '8' })
  expect(result.current.table.columnFilters).toEqual([
    { id: 'group', value: 'premium' },
    { id: '_userId', value: '8' },
  ])
})

it('retains both owner and group when their typed drafts commit together', () => {
  const { result } = renderHook(() => useFilters({}))
  act(() => {
    result.current.group.setInputValue('premium')
    result.current.owner.setInputValue('7')
  })
  act(() => vi.advanceTimersByTime(500))
  expect(result.current.search).toMatchObject({
    group: 'premium',
    user_id: '7',
  })
  expect(result.current.group.inputValue).toBe('premium')
  expect(result.current.owner.inputValue).toBe('7')
})

it('waits for IME composition to finish before committing the final group', () => {
  const { result } = renderHook(() => useFilters({}))
  act(() => {
    result.current.group.onCompositionStart()
    result.current.group.setInputValue('高级')
  })
  act(() => vi.advanceTimersByTime(500))
  expect(result.current.search).toEqual({})
  const input = document.createElement('input')
  input.value = '高级'
  act(() =>
    result.current.group.onCompositionEnd({
      currentTarget: input,
    } as CompositionEvent<HTMLInputElement>)
  )
  act(() => vi.advanceTimersByTime(500))
  expect(result.current.search.group).toBe('高级')
})

it('resumes a pending draft when IME composition ends with the same text', () => {
  const { result } = renderHook(() => useFilters({}))
  act(() => result.current.group.setInputValue('premium'))
  act(() => result.current.group.onCompositionStart())
  act(() => vi.advanceTimersByTime(500))
  expect(result.current.search).toEqual({})
  const input = document.createElement('input')
  input.value = 'premium'
  act(() =>
    result.current.group.onCompositionEnd({
      currentTarget: input,
    } as CompositionEvent<HTMLInputElement>)
  )
  act(() => vi.advanceTimersByTime(500))
  expect(result.current.search.group).toBe('premium')
})
