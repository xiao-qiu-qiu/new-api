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
import type { ProbeResult, ProbeStatus, PublicChannelStatus } from '../types'

export function getProbeStatus(
  probe: Pick<PublicChannelStatus, 'stale' | 'latest' | 'status'>
): ProbeStatus {
  if (probe.stale || !probe.latest) return 'unknown'
  return probe.status
}

/** Sample success rate, deliberately not a duration-weighted SLA. */
export function getSampleAvailability(history: ProbeResult[]): number | null {
  if (!history.length) return null
  return (
    (history.filter((sample) => sample.success).length / history.length) * 100
  )
}

export const statusColors: Record<ProbeStatus, string> = {
  green: 'bg-emerald-500 dark:bg-emerald-400',
  yellow: 'bg-amber-400 dark:bg-amber-400',
  red: 'bg-red-500 dark:bg-red-400',
  timeout: 'bg-red-900 dark:bg-red-800',
  unknown: 'bg-slate-200 dark:bg-slate-700',
}

export const statusTextColors: Record<ProbeStatus, string> = {
  green: 'text-emerald-600 dark:text-emerald-400',
  yellow: 'text-amber-600 dark:text-amber-400',
  red: 'text-red-500 dark:text-red-400',
  timeout: 'text-red-900 dark:text-red-300',
  unknown: 'text-muted-foreground',
}

export function getWindowAvailability(
  probe: Pick<PublicChannelStatus, 'sample_count' | 'success_count'>
): number | null {
  if (!probe.sample_count) return null
  return (probe.success_count / probe.sample_count) * 100
}
