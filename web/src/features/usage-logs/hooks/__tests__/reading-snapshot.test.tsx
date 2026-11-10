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
import { act, renderHook } from '@testing-library/react'
import { expect, it } from 'vitest'

import { usageLogSchema } from '../../data/schema'
import { useLogReadingSnapshot } from '../use-log-reading-snapshot'

const older = usageLogSchema.parse({
  id: 1,
  request_id: 'older',
  type: 2,
  user_id: 1,
  created_at: 100,
  content: '',
})
const newer = usageLogSchema.parse({ ...older, id: 2, request_id: 'newer' })

it('retains the rows being read until the reader resumes updates', () => {
  const { result, rerender } = renderHook(
    ({ logs }) =>
      useLogReadingSnapshot({ logs, scope: 'self:1', enabled: true }),
    { initialProps: { logs: [older] } }
  )
  act(() => result.current.pause())
  rerender({ logs: [newer, older] })
  expect(result.current.logs).toEqual([older])
  expect(result.current.hasUpdates).toBe(true)
  act(() => result.current.resume())
  expect(result.current.logs).toEqual([newer, older])
  expect(result.current.hasUpdates).toBe(false)
})

it('drops the reading snapshot immediately when the user or search changes', () => {
  const { result, rerender } = renderHook(
    ({ scope, logs }) => useLogReadingSnapshot({ logs, scope, enabled: true }),
    { initialProps: { scope: 'self:1', logs: [older] } }
  )
  act(() => result.current.pause())
  rerender({ scope: 'self:2', logs: [newer] })
  expect(result.current.logs).toEqual([newer])
  expect(result.current.isPaused).toBe(false)
})

it('does not announce unchanged rows as new logs after a background refresh', () => {
  const { result, rerender } = renderHook(
    ({ logs }) =>
      useLogReadingSnapshot({ logs, scope: 'self:1', enabled: true }),
    { initialProps: { logs: [older] } }
  )
  act(() => result.current.pause())
  rerender({ logs: [{ ...older }] })
  expect(result.current.hasUpdates).toBe(false)
})
