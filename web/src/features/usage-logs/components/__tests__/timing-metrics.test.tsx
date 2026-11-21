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

import { STATUS_QUERY_KEY } from '@/lib/status-query'

import { usageLogSchema, type UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
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
  durationMs?: number
  completionTokens?: number
  useTime?: number
  requestPath?: string
  other?: LogOtherData
  excludeFirstResponse?: boolean
  type?: number
  nativeCompactionV2?: boolean
}) {
  const log = usageLogSchema.parse({
    id: 1,
    user_id: 1,
    created_at: 1,
    type: options.type ?? 2,
    content: '',
    use_time: options.useTime ?? 10,
    completion_tokens: options.completionTokens ?? 200,
    is_stream: options.isStream ?? true,
    other: JSON.stringify({
      ...options.other,
      frt: options.frtMs,
      duration_ms: options.durationMs,
      request_path: options.requestPath,
      native_compaction_v2: options.nativeCompactionV2,
    }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(STATUS_QUERY_KEY, {
    log_tps_exclude_first_response: options.excludeFirstResponse,
  })
  return render(
    <QueryClientProvider client={client}>
      <UsageLogsProvider>
        <TimingFixture log={log} mobile={options.mobile ?? false} />
      </UsageLogsProvider>
    </QueryClientProvider>
  )
}

it.each([false, true])(
  'includes the first response wait in TPS and its color by default (mobile=%s)',
  (mobile) => {
    renderTiming({ mobile, frtMs: 6000 })
    expect(screen.getByText('First response')).toBeVisible()
    expect(screen.getByText('20.0 t/s')).toBeVisible()
    expect(screen.getByText('Duration').nextElementSibling).toHaveClass(
      'text-warning'
    )
  }
)

it.each([false, true])(
  'excludes first response time from displayed TPS and its color when enabled (mobile=%s)',
  (mobile) => {
    renderTiming({ mobile, frtMs: 6000, excludeFirstResponse: true })
    expect(screen.getByText('50.0 t/s')).toBeVisible()
    expect(screen.getByText('Duration').nextElementSibling).toHaveClass(
      'text-success'
    )
  }
)

it.each([10000, 11000])(
  'shows no TPS when first response time %s ms leaves no generation time',
  (frtMs) => {
    renderTiming({ frtMs, excludeFirstResponse: true })
    expect(screen.getByText('—')).toBeVisible()
    expect(screen.queryByText(/t\/s$/)).not.toBeInTheDocument()
  }
)

it.each([undefined, -1000])(
  'uses total-duration average when the first-response measurement is absent (%s)',
  (frtMs) => {
    renderTiming({ frtMs, excludeFirstResponse: true })
    expect(screen.getByText('20.0 t/s')).toBeVisible()
  }
)

it.each([false, true])(
  'uses total-duration average for non-streaming logs even if they contain frt (mobile=%s)',
  (mobile) => {
    renderTiming({
      mobile,
      isStream: false,
      frtMs: 6000,
      excludeFirstResponse: true,
    })
    expect(screen.getByText('20.0 t/s')).toBeVisible()
    expect(screen.queryByText('First response')).not.toBeInTheDocument()
  }
)

it.each([
  { excludeFirstResponse: false, expected: '10.0 t/s' },
  { excludeFirstResponse: true, expected: '20.0 t/s' },
])(
  'uses measured milliseconds for subsecond requests ($expected)',
  (options) => {
    renderTiming({
      ...options,
      useTime: 0,
      durationMs: 400,
      frtMs: 200,
      completionTokens: 4,
    })
    expect(screen.getByText(options.expected)).toBeVisible()
    expect(screen.getByText('0.4s')).toBeVisible()
  }
)

it.each([
  { completionTokens: 1 },
  { durationMs: 0 },
  { requestPath: '/v1/images/generations' },
  { requestPath: '/v1/videos' },
  { other: { billing_tokens: { img_o: 100 } } },
  { other: { usage_facts: { video_tokens: 100 } } },
  {
    other: {
      tool_surcharges: [{ name: 'image_generation', count: 1, price: 0.04 }],
    },
  },
])(
  'does not show text TPS for insufficient or media samples (%j)',
  (options) => {
    renderTiming(options)
    expect(screen.getByText('—')).toBeVisible()
    expect(screen.queryByText(/t\/s$/)).not.toBeInTheDocument()
  }
)

it('keeps text TPS when the image is input rather than output', () => {
  renderTiming({
    other: {
      image: true,
      image_output: 100,
      billing_tokens: { img: 100, img_o: 0 },
    },
  })
  expect(screen.getByText('20.0 t/s')).toBeVisible()
})

it.each([
  { mobile: false, type: 2 },
  { mobile: true, type: 2 },
  { mobile: false, type: 5 },
  { mobile: true, type: 5 },
])(
  'shows compaction alongside the transport for native requests (mobile=$mobile, type=$type)',
  (options) => {
    renderTiming({ ...options, nativeCompactionV2: true })
    expect(screen.getByText('Stream')).toBeVisible()
    expect(screen.getByText('Compaction')).toBeVisible()
  }
)

it.each([undefined, false])(
  'keeps ordinary and older logs without a compaction label when the marker is %s',
  (nativeCompactionV2) => {
    renderTiming({ nativeCompactionV2 })
    expect(screen.getByText('Stream')).toBeVisible()
    expect(screen.queryByText('Compaction')).not.toBeInTheDocument()
  }
)
