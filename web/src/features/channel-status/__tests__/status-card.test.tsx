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
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { StatusCard } from '../components/status-card'
import { getWindowAvailability } from '../lib/status'
import type { ProbeResult, PublicChannelStatus } from '../types'

const sample: ProbeResult = {
  checked_at: 1700000000,
  source: 'active',
  success: true,
  ttft_ms: 250,
  latency_ms: 600,
  status: 'green',
  error_code: '',
}
const probe: PublicChannelStatus = {
  public_name: 'Public group',
  scope: 'group',
  model: 'gpt-test',
  green_ms: 5000,
  yellow_ms: 10000,
  status: 'green',
  stale: false,
  latest: sample,
  history: [sample],
  window_start: 1699996400,
  window_end: 1700000000,
  sample_count: 100,
  success_count: 97,
  timeline: [
    {
      start_at: 1699996400,
      end_at: 1699996460,
      count: 0,
      success_count: 0,
      ttft_ms: null,
      status: 'unknown',
    },
    {
      start_at: 1699999940,
      end_at: 1700000000,
      count: 100,
      success_count: 97,
      ttft_ms: 250,
      status: 'red',
    },
  ],
}

describe('model status measurements', () => {
  it('keeps unsampled periods gray without inventing latency or availability', () => {
    render(
      <StatusCard
        probe={{
          ...probe,
          latest: null,
          history: [],
          sample_count: 0,
          success_count: 0,
          timeline: [probe.timeline[0]],
        }}
      />
    )
    expect(screen.getByText('Awaiting the first sample')).toBeVisible()
    expect(screen.getByText('Unknown')).toBeVisible()
    expect(screen.queryByText('100')).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /No samples.*0 samples/ })
    ).toBeVisible()
  })
  it('hides stale TTFT while preserving the selected-window availability', () => {
    render(<StatusCard probe={{ ...probe, stale: true }} />)
    expect(screen.getByText('Unknown')).toBeVisible()
    expect(screen.getByText('Stale')).toBeVisible()
    expect(screen.queryByText('0.25')).not.toBeInTheDocument()
    expect(screen.getByText('97')).toBeVisible()
  })
  it('shows timeout independently and orders real time buckets from oldest to latest', () => {
    render(
      <StatusCard
        probe={{
          ...probe,
          status: 'timeout',
          latest: {
            ...sample,
            success: false,
            ttft_ms: null,
            status: 'timeout',
            error_code: 'timeout',
          },
          timeline: [
            probe.timeline[0],
            { ...probe.timeline[1], status: 'timeout' },
          ],
        }}
      />
    )
    expect(screen.getByText('Timed out')).toBeVisible()
    const bars = screen.getAllByRole('button')
    expect(bars[0]).toHaveAccessibleName(/No samples.*0 samples/)
    expect(bars[1]).toHaveAccessibleName(/Timed out.*100 samples/)
    expect(screen.getByText('gpt-test')).toBeVisible()
  })
  it('calculates availability from all samples in the window, not the limited latest history', () => {
    expect(getWindowAvailability(probe)).toBe(97)
    expect(
      getWindowAvailability({ sample_count: 0, success_count: 0 })
    ).toBeNull()
  })
})
