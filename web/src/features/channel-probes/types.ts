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
import type {
  ProbeScope,
  PublicChannelStatus,
} from '@/features/channel-status/types'

export type ProbeConfig = {
  scope: ProbeScope
  group_name: string
  models: string[]
  sort_order: number
  channel_id: number
  public_name: string
  model: string
  enabled: boolean
  public: boolean
  active_enabled: boolean
  passive_enabled: boolean
  interval_seconds: number
  timeout_seconds: number
  green_ms: number
  yellow_ms: number
}

export type ChannelProbe = ProbeConfig &
  PublicChannelStatus & { id: number; model_statuses?: PublicChannelStatus[] }
