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
import { getChannels } from '@/features/channels/api'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type { ChannelProbe, ProbeConfig } from './types'

type Response<T> = { success: boolean; message: string; data: T }
const base = '/api/channel/probes'

export type ProbeChannelOption = { id: number; name: string; models: string }

export type ProbeGroupOption = { name: string; models: string[] }

export async function getProbeGroups(
  signal?: AbortSignal
): Promise<ProbeGroupOption[]> {
  const response = await api.get<Response<ProbeGroupOption[]>>(
    `${base}/groups`,
    { signal }
  )
  return requireServerSuccess(response.data).data ?? []
}

export async function orderChannelProbes(ids: number[]): Promise<void> {
  const response = await api.post<Response<unknown>>(`${base}/order`, { ids })
  requireServerSuccess(response.data)
}

export async function getProbeChannelOptions(): Promise<ProbeChannelOption[]> {
  const channels: ProbeChannelOption[] = []
  let page = 1
  // Read every page so local name/ID search also finds channels beyond page one.
  while (true) {
    const response = requireServerSuccess(
      await getChannels({ p: page, page_size: 100, id_sort: true })
    )
    const data = response.data
    if (!data) throw new Error('Failed to load channels')
    channels.push(
      ...data.items.map((channel) => ({
        id: channel.id,
        name: channel.name,
        models: channel.models,
      }))
    )
    if (!data.items.length || channels.length >= data.total) return channels
    page += 1
  }
}

export async function getChannelProbes(
  signal?: AbortSignal
): Promise<ChannelProbe[]> {
  const response = await api.get<Response<ChannelProbe[]>>(base, { signal })
  return requireServerSuccess(response.data).data ?? []
}

export async function saveChannelProbe(
  config: ProbeConfig,
  id?: number
): Promise<void> {
  config = {
    ...config,
    model: config.models[0] ?? '',
    channel_id: config.scope === 'group' ? 0 : config.channel_id,
    group_name: config.scope === 'group' ? config.group_name : '',
  }
  const response =
    id == null
      ? await api.post<Response<unknown>>(base, config)
      : await api.put<Response<unknown>>(`${base}/${id}`, config)
  requireServerSuccess(response.data)
}

export async function deleteChannelProbe(id: number): Promise<void> {
  const response = await api.delete<Response<unknown>>(`${base}/${id}`)
  requireServerSuccess(response.data)
}

export async function runChannelProbe(id: number): Promise<void> {
  const response = await api.post<Response<{ queued: boolean }>>(
    `${base}/${id}/run`
  )
  requireServerSuccess(response.data)
}
