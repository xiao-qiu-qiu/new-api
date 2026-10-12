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
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'

import { usageLogSchema, type UsageLog } from '../../data/schema'
import { useCommonLogsColumns } from '../columns/common-logs-columns'
import { UsageLogsMobileList } from '../usage-logs-mobile-card'
import { UsageLogsProvider } from '../usage-logs-provider'

function TimingFixture(props: { log: UsageLog; mobile: boolean }) {
  const table = useReactTable({
    data: [props.log],
    columns: useCommonLogsColumns(false, false),
    getCoreRowModel: getCoreRowModel(),
  })
  if (props.mobile) {
    return <UsageLogsMobileList table={table} logCategory='common' />
  }
  return table
    .getRowModel()
    .rows[0].getAllCells()
    .filter((cell) => ['is_stream', 'use_time'].includes(cell.column.id))
    .map((cell) => (
      <div key={cell.column.id}>
        {flexRender(cell.column.columnDef.cell, cell.getContext())}
      </div>
    ))
}

function renderTiming(options: {
  mobile?: boolean
  isStream?: boolean
  frtMs?: number
}) {
  const log = usageLogSchema.parse({
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    use_time: 10,
    completion_tokens: 200,
    is_stream: options.isStream ?? true,
    other: JSON.stringify({ frt: options.frtMs }),
  })
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <UsageLogsProvider>
        <TimingFixture log={log} mobile={options.mobile ?? false} />
      </UsageLogsProvider>
    </QueryClientProvider>
  )
}

it.each([false, true])(
  'excludes first-token latency from displayed TPS and its status color (mobile=%s)',
  (mobile) => {
    renderTiming({ mobile, frtMs: 6000 })
    expect(screen.getByText('50 t/s')).toBeVisible()
    expect(screen.getByText('Duration').nextElementSibling).toHaveClass(
      'text-success'
    )
  }
)

it.each([10000, 11000])(
  'shows no TPS when first-token latency %s ms leaves no generation time',
  (frtMs) => {
    renderTiming({ frtMs })
    expect(screen.getByText('—')).toBeVisible()
    expect(screen.queryByText(/t\/s$/)).not.toBeInTheDocument()
  }
)

it.each([undefined, -1000])(
  'uses total-duration average when the first-token measurement is absent (%s)',
  (frtMs) => {
    renderTiming({ frtMs })
    expect(screen.getByText('20 t/s')).toBeVisible()
  }
)

it('uses total-duration average for non-streaming logs even if they contain frt', () => {
  renderTiming({ isStream: false, frtMs: 6000 })
  expect(screen.getByText('20 t/s')).toBeVisible()
  expect(screen.queryByText('First token')).not.toBeInTheDocument()
})
