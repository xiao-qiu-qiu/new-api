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
export type ProbeStatus = 'green' | 'yellow' | 'red' | 'timeout' | 'unknown'
export type ProbeScope = 'upstream' | 'group'
export type StatusHours = 1 | 6 | 12 | 24

export type ProbeTimeBucket = {
  start_at: number
  end_at: number
  count: number
  success_count: number
  ttft_ms: number | null
  status: ProbeStatus
}

export type ProbeResult = {
  checked_at: number
  source: 'active' | 'passive'
  success: boolean
  ttft_ms: number | null
  latency_ms: number
  status: ProbeStatus
  error_code: string
}

export type PublicChannelStatus = {
  scope: ProbeScope
  public_name: string
  model: string
  green_ms: number
  yellow_ms: number
  status: ProbeStatus
  stale: boolean
  latest: ProbeResult | null
  history: ProbeResult[]
  window_start: number
  window_end: number
  sample_count: number
  success_count: number
  timeline: ProbeTimeBucket[]
}
