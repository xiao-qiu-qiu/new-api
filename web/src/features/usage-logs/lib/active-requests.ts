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
import { t } from 'i18next'

import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

import type { UsageLog } from '../data/schema'
import type { GetLogsParams } from '../types'
import { buildQueryParams } from './query-params'

export interface ActiveRequest {
  request_id: string
  user_id: number
  username: string
  token_id: number
  token_name: string
  model_name: string
  group: string
  channel: number
  channel_name: string
  started_at: number
  updated_at: number
  status: 'processing' | 'waiting' | 'streaming' | 'retrying' | 'draining'
  is_stream: boolean
  native_compaction_v2?: boolean
  attempt: number
  client_gone: boolean
}

export interface ActiveRequestSnapshot {
  items: ActiveRequest[]
  total: number
  server_time: number
  received_at: number
}

export interface ActiveUsageLog extends UsageLog {
  active_request: ActiveRequest & {
    /** Add to the browser clock to obtain the server clock. */
    clock_offset_ms: number
  }
}

export async function getActiveRequests(
  params: GetLogsParams,
  isAdmin: boolean,
  signal?: AbortSignal
): Promise<ActiveRequestSnapshot> {
  const endpoint = isAdmin ? '/api/log/active' : '/api/log/self/active'
  const response = await api.get<{
    success: boolean
    message?: string
    data?: Omit<ActiveRequestSnapshot, 'received_at'>
  }>(`${endpoint}?${buildQueryParams({ ...params })}`, {
    signal,
    // React Query owns deduplication; the shared GET cache cannot share abort
    // signals or responses across account/permission changes.
    disableDuplicate: true,
    skipErrorHandler: true,
  })
  const payload = response.data
  if (!payload.success || !payload.data) {
    throw createServerError(payload, t('Failed to load logs'))
  }

  return { ...payload.data, received_at: Date.now() }
}

export function getActiveRequest(
  log: UsageLog
): ActiveUsageLog['active_request'] | undefined {
  if ('active_request' in log) {
    return (log as ActiveUsageLog).active_request
  }
  return undefined
}

export function mergeActiveLogs(
  logs: UsageLog[],
  snapshot?: ActiveRequestSnapshot
): UsageLog[] {
  if (!snapshot?.items.length) return logs

  const requestIds = new Set(snapshot.items.map((item) => item.request_id))
  const clockOffset = snapshot.server_time - snapshot.received_at
  const activeLogs: ActiveUsageLog[] = snapshot.items.map((request, index) => ({
    id: -index - 1,
    user_id: request.user_id,
    created_at: Math.floor(request.started_at / 1000),
    type: 2,
    content: '',
    username: request.username,
    token_name: request.token_name,
    model_name: request.model_name,
    // These satisfy the persisted log shape. Active UI renders unknown values.
    quota: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: request.is_stream,
    channel: request.channel,
    channel_name: request.channel_name,
    token_id: request.token_id,
    group: request.group,
    ip: '',
    other: '',
    request_id: request.request_id,
    upstream_request_id: '',
    active_request: { ...request, clock_offset_ms: clockOffset },
  }))

  return [
    ...activeLogs,
    ...logs.filter((log) => log.type !== 2 || !requestIds.has(log.request_id)),
  ]
}
