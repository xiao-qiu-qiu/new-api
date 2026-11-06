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
import { z } from 'zod'

import type { ProbeConfig } from '../types'

export const probeSchema = z
  .object({
    scope: z.enum(['upstream', 'group']),
    group_name: z.string().trim().max(64),
    models: z
      .array(z.string().trim().min(1).max(191))
      .min(1, 'Select at least one model')
      .max(32, 'Select at most 32 models'),
    sort_order: z.number().int().min(0).max(1000000),
    channel_id: z.number().int().min(0),
    public_name: z
      .string()
      .trim()
      .max(128, 'Public name must be at most 128 characters'),
    model: z.string().trim().max(191, 'Model must be at most 191 characters'),
    enabled: z.boolean(),
    public: z.boolean(),
    active_enabled: z.boolean(),
    passive_enabled: z.boolean(),
    interval_seconds: z
      .number()
      .int()
      .min(30, 'Interval must be between 30 and 86400 seconds')
      .max(86400, 'Interval must be between 30 and 86400 seconds'),
    timeout_seconds: z
      .number()
      .int()
      .min(5, 'Timeout must be between 5 and 300 seconds')
      .max(300, 'Timeout must be between 5 and 300 seconds'),
    green_ms: z
      .number()
      .int()
      .min(1, 'Threshold must be between 1 and 300000 ms')
      .max(300000, 'Threshold must be between 1 and 300000 ms'),
    yellow_ms: z
      .number()
      .int()
      .min(1, 'Threshold must be between 1 and 300000 ms')
      .max(300000, 'Threshold must be between 1 and 300000 ms'),
  })
  .superRefine((value, ctx) => {
    if (value.scope === 'upstream' && value.channel_id < 1) {
      ctx.addIssue({
        code: 'custom',
        path: ['channel_id'],
        message: 'Select a valid channel ID',
      })
    }
    if (value.scope === 'group' && !value.group_name) {
      ctx.addIssue({
        code: 'custom',
        path: ['group_name'],
        message: 'Select a group',
      })
    }
    if (value.enabled && !value.active_enabled && !value.passive_enabled) {
      ctx.addIssue({
        code: 'custom',
        path: ['active_enabled'],
        message: 'Enable active probes or passive observations',
      })
    }
    if (value.public && !value.public_name) {
      ctx.addIssue({
        code: 'custom',
        path: ['public_name'],
        message: 'A public name is required when publishing a probe',
      })
    }
    if (value.timeout_seconds > value.interval_seconds) {
      ctx.addIssue({
        code: 'custom',
        path: ['timeout_seconds'],
        message: 'Timeout must not exceed the probe interval',
      })
    }
    if (value.green_ms > value.yellow_ms) {
      ctx.addIssue({
        code: 'custom',
        path: ['yellow_ms'],
        message: 'Yellow threshold must be at least the green threshold',
      })
    }
  })

export const defaultProbe: ProbeConfig = {
  scope: 'upstream',
  group_name: '',
  models: [],
  sort_order: 0,
  channel_id: 0,
  public_name: '',
  model: '',
  enabled: true,
  public: false,
  active_enabled: true,
  passive_enabled: true,
  interval_seconds: 300,
  timeout_seconds: 60,
  green_ms: 5000,
  yellow_ms: 10000,
}
