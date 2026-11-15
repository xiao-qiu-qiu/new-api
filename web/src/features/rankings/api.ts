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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type { RankingPeriod, RankingsSnapshot } from './types'

type RankingsResponse<T> = {
  success: boolean
  message?: string
  pending?: boolean
  data: T
}

// Keep the existing loading state until a complete snapshot is available.
// Polling belongs to the query's AbortSignal, so leaving a board stops it.
export async function getRankingSnapshot<T>(
  url: string,
  params: Record<string, string>,
  signal?: AbortSignal
): Promise<RankingsResponse<T>> {
  for (;;) {
    signal?.throwIfAborted()
    const response = await api.get<RankingsResponse<T>>(url, { params, signal })
    if (response.status !== 202 || response.data.pending !== true) {
      return requireServerSuccess(response.data)
    }
    const retryAfter = Number(response.headers?.['retry-after'])
    const delay =
      Number.isFinite(retryAfter) && retryAfter > 0
        ? Math.min(5_000, Math.max(500, retryAfter * 1_000))
        : 1_000
    await new Promise<void>((resolve, reject) => {
      const abort = () => {
        clearTimeout(timer)
        signal?.removeEventListener('abort', abort)
        reject(signal?.reason ?? new DOMException('Aborted', 'AbortError'))
      }
      const timer = setTimeout(() => {
        signal?.removeEventListener('abort', abort)
        resolve()
      }, delay)
      signal?.addEventListener('abort', abort, { once: true })
      if (signal?.aborted) abort()
    })
  }
}

export async function getRankings(
  period: RankingPeriod,
  signal?: AbortSignal
): Promise<RankingsResponse<RankingsSnapshot>> {
  return getRankingSnapshot('/api/rankings', { period }, signal)
}
