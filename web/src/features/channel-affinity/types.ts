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
export interface AffinityLogTarget {
  binding_id?: string
  rule_name?: string
  using_group?: string
  key_fp?: string
  user_id?: number
  token_id?: number
}

export interface AffinityBindingRef {
  id: string
  version: string
  channel_id: number
}

export interface AffinityBinding extends AffinityBindingRef {
  channel_name: string
  user_id: number
  user_name: string
  token_id: number
  token_name: string
  rule_name: string
  using_group: string
  models: string[]
  session_mode: string
  expires_at: number
  last_seen_at: number
  shared: boolean
  ownership_known: boolean
  metadata_complete: boolean
  key_fp: string
}

export interface AffinityBindingFilters {
  channel_id?: number
  user_id?: number
  token_id?: number
  model?: string
  binding_id?: string
  rule_name?: string
  using_group?: string
  key_fp?: string
  page: number
  page_size: number
}

export interface AffinityBindingList {
  items: AffinityBinding[]
  total: number
  page: number
  page_size: number
}

export interface AffinityChannelTarget {
  id: number
  name: string
}

export interface AffinityActionRequest {
  action: 'reset' | 'migrate'
  bindings: AffinityBindingRef[]
  target_channel_id?: number
}

export interface AffinityActionResult {
  succeeded: number
  expired: number
  conflicted: number
  failed: number
  items: { id: string; status: string; message?: string }[]
}
