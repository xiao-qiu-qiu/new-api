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
import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { usageLogSchema } from '../../data/schema'
import type { ActiveUsageLog } from '../../lib/active-requests'
import { ActiveRequestElapsed } from '../active-request-cells'
import { useCommonLogsColumns } from '../columns/common-logs-columns'
import { UsageLogsMobileList } from '../usage-logs-mobile-card'
import { UsageLogsProvider, useUsageLogsContext } from '../usage-logs-provider'

const activeLog: ActiveUsageLog = {
  ...usageLogSchema.parse({
    id: -1,
    user_id: 7,
    created_at: 100,
    type: 2,
    content: '',
    model_name: 'example-model',
    token_name: 'example-token',
    username: 'example-user',
    channel: 12,
    channel_name: 'example-channel',
    group: 'default',
    request_id: 'request-in-progress',
  }),
  active_request: {
    request_id: 'request-in-progress',
    user_id: 7,
    username: 'example-user',
    token_id: 2,
    token_name: 'example-token',
    model_name: 'example-model',
    group: 'default',
    channel: 12,
    channel_name: 'example-channel',
    started_at: 100_000,
    updated_at: 100_000,
    status: 'waiting',
    is_stream: true,
    attempt: 1,
    client_gone: false,
    clock_offset_ms: 0,
  },
}

function Fixture(props: {
  mobile: boolean
  admin: boolean
  log?: ActiveUsageLog
}) {
  const context = useUsageLogsContext()
  const table = useReactTable({
    data: [props.log ?? activeLog],
    columns: useCommonLogsColumns(props.admin, false),
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <>
      <button type='button' onClick={() => context.setSensitiveVisible(false)}>
        Hide sensitive data
      </button>
      {props.mobile ? (
        <UsageLogsMobileList table={table} logCategory='common' />
      ) : (
        <table>
          <tbody>
            {table.getRowModel().rows.map((row) => (
              <tr key={row.id}>
                {row.getVisibleCells().map((cell) => (
                  <td key={cell.id} aria-label={cell.column.id}>
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}

function renderFixture(mobile: boolean, admin = true, log = activeLog) {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <UsageLogsProvider>
        <Fixture mobile={mobile} admin={admin} log={log} />
      </UsageLogsProvider>
    </QueryClientProvider>
  )
}

afterEach(() => vi.useRealTimers())

it.each([false, true])(
  'shows pending status and unknown amounts instead of finalized billing (mobile=%s)',
  (mobile) => {
    renderFixture(mobile)
    expect(screen.getByText('Waiting for first response')).toBeVisible()
    expect(screen.getByText('example-model')).toBeVisible()
    expect(screen.queryByText('Consume')).not.toBeInTheDocument()
    expect(screen.queryByText(/\$0/)).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Copy request ID' })
    ).toBeVisible()
    if (!mobile) {
      expect(
        within(screen.getByRole('cell', { name: 'quota' })).getByText('—')
      ).toBeVisible()
      expect(
        within(screen.getByRole('cell', { name: 'prompt_tokens' })).getByText(
          '—'
        )
      ).toBeVisible()
    }
  }
)

it('keeps channel and user details out of the self mobile view', () => {
  renderFixture(true, false)
  expect(screen.queryByText('example-channel')).not.toBeInTheDocument()
  expect(screen.queryByText('example-user')).not.toBeInTheDocument()
  expect(screen.getByText('example-token')).toBeVisible()
})

it.each([false, true])(
  'shows the native compaction label while the request is still pending (mobile=%s)',
  (mobile) => {
    renderFixture(mobile, false, {
      ...activeLog,
      active_request: {
        ...activeLog.active_request,
        native_compaction_v2: true,
      },
    })
    expect(screen.getByText('Stream')).toBeVisible()
    expect(screen.getByText('Compaction')).toBeVisible()
    expect(screen.getByText('Waiting for first response')).toBeVisible()
  }
)

it('hides request IDs with the existing sensitive-data switch', async () => {
  const user = userEvent.setup()
  renderFixture(true)
  await user.click(screen.getByRole('button', { name: 'Hide sensitive data' }))
  expect(screen.queryByText('request-in-progress')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Copy request ID' })
  ).not.toBeInTheDocument()
})

it('shows draining after client disconnect without claiming the request completed', () => {
  renderFixture(false, true, {
    ...activeLog,
    active_request: {
      ...activeLog.active_request,
      status: 'draining',
      client_gone: true,
    },
  })
  expect(screen.getByText('Finishing after disconnect')).toBeVisible()
  expect(
    screen.queryByText('Waiting for first response')
  ).not.toBeInTheDocument()
})

it('advances elapsed time locally using the server clock offset', () => {
  vi.useFakeTimers()
  vi.setSystemTime(80_000)
  render(
    <ActiveRequestElapsed
      request={{ ...activeLog.active_request, clock_offset_ms: 30_000 }}
    />
  )
  expect(screen.getByLabelText('Elapsed time')).toHaveTextContent(
    '10 s elapsed'
  )
  act(() => vi.advanceTimersByTime(2000))
  expect(screen.getByLabelText('Elapsed time')).toHaveTextContent(
    '12 s elapsed'
  )
})
